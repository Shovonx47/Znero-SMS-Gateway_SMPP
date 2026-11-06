package prefix

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrPlatformOnlyMasterData = errors.New("master data is platform-owned: only platform organization users can make modifications")
	ErrEmptyPrefix            = errors.New("prefix string cannot be empty")
	ErrInvalidCountryID       = errors.New("country ID is required")
)

type ReloadCallback func()

type Service struct {
	repo        Repository
	approvalSvc *approval.Service
	auditSvc    *audit.Service
	pool        *pgxpool.Pool
	mu          sync.RWMutex
	callbacks   []ReloadCallback
}

func NewService(repo Repository, approvalSvc *approval.Service, auditSvc *audit.Service, pool *pgxpool.Pool) *Service {
	s := &Service{
		repo:        repo,
		approvalSvc: approvalSvc,
		auditSvc:    auditSvc,
		pool:        pool,
	}
	approvalSvc.RegisterApplier("prefix", s)
	approvalSvc.RegisterApprovePermission("prefix", "number.prefix.approve")
	return s
}

func (s *Service) OnChange(cb ReloadCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callbacks = append(s.callbacks, cb)
}

func (s *Service) triggerReload() {
	s.mu.RLock()
	cbs := append([]ReloadCallback(nil), s.callbacks...)
	s.mu.RUnlock()
	for _, cb := range cbs {
		go cb()
	}
}

func (s *Service) List(ctx context.Context, tc *tenant.Context, f Filter) (ListResult, error) {
	if !tc.IsPlatformOrg() {
		f.ActiveOnly = true
	}

	items, total, err := s.repo.List(ctx, f)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{
		Items:      items,
		TotalCount: total,
		Page:       f.Page,
		PageSize:   f.PageSize,
	}, nil
}

func (s *Service) ListAllActive(ctx context.Context) ([]NumberPrefix, error) {
	return s.repo.ListAllActive(ctx)
}

func (s *Service) Get(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*NumberPrefix, error) {
	np, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !tc.IsPlatformOrg() && np.Status != "active" {
		return nil, ErrPrefixNotFound
	}
	return np, nil
}

func (s *Service) GetHistory(ctx context.Context, tc *tenant.Context, id uuid.UUID) ([]HistoryEntry, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}
	return s.repo.GetHistory(ctx, id)
}

