package senderid

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	reAlphanumeric = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{3,11}$`)
	reNumeric      = regexp.MustCompile(`^\+?[0-9]{3,15}$`)
	reShortcode    = regexp.MustCompile(`^[0-9]{3,8}$`)

	validSenderTypes = map[string]bool{
		"alphanumeric": true,
		"numeric":      true,
		"shortcode":    true,
	}
	validSenderStatuses = map[string]bool{
		"pending":   true,
		"approved":  true,
		"rejected":  true,
		"suspended": true,
	}
)

type applierFunc func(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error

func (f applierFunc) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	return f(ctx, tx, cr)
}

type Service struct {
	repo        Repository
	auditSvc    *audit.Service
	approvalSvc *approval.Service
}

func NewService(repo Repository, auditSvc *audit.Service, approvalSvc *approval.Service) *Service {
	svc := &Service{
		repo:        repo,
		auditSvc:    auditSvc,
		approvalSvc: approvalSvc,
	}
	if approvalSvc != nil {
		approvalSvc.RegisterApplier("sender_id", applierFunc(svc.applyChange))
		approvalSvc.RegisterApprovePermission("sender_id", "customer.senderid.approve")
	}
	return svc
}

// ValidateSenderIDFormat validates sender_id string against sender_type rules.
func ValidateSenderIDFormat(senderID, senderType string) (string, error) {
	trimmed := strings.TrimSpace(senderID)
	if trimmed == "" {
		return "", fmt.Errorf("%w: sender_id is required", ErrValidation)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: sender_id must not contain control characters", ErrValidation)
		}
	}
	if !validSenderTypes[senderType] {
		return "", fmt.Errorf("%w: invalid sender_type %q (must be alphanumeric, numeric, or shortcode)", ErrValidation, senderType)
	}

	switch senderType {
	case "alphanumeric":
		if len(trimmed) < 3 || len(trimmed) > 11 || !reAlphanumeric.MatchString(trimmed) {
			return "", fmt.Errorf("%w: alphanumeric sender_id must be 3..11 characters ([A-Za-z0-9 _.-])", ErrValidation)
		}
		hasLetter := false
		for _, r := range trimmed {
			if unicode.IsLetter(r) {
				hasLetter = true
				break
			}
		}
		if !hasLetter {
			return "", fmt.Errorf("%w: alphanumeric sender_id must contain at least one letter", ErrValidation)
		}
	case "numeric":
		if !reNumeric.MatchString(trimmed) {
			return "", fmt.Errorf("%w: numeric sender_id must be 3..15 digits", ErrValidation)
		}
	case "shortcode":
		if !reShortcode.MatchString(trimmed) {
			return "", fmt.Errorf("%w: shortcode sender_id must be 3..8 digits", ErrValidation)
		}
	}
	return trimmed, nil
}

func (s *Service) authorizeOrgAccess(ctx context.Context, tc *tenant.Context, targetOrgID uuid.UUID, perm string) (*OrgInfo, error) {
	if tc == nil || !tc.HasPermission(perm) {
		return nil, ErrPermissionDenied
	}
	info, err := s.repo.GetOrgInfo(ctx, targetOrgID)
	if err != nil {
		return nil, err
	}
	if info.Type != "customer" {
		return nil, ErrNotCustomerOrg
	}

	switch tc.ActorOrgType {
	case "platform":
		return info, nil
	case "reseller":
		if info.ParentID == nil || *info.ParentID != tc.ActorOrgID {
			return nil, ErrCrossTenantAccess
		}
		return info, nil
	case "customer":
		if info.ID != tc.ActorOrgID {
			return nil, ErrCrossTenantAccess
		}
		return info, nil
	default:
		return nil, ErrPermissionDenied
	}
}

func hasSenderOverride(tc *tenant.Context) bool {
	return tc != nil && tc.IsPlatformOrg() && tc.HasPermission("customer.senderid.override")
}

func (s *Service) List(ctx context.Context, tc *tenant.Context, filter ListFilter) ([]SenderID, int, error) {
	if tc == nil || !tc.HasPermission("customer.senderid.view") {
		return nil, 0, ErrPermissionDenied
	}
	switch tc.ActorOrgType {
	case "platform":
		// Platform sees all sender IDs
	case "reseller":
		if filter.OrganizationID != nil {
			if _, err := s.authorizeOrgAccess(ctx, tc, *filter.OrganizationID, "customer.senderid.view"); err != nil {
				return nil, 0, err
			}
		}
		resellerID := tc.ActorOrgID
		filter.ParentOrgID = &resellerID
	case "customer":
		if filter.OrganizationID != nil && *filter.OrganizationID != tc.ActorOrgID {
			return nil, 0, ErrCrossTenantAccess
		}
		ownID := tc.ActorOrgID
		filter.OrganizationID = &ownID
	default:
		return nil, 0, ErrPermissionDenied
	}
	return s.repo.List(ctx, filter)
}

func (s *Service) Get(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*SenderID, error) {
	item, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, item.OrganizationID, "customer.senderid.view"); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) Create(ctx context.Context, tc *tenant.Context, req CreateSenderIDRequest, ip, ua string) (*SenderID, error) {
	if req.OrganizationID == uuid.Nil && tc != nil && tc.ActorOrgType == "customer" {
		req.OrganizationID = tc.ActorOrgID
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, req.OrganizationID, "customer.senderid.create"); err != nil {
		return nil, err
	}

	normalized, err := ValidateSenderIDFormat(req.SenderID, req.SenderType)
	if err != nil {
		return nil, err
	}

	status := "pending"
	if req.Status != "" && req.Status != "pending" {
		if !hasSenderOverride(tc) {
			return nil, ErrPermissionDenied
		}
		if !validSenderStatuses[req.Status] {
			return nil, fmt.Errorf("%w: invalid status %q", ErrValidation, req.Status)
		}
		status = req.Status
	}

	item := &SenderID{
		OrganizationID: req.OrganizationID,
		SenderID:       normalized,
		SenderType:     req.SenderType,
		CountryID:      req.CountryID,
		Status:         status,
		ApprovalNotes:  strings.TrimSpace(req.ApprovalNotes),
		EffectiveFrom:  req.EffectiveFrom,
		EffectiveTo:    req.EffectiveTo,
		DataSource:     strings.TrimSpace(req.DataSource),
		CreatedBy:      &tc.ActorUserID,
		UpdatedBy:      &tc.ActorUserID,
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Submitted sender ID %q (%s)", item.SenderID, item.SenderType)
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.Create(ctx, tx, item, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.senderid.create",
		Module:      "customer",
		EntityType:  "sender_id",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetByID(ctx, created.ID)
	if err == nil {
		created = full
	}
	return created, nil
}

// Approve transitions a pending sender ID to approved.
// Requires platform organization + customer.senderid.approve, and enforces maker != checker
// (unless the actor has customer.senderid.override).
func (s *Service) Approve(ctx context.Context, tc *tenant.Context, id uuid.UUID, req ReviewSenderIDRequest, ip, ua string) (*SenderID, error) {
	if tc == nil || !tc.IsPlatformOrg() || (!tc.HasPermission("customer.senderid.approve") && !tc.HasPermission("customer.senderid.override")) {
		return nil, ErrPermissionDenied
	}
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.CreatedBy != nil && *current.CreatedBy == tc.ActorUserID && !hasSenderOverride(tc) {
		return nil, ErrMakerCannotApproveOwnSenderID
	}

	updated := *current
	updated.Status = "approved"
	if strings.TrimSpace(req.Notes) != "" {
		updated.ApprovalNotes = strings.TrimSpace(req.Notes)
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = strings.TrimSpace(req.Notes)
	}
	if reason == "" {
		reason = fmt.Sprintf("Approved sender ID %q", current.SenderID)
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.Update(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.senderid.approve",
		Module:      "customer",
		EntityType:  "sender_id",
		EntityID:    saved.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(saved),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return saved, nil
}

// Reject transitions a sender ID to rejected with a mandatory rejection reason.
func (s *Service) Reject(ctx context.Context, tc *tenant.Context, id uuid.UUID, req ReviewSenderIDRequest, ip, ua string) (*SenderID, error) {
	if tc == nil || !tc.IsPlatformOrg() || (!tc.HasPermission("customer.senderid.approve") && !tc.HasPermission("customer.senderid.override")) {
		return nil, ErrPermissionDenied
	}
	rejectReason := strings.TrimSpace(req.Reason)
	if rejectReason == "" {
		rejectReason = strings.TrimSpace(req.Notes)
	}
	if rejectReason == "" {
		return nil, ErrRejectReasonRequired
	}

	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.CreatedBy != nil && *current.CreatedBy == tc.ActorUserID && !hasSenderOverride(tc) {
		return nil, ErrMakerCannotApproveOwnSenderID
	}

	updated := *current
	updated.Status = "rejected"
	updated.ApprovalNotes = rejectReason
	updated.UpdatedBy = &tc.ActorUserID

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.Update(ctx, tx, &updated, rejectReason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.senderid.reject",
		Module:      "customer",
		EntityType:  "sender_id",
		EntityID:    saved.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(saved),
		Reason:      rejectReason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return saved, nil
}

func (s *Service) Update(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateSenderIDRequest, ip, ua string) (*SenderID, error) {
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, "customer.senderid.edit"); err != nil {
		return nil, err
	}

	updated := *current
	if req.ApprovalNotes != nil {
		updated.ApprovalNotes = strings.TrimSpace(*req.ApprovalNotes)
	}
	if req.EffectiveFrom != nil {
		updated.EffectiveFrom = req.EffectiveFrom
	}
	if req.EffectiveTo != nil {
		updated.EffectiveTo = req.EffectiveTo
	}
	if req.DataSource != nil {
		updated.DataSource = strings.TrimSpace(*req.DataSource)
	}
	if req.Status != nil && *req.Status != current.Status {
		if !validSenderStatuses[*req.Status] {
			return nil, fmt.Errorf("%w: invalid status %q", ErrValidation, *req.Status)
		}
		if (*req.Status == "approved" || *req.Status == "rejected") && !hasSenderOverride(tc) {
			return nil, ErrPermissionDenied
		}
		updated.Status = *req.Status
	}
	if req.Archived != nil {
		updated.Archived = *req.Archived
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Updated sender ID"
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.Update(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.senderid.edit",
		Module:      "customer",
		EntityType:  "sender_id",
		EntityID:    saved.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(saved),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return saved, nil
}

func (s *Service) ListHistory(ctx context.Context, tc *tenant.Context, id uuid.UUID) ([]SenderIDHistory, error) {
	item, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, item.OrganizationID, "customer.senderid.view"); err != nil {
		return nil, err
	}
	return s.repo.ListHistory(ctx, id)
}

func (s *Service) Rollback(ctx context.Context, tc *tenant.Context, id uuid.UUID, targetVersion int, reason, ip, ua string) (*SenderID, error) {
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, "customer.senderid.edit"); err != nil {
		return nil, err
	}
	hist, err := s.repo.GetHistoryVersion(ctx, id, targetVersion)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(hist.Snapshot)
	if err != nil {
		return nil, err
	}
	var restored SenderID
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.ID = current.ID
	restored.OrganizationID = current.OrganizationID
	restored.SenderID = current.SenderID
	restored.SenderType = current.SenderType
	restored.CountryID = current.CountryID
	restored.UpdatedBy = &tc.ActorUserID

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rollback sender ID to version %d", targetVersion)
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.Update(ctx, tx, &restored, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.senderid.rollback",
		Module:      "customer",
		EntityType:  "sender_id",
		EntityID:    saved.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(saved),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return saved, nil
}

func (s *Service) Export(ctx context.Context, tc *tenant.Context, filter ListFilter, format string) ([]byte, string, error) {
	filter.Page = 1
	filter.Limit = 500
	items, _, err := s.List(ctx, tc, filter)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"organization_name", "sender_id", "sender_type", "country_iso2", "status", "approval_notes", "data_source"})
	for _, it := range items {
		iso2 := ""
		if it.CountryISO2 != nil {
			iso2 = *it.CountryISO2
		}
		_ = w.Write([]string{
			sanitizeCSVCell(it.OrganizationName),
			sanitizeCSVCell(it.SenderID),
			sanitizeCSVCell(it.SenderType),
			sanitizeCSVCell(iso2),
			sanitizeCSVCell(it.Status),
			sanitizeCSVCell(it.ApprovalNotes),
			sanitizeCSVCell(it.DataSource),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

func (s *Service) applyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var item SenderID
	if err := json.Unmarshal(cr.ProposedData, &item); err != nil {
		return err
	}
	actorID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		actorID = *cr.CheckerUserID
	}
	item.UpdatedBy = &actorID

	if cr.Operation == "create" {
		item.CreatedBy = &cr.MakerUserID
		_, err := s.repo.Create(ctx, tx, &item, "Approved sender ID creation")
		return err
	}
	if cr.EntityID != nil {
		item.ID = *cr.EntityID
	}
	_, err := s.repo.Update(ctx, tx, &item, "Approved sender ID change")
	return err
}

func sanitizeCSVCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
