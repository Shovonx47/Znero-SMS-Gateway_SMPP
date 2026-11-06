package country

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
	ErrInvalidCountryCodes    = errors.New("invalid country codes: ISO2 must be 2 characters, ISO3 must be 3 characters, numeric code must be 3 digits")
	ErrEmptyCountryName       = errors.New("country name cannot be empty")
	ErrEmptyCallingCode       = errors.New("calling code cannot be empty")
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
	approvalSvc.RegisterApplier("country", s)
	approvalSvc.RegisterApprovePermission("country", "country.country.approve")
	return s
}

func (s *Service) List(ctx context.Context, tc *tenant.Context, f Filter) (ListResult, error) {
	// Reseller and customer get read-only access to active records only
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

func (s *Service) Get(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Country, error) {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !tc.IsPlatformOrg() && c.Status != "active" {
		return nil, ErrCountryNotFound
	}
	return c, nil
}

func (s *Service) GetHistory(ctx context.Context, tc *tenant.Context, id uuid.UUID) ([]HistoryEntry, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}
	return s.repo.GetHistory(ctx, id)
}

func (s *Service) Create(ctx context.Context, tc *tenant.Context, req CreateCountryRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	req.Name = strings.TrimSpace(req.Name)
	req.ISO2 = strings.ToUpper(strings.TrimSpace(req.ISO2))
	req.ISO3 = strings.ToUpper(strings.TrimSpace(req.ISO3))
	req.NumericCode = strings.TrimSpace(req.NumericCode)
	req.CallingCode = strings.TrimSpace(req.CallingCode)

	if req.Name == "" {
		return nil, ErrEmptyCountryName
	}
	if len(req.ISO2) != 2 || len(req.ISO3) != 3 || len(req.NumericCode) != 3 {
		return nil, ErrInvalidCountryCodes
	}
	if req.CallingCode == "" {
		return nil, ErrEmptyCallingCode
	}

	// Check if user has override permission
	hasOverride := tc.HasPermission("country.country.override")
	if !hasOverride {
		// Route to approval queue (maker-checker)
		proposedJSON, _ := json.Marshal(req)
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:   "country",
			Operation:    "create",
			ProposedData: proposedJSON,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}
		return cr, nil
	}

	// Direct execution with override
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	minLen := req.MinNationalLength
	if minLen <= 0 {
		minLen = 4
	}
	maxLen := req.MaxNationalLength
	if maxLen <= 0 {
		maxLen = 15
	}

	c := Country{
		Name:                    req.Name,
		ISO2:                    req.ISO2,
		ISO3:                    req.ISO3,
		NumericCode:             req.NumericCode,
		CallingCode:             req.CallingCode,
		Continent:               strings.TrimSpace(req.Continent),
		Region:                  strings.TrimSpace(req.Region),
		CurrencyCode:            strings.TrimSpace(req.CurrencyCode),
		Timezones:               req.Timezones,
		NumberingPlanNotes:      strings.TrimSpace(req.NumberingPlanNotes),
		MinNationalLength:       minLen,
		MaxNationalLength:       maxLen,
		SenderIDPolicyNotes:     strings.TrimSpace(req.SenderIDPolicyNotes),
		DNDPolicyNotes:          strings.TrimSpace(req.DNDPolicyNotes),
		ConsentRequirementNotes: strings.TrimSpace(req.ConsentRequirementNotes),
		DataSource:              strings.TrimSpace(req.DataSource),
		VerificationStatus:      "unverified",
		Status:                  "active",
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Created country %s (%s)", c.Name, c.ISO2)
	}

	created, err := s.repo.Create(ctx, tx, c, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "country.country.create",
		Module:      "country",
		EntityType:  "country",
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

func (s *Service) Update(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateCountryRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyMasterData
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	req.Name = strings.TrimSpace(req.Name)
	req.CallingCode = strings.TrimSpace(req.CallingCode)
	if req.Name == "" {
		return nil, ErrEmptyCountryName
	}
	if req.CallingCode == "" {
		return nil, ErrEmptyCallingCode
	}

	hasOverride := tc.HasPermission("country.country.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(req)
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "country",
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

	minLen := req.MinNationalLength
	if minLen <= 0 {
		minLen = existing.MinNationalLength
	}
	maxLen := req.MaxNationalLength
	if maxLen <= 0 {
		maxLen = existing.MaxNationalLength
	}
	verStatus := req.VerificationStatus
	if verStatus == "" {
		verStatus = existing.VerificationStatus
	}

	updateData := Country{
		ID:                      id,
		Name:                    req.Name,
		CallingCode:             req.CallingCode,
		Continent:               strings.TrimSpace(req.Continent),
		Region:                  strings.TrimSpace(req.Region),
		CurrencyCode:            strings.TrimSpace(req.CurrencyCode),
		Timezones:               req.Timezones,
		NumberingPlanNotes:      strings.TrimSpace(req.NumberingPlanNotes),
		MinNationalLength:       minLen,
		MaxNationalLength:       maxLen,
		SenderIDPolicyNotes:     strings.TrimSpace(req.SenderIDPolicyNotes),
		DNDPolicyNotes:          strings.TrimSpace(req.DNDPolicyNotes),
		ConsentRequirementNotes: strings.TrimSpace(req.ConsentRequirementNotes),
		DataSource:              strings.TrimSpace(req.DataSource),
		VerificationStatus:      verStatus,
	}

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Updated country %s (%s)", req.Name, existing.ISO2)
	}

	updated, err := s.repo.Update(ctx, tx, updateData, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "country.country.edit",
		Module:      "country",
		EntityType:  "country",
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

	hasOverride := tc.HasPermission("country.country.override")
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
			EntityType:    "country",
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
		reason = fmt.Sprintf("Changed country status to %s", newStatus)
	}

	updated, err := s.repo.SetStatus(ctx, tx, id, newStatus, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	action := fmt.Sprintf("country.country.%s", newStatus)
	if newStatus == "inactive" {
		action = "country.country.deactivate"
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      action,
		Module:      "country",
		EntityType:  "country",
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

	hasOverride := tc.HasPermission("country.country.override")
	if !hasOverride {
		proposedJSON, _ := json.Marshal(RollbackRequest{Version: targetVersion, Reason: reason})
		currentJSON, _ := json.Marshal(existing)
		v := existing.Version
		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "country",
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
		reason = fmt.Sprintf("Rolled back to version %d", targetVersion)
	}

	rolledBack, err := s.repo.Rollback(ctx, tx, id, targetVersion, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "country.country.rollback",
		Module:      "country",
		EntityType:  "country",
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

// ApplyChange implements approval.ChangeApplier for countries
func (s *Service) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var req CreateCountryRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal create country: %w", err)
		}
		minLen := req.MinNationalLength
		if minLen <= 0 {
			minLen = 4
		}
		maxLen := req.MaxNationalLength
		if maxLen <= 0 {
			maxLen = 15
		}
		c := Country{
			Name:                    req.Name,
			ISO2:                    req.ISO2,
			ISO3:                    req.ISO3,
			NumericCode:             req.NumericCode,
			CallingCode:             req.CallingCode,
			Continent:               req.Continent,
			Region:                  req.Region,
			CurrencyCode:            req.CurrencyCode,
			Timezones:               req.Timezones,
			NumberingPlanNotes:      req.NumberingPlanNotes,
			MinNationalLength:       minLen,
			MaxNationalLength:       maxLen,
			SenderIDPolicyNotes:     req.SenderIDPolicyNotes,
			DNDPolicyNotes:          req.DNDPolicyNotes,
			ConsentRequirementNotes: req.ConsentRequirementNotes,
			DataSource:              req.DataSource,
			VerificationStatus:      "unverified",
			Status:                  "active",
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err := s.repo.Create(ctx, tx, c, cr.CheckerUserID, reason)
		return err

	case "update":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for update")
		}
		var req UpdateCountryRequest
		if err := json.Unmarshal(cr.ProposedData, &req); err != nil {
			return fmt.Errorf("unmarshal update country: %w", err)
		}
		existing, err := s.repo.GetByID(ctx, *cr.EntityID)
		if err != nil {
			return err
		}
		if req.MinNationalLength <= 0 {
			req.MinNationalLength = existing.MinNationalLength
		}
		if req.MaxNationalLength <= 0 {
			req.MaxNationalLength = existing.MaxNationalLength
		}
		if req.VerificationStatus == "" {
			req.VerificationStatus = existing.VerificationStatus
		}
		updateData := Country{
			ID:                      *cr.EntityID,
			Name:                    req.Name,
			CallingCode:             req.CallingCode,
			Continent:               req.Continent,
			Region:                  req.Region,
			CurrencyCode:            req.CurrencyCode,
			Timezones:               req.Timezones,
			NumberingPlanNotes:      req.NumberingPlanNotes,
			MinNationalLength:       req.MinNationalLength,
			MaxNationalLength:       req.MaxNationalLength,
			SenderIDPolicyNotes:     req.SenderIDPolicyNotes,
			DNDPolicyNotes:          req.DNDPolicyNotes,
			ConsentRequirementNotes: req.ConsentRequirementNotes,
			DataSource:              req.DataSource,
			VerificationStatus:      req.VerificationStatus,
		}
		reason := fmt.Sprintf("Approved change request %s: %s", cr.ID, req.Reason)
		_, err = s.repo.Update(ctx, tx, updateData, cr.CheckerUserID, reason)
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
		return fmt.Errorf("unsupported operation: %s", cr.Operation)
	}
}

// Export produces safe CSV or JSON bytes with spreadsheet formula injection protection
func (s *Service) Export(ctx context.Context, tc *tenant.Context, f Filter, format string) ([]byte, string, error) {
	// High limit for export
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

	// CSV export with anti-formula-injection
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := []string{
		"ISO2", "ISO3", "NumericCode", "Name", "CallingCode",
		"Continent", "Region", "CurrencyCode", "MinLength", "MaxLength",
		"Status", "VerificationStatus", "DataSource", "Version",
	}
	if err := w.Write(header); err != nil {
		return nil, "", err
	}

	for _, c := range items {
		row := []string{
			sanitizeCSV(c.ISO2),
			sanitizeCSV(c.ISO3),
			sanitizeCSV(c.NumericCode),
			sanitizeCSV(c.Name),
			sanitizeCSV(c.CallingCode),
			sanitizeCSV(c.Continent),
			sanitizeCSV(c.Region),
			sanitizeCSV(c.CurrencyCode),
			fmt.Sprintf("%d", c.MinNationalLength),
			fmt.Sprintf("%d", c.MaxNationalLength),
			sanitizeCSV(c.Status),
			sanitizeCSV(c.VerificationStatus),
			sanitizeCSV(c.DataSource),
			fmt.Sprintf("%d", c.Version),
		}
		if err := w.Write(row); err != nil {
			return nil, "", err
		}
	}
	w.Flush()

	return buf.Bytes(), "text/csv", nil
}

// sanitizeCSV neutralizes spreadsheet formula injection (=, +, -, @, \t, \r)
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
