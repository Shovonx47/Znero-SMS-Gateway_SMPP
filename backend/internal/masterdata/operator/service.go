package operator

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrPlatformOnlyMasterData = errors.New("master data is platform-owned: only platform organization users can make modifications")
	ErrEmptyOperatorName      = errors.New("operator name cannot be empty")
	ErrInvalidMCCMNC          = errors.New("invalid MCC/MNC: MCC must be 3 digits, MNC must be 2 or 3 digits")
	ErrInvalidCountryID       = errors.New("country ID is required")
)

type Service struct {
	repo        Repository
	approvalSvc *approval.Service
	auditSvc    *audit.Service
	pool        *pgxpool.Pool
}

func NewService(repo Repository, approvalSvc *approval.Service, auditSvc *audit.Service, pool *pgxpool.Pool) *Service {
	s := &Service{
		repo:        repo,
		approvalSvc: approvalSvc,
		auditSvc:    auditSvc,
		pool:        pool,
	}
	approvalSvc.RegisterApplier("operator", s)
	approvalSvc.RegisterApprovePermission("operator", "operator.operator.approve")
	return s
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

func (s *Service) Get(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Operator, error) {
	op, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !tc.IsPlatformOrg() && op.Status != "active" {
		return nil, ErrOperatorNotFound
	}
	return op, nil
}

func (s *Service) GetHistory(ctx context.Context, tc *tenant.Context, id uuid.UUID) ([]HistoryEntry, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}
	return s.repo.GetHistory(ctx, id)
}

func (s *Service) Create(ctx context.Context, tc *tenant.Context, req CreateOperatorRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	req.Name = strings.TrimSpace(req.Name)
	req.MCC = strings.TrimSpace(req.MCC)
	req.MNC = strings.TrimSpace(req.MNC)

	if req.Name == "" {
		return nil, ErrEmptyOperatorName
	}
	if req.CountryID == uuid.Nil {
		return nil, ErrInvalidCountryID
	}
	if len(req.MCC) != 3 || len(req.MNC) < 2 || len(req.MNC) > 3 {
		return nil, ErrInvalidMCCMNC
	}

	hasOverride := tc.HasPermission("operator.operator.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(req)
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:   "operator",
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

	op := Operator{
		Name:               req.Name,
		BrandName:          strings.TrimSpace(req.BrandName),
		ParentCompany:      strings.TrimSpace(req.ParentCompany),
		CountryID:          req.CountryID,
		MCC:                req.MCC,
		MNC:                req.MNC,
		NetworkTechnology:  req.NetworkTechnology,
		Capabilities:       req.Capabilities,
		DataSource:         strings.TrimSpace(req.DataSource),
		VerificationStatus: "unverified",
		EffectiveDate:      req.EffectiveDate,
		Status:             "active",
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Created operator %s (MCC %s, MNC %s)", op.Name, op.MCC, op.MNC)
	}

	created, err := s.repo.Create(ctx, tx, op, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "operator.operator.create",
		Module:      "operator",
		EntityType:  "operator",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return created, nil
}

func (s *Service) Update(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateOperatorRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, ErrEmptyOperatorName
	}

	hasOverride := tc.HasPermission("operator.operator.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(req)
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "operator",
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

	verStatus := req.VerificationStatus
	if verStatus == "" {
		verStatus = existing.VerificationStatus
	}

	updateData := Operator{
		ID:                 id,
		Name:               req.Name,
		BrandName:          strings.TrimSpace(req.BrandName),
		ParentCompany:      strings.TrimSpace(req.ParentCompany),
		NetworkTechnology:  req.NetworkTechnology,
		Capabilities:       req.Capabilities,
		DataSource:         strings.TrimSpace(req.DataSource),
		VerificationStatus: verStatus,
		EffectiveDate:      req.EffectiveDate,
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Updated operator %s", req.Name)
	}

	updated, err := s.repo.Update(ctx, tx, updateData, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "operator.operator.edit",
		Module:      "operator",
		EntityType:  "operator",
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

	hasOverride := tc.HasPermission("operator.operator.override")
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
			EntityType:    "operator",
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
		reason = fmt.Sprintf("Changed operator status to %s", newStatus)
	}

	updated, err := s.repo.SetStatus(ctx, tx, id, newStatus, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	action := fmt.Sprintf("operator.operator.%s", newStatus)
	if newStatus == "inactive" {
		action = "operator.operator.deactivate"
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      action,
		Module:      "operator",
		EntityType:  "operator",
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

	hasOverride := tc.HasPermission("operator.operator.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(RollbackRequest{Version: targetVersion, Reason: reason})
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "operator",
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
		reason = fmt.Sprintf("Rolled back operator to version %d", targetVersion)
	}

	rolledBack, err := s.repo.Rollback(ctx, tx, id, targetVersion, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "operator.operator.rollback",
		Module:      "operator",
		EntityType:  "operator",
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

	return rolledBack, nil
}

func (s *Service) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var req CreateOperatorRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal create operator: %w", err)
		}
		op := Operator{
			Name:               req.Name,
			BrandName:          req.BrandName,
			ParentCompany:      req.ParentCompany,
			CountryID:          req.CountryID,
			MCC:                req.MCC,
			MNC:                req.MNC,
			NetworkTechnology:  req.NetworkTechnology,
			Capabilities:       req.Capabilities,
			DataSource:         req.DataSource,
			VerificationStatus: "unverified",
			EffectiveDate:      req.EffectiveDate,
			Status:             "active",
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Create(ctx, tx, op, cr.CheckerUserID, reason)
		return err

	case "update":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for update")
		}
		var req UpdateOperatorRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal update operator: %w", err)
		}
		updateData := Operator{
			ID:                 *cr.EntityID,
			Name:               req.Name,
			BrandName:          req.BrandName,
			ParentCompany:      req.ParentCompany,
			NetworkTechnology:  req.NetworkTechnology,
			Capabilities:       req.Capabilities,
			DataSource:         req.DataSource,
			VerificationStatus: req.VerificationStatus,
			EffectiveDate:      req.EffectiveDate,
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Update(ctx, tx, updateData, cr.CheckerUserID, reason)
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
		return err

	default:
		return fmt.Errorf("unsupported operator operation: %s", cr.Operation)
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
		"OperatorName", "BrandName", "ParentCompany", "CountryName", "CountryISO2",
		"MCC", "MNC", "NetworkTechnology", "Status", "VerificationStatus", "DataSource", "Version",
	}
	if err := w.Write(header); err != nil {
		return nil, "", err
	}

	for _, op := range items {
		row := []string{
			sanitizeCSV(op.Name),
			sanitizeCSV(op.BrandName),
			sanitizeCSV(op.ParentCompany),
			sanitizeCSV(op.CountryName),
			sanitizeCSV(op.CountryISO2),
			sanitizeCSV(op.MCC),
			sanitizeCSV(op.MNC),
			sanitizeCSV(strings.Join(op.NetworkTechnology, ",")),
			sanitizeCSV(op.Status),
			sanitizeCSV(op.VerificationStatus),
			sanitizeCSV(op.DataSource),
			fmt.Sprintf("%d", op.Version),
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