func (s *Service) Create(ctx context.Context, tc *tenant.Context, req CreatePrefixRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	req.Prefix = strings.TrimSpace(req.Prefix)
	if req.Prefix == "" {
		return nil, ErrEmptyPrefix
	}
	if req.CountryID == uuid.Nil {
		return nil, ErrInvalidCountryID
	}

	hasOverride := tc.HasPermission("number.prefix.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(req)
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:   "prefix",
			Operation:    "create",
			ProposedData: proposedJSON,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	prio := req.Priority
	if prio <= 0 {
		prio = 100
	}
	numType := req.NumberType
	if numType == "" {
		numType = "mobile"
	}

	np := NumberPrefix{
		Prefix:        req.Prefix,
		CountryID:     req.CountryID,
		OperatorID:    req.OperatorID,
		NumberType:    numType,
		Priority:      prio,
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
		DataSource:    strings.TrimSpace(req.DataSource),
		Status:        "active",
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Created prefix %s", np.Prefix)
	}

	created, err := s.repo.Create(ctx, tx, np, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.prefix.create",
		Module:      "number",
		EntityType:  "prefix",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	s.triggerReload()
	return created, nil
}

func (s *Service) Update(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdatePrefixRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	req.Prefix = strings.TrimSpace(req.Prefix)
	if req.Prefix == "" {
		return nil, ErrEmptyPrefix
	}

	hasOverride := tc.HasPermission("number.prefix.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(req)
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "prefix",
			EntityID:      &id,
			Operation:     "update",
			ProposedData:  proposedJSON,
			CurrentData:   currentJSON,
			EntityVersion: &v,
			MakerUserID:   tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	prio := req.Priority
	if prio <= 0 {
		prio = existing.Priority
	}
	numType := req.NumberType
	if numType == "" {
		numType = existing.NumberType
	}

	updateData := NumberPrefix{
		ID:            id,
		Prefix:        req.Prefix,
		OperatorID:    req.OperatorID,
		NumberType:    numType,
		Priority:      prio,
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
		DataSource:    strings.TrimSpace(req.DataSource),
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Updated prefix %s", req.Prefix)
	}

	updated, err := s.repo.Update(ctx, tx, updateData, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.prefix.edit",
		Module:      "number",
		EntityType:  "prefix",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
		Reason:      reason,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	s.triggerReload()
	return updated, nil
}

func (s *Service) SetStatus(ctx context.Context, tc *tenant.Context, id uuid.UUID, newStatus string, reason string) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	hasOverride := tc.HasPermission("number.prefix.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(map[string]string{"status": newStatus, "reason": reason})
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		op := "deactivate"
		if newStatus == "active" {
			op = "activate"
		} else if newStatus == "archived" {
			op = "archive"
		}
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "prefix",
			EntityID:      &id,
			Operation:     op,
			ProposedData:  proposedJSON,
			CurrentData:   currentJSON,
			EntityVersion: &v,
			MakerUserID:   tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if reason == "" {
		reason = fmt.Sprintf("Changed prefix status to %s", newStatus)
	}

	updated, err := s.repo.SetStatus(ctx, tx, id, newStatus, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	action := fmt.Sprintf("number.prefix.%s", newStatus)
	if newStatus == "inactive" {
		action = "number.prefix.deactivate"
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      action,
		Module:      "number",
		EntityType:  "prefix",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
		Reason:      reason,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	s.triggerReload()
	return updated, nil
}

func (s *Service) Rollback(ctx context.Context, tc *tenant.Context, id uuid.UUID, targetVersion int, reason string) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	hasOverride := tc.HasPermission("number.prefix.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(RollbackRequest{Version: targetVersion, Reason: reason})
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "prefix",
			EntityID:      &id,
			Operation:     "rollback",
			ProposedData:  proposedJSON,
			CurrentData:   currentJSON,
			EntityVersion: &v,
			MakerUserID:   tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if reason == "" {
		reason = fmt.Sprintf("Rolled back prefix to version %d", targetVersion)
	}

	rolledBack, err := s.repo.Rollback(ctx, tx, id, targetVersion, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.prefix.rollback",
		Module:      "number",
		EntityType:  "prefix",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(rolledBack),
		Reason:      reason,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	s.triggerReload()
	return rolledBack, nil
}

func (s *Service) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var req CreatePrefixRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal create prefix: %w", err)
		}
		prio := req.Priority
		if prio <= 0 {
			prio = 100
		}
		numType := req.NumberType
		if numType == "" {
			numType = "mobile"
		}
		np := NumberPrefix{
			Prefix:        req.Prefix,
			CountryID:     req.CountryID,
			OperatorID:    req.OperatorID,
			NumberType:    numType,
			Priority:      prio,
			EffectiveFrom: req.EffectiveFrom,
			EffectiveTo:   req.EffectiveTo,
			DataSource:    req.DataSource,
			Status:        "active",
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Create(ctx, tx, np, cr.CheckerUserID, reason)
		if err == nil {
			defer s.triggerReload()
		}
		return err

	case "update":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for update")
		}
		var req UpdatePrefixRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal update prefix: %w", err)
		}
		updateData := NumberPrefix{
			ID:            *cr.EntityID,
			Prefix:        req.Prefix,
			OperatorID:    req.OperatorID,
			NumberType:    req.NumberType,
			Priority:      req.Priority,
			EffectiveFrom: req.EffectiveFrom,
			EffectiveTo:   req.EffectiveTo,
			DataSource:    req.DataSource,
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Update(ctx, tx, updateData, cr.CheckerUserID, reason)
		if err == nil {
			defer s.triggerReload()
		}
		return err

	case "deactivate", "activate", "archive":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for status change")
		}
		var payload map[string]string
		_ = json.Unmarshal(cr.ProposedData, &payload)
		newStatus := payload["status"]
		if newStatus == "" {
			if cr.Operation == "deactivate" {
				newStatus = "inactive"
			} else if cr.Operation == "activate" {
				newStatus = "active"
			} else {
				newStatus = "archived"
			}
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, payload["reason"])
		_, err := s.repo.SetStatus(ctx, tx, *cr.EntityID, newStatus, cr.CheckerUserID, reason)
		if err == nil {
			defer s.triggerReload()
		}
		return err

	case "rollback":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for rollback")
		}
		var req RollbackRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal rollback: %w", err)
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Rollback(ctx, tx, *cr.EntityID, req.Version, cr.CheckerUserID, reason)
		if err == nil {
			defer s.triggerReload()
		}
		return err

	default:
		return fmt.Errorf("unsupported prefix operation: %s", cr.Operation)
	}
}

func (s *Service) Export(ctx context.Context, tc *tenant.Context, f Filter, format string) ([]byte, string, error) {
	f.Page = 1
	f.PageSize = 10000
	if !tc.IsPlatformOrg() {
		f.ActiveOnly = true
	}

	items, _, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, "", err
	}

	if format == "json" {
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return nil, "", err
		}
		return data, "application/json", nil
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := []string{
		"Prefix", "CountryName", "CountryISO2", "OperatorName", "NumberType",
		"Priority", "Status", "DataSource", "Version",
	}
	if err := w.Write(header); err != nil {
		return nil, "", err
	}

	for _, np := range items {
		row := []string{
			sanitizeCSV(np.Prefix),
			sanitizeCSV(np.CountryName),
			sanitizeCSV(np.CountryISO2),
			sanitizeCSV(np.OperatorName),
			sanitizeCSV(np.NumberType),
			fmt.Sprintf("%d", np.Priority),
			sanitizeCSV(np.Status),
			sanitizeCSV(np.DataSource),
			fmt.Sprintf("%d", np.Version),
		}
		if err := w.Write(row); err != nil {
			return nil, "", err
		}
	}
	w.Flush()

	return buf.Bytes(), "text/csv", nil
}

func sanitizeCSV(s string) string {
	if len(s) == 0 {
		return ""
	}
	first := s[0]
	if first == '=' || first == '+' || first == '-' || first == '@' || first == '\t' || first == '\r' {
		return "'" + s
	}
	return s
}
