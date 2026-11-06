package customer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	reCurrencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	reISO2         = regexp.MustCompile(`^[A-Z]{2}$`)
	reSystemID     = regexp.MustCompile(`^[a-zA-Z0-9_]{4,16}$`)

	validAccountTypes = map[string]bool{
		"prepaid":  true,
		"postpaid": true,
	}
	validDestinationPolicies = map[string]bool{
		"allow_all":    true,
		"allow_listed": true,
		"block_listed": true,
	}
	validProfileStatuses = map[string]bool{
		"draft":     true,
		"active":    true,
		"suspended": true,
		"closed":    true,
	}
	validSMPPStatuses = map[string]bool{
		"active":    true,
		"disabled":  true,
		"suspended": true,
	}
	validBindTypes = map[string]bool{
		"transceiver": true,
		"transmitter": true,
		"receiver":    true,
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
		approvalSvc.RegisterApplier("customer_profile", applierFunc(svc.applyProfileChange))
		approvalSvc.RegisterApprovePermission("customer_profile", "customer.profile.approve")
	}
	return svc
}

func (s *Service) authorizeOrgAccess(ctx context.Context, tc *tenant.Context, targetOrgID uuid.UUID, perm string, isProfileWrite bool) (*OrgInfo, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission(perm) {
		return nil, ErrPermissionDenied
	}
	if isProfileWrite && tc.ActorOrgType == "customer" {
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

func validateIPAllowlist(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if ip := net.ParseIP(trimmed); ip != nil {
			out = append(out, trimmed)
			continue
		}
		if _, _, err := net.ParseCIDR(trimmed); err == nil {
			out = append(out, trimmed)
			continue
		}
		return nil, fmt.Errorf("%w: invalid IP or CIDR %q in allowlist", ErrValidation, raw)
	}
	return out, nil
}

func normalizeCountries(countries []string) ([]string, error) {
	out := make([]string, 0, len(countries))
	seen := make(map[string]bool)
	for _, c := range countries {
		iso := strings.ToUpper(strings.TrimSpace(c))
		if iso == "" {
			continue
		}
		if !reISO2.MatchString(iso) {
			return nil, fmt.Errorf("%w: invalid country ISO2 %q in allowed_countries", ErrValidation, c)
		}
		if !seen[iso] {
			seen[iso] = true
			out = append(out, iso)
		}
	}
	slices.Sort(out)
	return out, nil
}

func enforceResellerProfileCaps(tc *tenant.Context, p *CustomerProfile) error {
	if tc.ActorOrgType != "reseller" {
		return nil
	}
	if p.AccountType == "postpaid" {
		return fmt.Errorf("%w: only platform administrators can grant postpaid accounts", ErrPlatformOnlyField)
	}
	if p.DefaultMaxTPS > MaxResellerTPS {
		return fmt.Errorf("%w: default_max_tps (%d) exceeds reseller ceiling (%d)", ErrResellerCapExceeded, p.DefaultMaxTPS, MaxResellerTPS)
	}
	if p.CreditLimit > MaxResellerCreditLimit {
		return fmt.Errorf("%w: credit_limit (%.2f) exceeds reseller ceiling (%.2f)", ErrResellerCapExceeded, p.CreditLimit, MaxResellerCreditLimit)
	}
	if p.DailyMessageCap > MaxResellerDailyCap {
		return fmt.Errorf("%w: daily_message_cap (%d) exceeds reseller ceiling (%d)", ErrResellerCapExceeded, p.DailyMessageCap, MaxResellerDailyCap)
	}
	if p.MonthlyMessageCap > MaxResellerMonthlyCap {
		return fmt.Errorf("%w: monthly_message_cap (%d) exceeds reseller ceiling (%d)", ErrResellerCapExceeded, p.MonthlyMessageCap, MaxResellerMonthlyCap)
	}
	return nil
}

func validateProfileFields(p *CustomerProfile) error {
	if p.AccountType == "" {
		p.AccountType = "prepaid"
	}
	if !validAccountTypes[p.AccountType] {
		return fmt.Errorf("%w: invalid account_type %q", ErrValidation, p.AccountType)
	}
	if p.BillingCurrency == "" {
		p.BillingCurrency = "USD"
	}
	p.BillingCurrency = strings.ToUpper(strings.TrimSpace(p.BillingCurrency))
	if !reCurrencyCode.MatchString(p.BillingCurrency) {
		return fmt.Errorf("%w: billing_currency must be a 3-letter ISO 4217 code", ErrValidation)
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if p.DefaultLanguage == "" {
		p.DefaultLanguage = "en"
	}
	if p.CreditLimit < 0 {
		return fmt.Errorf("%w: credit_limit cannot be negative", ErrValidation)
	}
	if p.DefaultMaxTPS < 1 || p.DefaultMaxTPS > 100000 {
		return fmt.Errorf("%w: default_max_tps must be between 1 and 100000", ErrValidation)
	}
	if p.DailyMessageCap < 0 || p.MonthlyMessageCap < 0 {
		return fmt.Errorf("%w: message caps cannot be negative", ErrValidation)
	}
	if p.DestinationPolicy == "" {
		p.DestinationPolicy = "allow_all"
	}
	if !validDestinationPolicies[p.DestinationPolicy] {
		return fmt.Errorf("%w: invalid destination_policy %q", ErrValidation, p.DestinationPolicy)
	}
	if p.Status == "" {
		p.Status = "draft"
	}
	if !validProfileStatuses[p.Status] {
		return fmt.Errorf("%w: invalid profile status %q", ErrValidation, p.Status)
	}
	countries, err := normalizeCountries(p.AllowedCountries)
	if err != nil {
		return err
	}
	p.AllowedCountries = countries
	return nil
}

func hasProfileOverride(tc *tenant.Context) bool {
	return tc != nil && tc.IsPlatformOrg() && tc.HasPermission("customer.profile.override")
}

func (s *Service) submitProfileChange(ctx context.Context, tc *tenant.Context, entityID *uuid.UUID, entityVersion *int, op string, before, after any, reason string) (*approval.ChangeRequest, error) {
	var curRaw json.RawMessage
	if before != nil {
		curRaw, _ = json.Marshal(before)
	}
	afterMap := toMap(after)
	if reason != "" {
		afterMap["_reason"] = reason
	}
	propRaw, _ := json.Marshal(afterMap)
	return s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
		EntityType:    "customer_profile",
		EntityID:      entityID,
		Operation:     op,
		ProposedData:  propRaw,
		CurrentData:   curRaw,
		EntityVersion: entityVersion,
		MakerUserID:   tc.ActorUserID,
	})
}

// ============================================================================
// Customer Commercial Profiles
// ============================================================================

func (s *Service) ListProfiles(ctx context.Context, tc *tenant.Context, filter ProfileListFilter) ([]CustomerProfile, int, error) {
	if tc == nil || !tc.HasPermission("customer.profile.view") {
		return nil, 0, ErrPermissionDenied
	}
	switch tc.ActorOrgType {
	case "platform":
		// Platform sees all customer profiles
	case "reseller":
		// Reseller sees ONLY its own direct child customer organizations
		if filter.OrganizationID != nil {
			if _, err := s.authorizeOrgAccess(ctx, tc, *filter.OrganizationID, "customer.profile.view", false); err != nil {
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
	return s.repo.ListProfiles(ctx, filter)
}

func (s *Service) GetProfile(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*CustomerProfile, error) {
	p, err := s.repo.GetProfileByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, p.OrganizationID, "customer.profile.view", false); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) GetProfileByOrg(ctx context.Context, tc *tenant.Context, orgID uuid.UUID) (*CustomerProfile, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, orgID, "customer.profile.view", false); err != nil {
		return nil, err
	}
	return s.repo.GetProfileByOrgID(ctx, orgID)
}

func (s *Service) CreateProfile(ctx context.Context, tc *tenant.Context, req CreateProfileRequest, ip, ua string) (*ProfileWriteResult, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, req.OrganizationID, "customer.profile.create", true); err != nil {
		return nil, err
	}
	if existing, err := s.repo.GetProfileByOrgID(ctx, req.OrganizationID); err == nil && existing != nil {
		return nil, ErrProfileAlreadyExists
	}

	p := &CustomerProfile{
		OrganizationID:    req.OrganizationID,
		AccountType:       req.AccountType,
		BillingCurrency:   req.BillingCurrency,
		Timezone:          req.Timezone,
		DefaultLanguage:   req.DefaultLanguage,
		TechnicalContact:  strings.TrimSpace(req.TechnicalContact),
		BillingContact:    strings.TrimSpace(req.BillingContact),
		CreditLimit:       floatOrDefault(req.CreditLimit, 0),
		DefaultMaxTPS:     intOrDefault(req.DefaultMaxTPS, 10),
		DailyMessageCap:   int64OrDefault(req.DailyMessageCap, 10000),
		MonthlyMessageCap: int64OrDefault(req.MonthlyMessageCap, 300000),
		AllowedCountries:  req.AllowedCountries,
		DestinationPolicy: req.DestinationPolicy,
		Status:            req.Status,
		Notes:             strings.TrimSpace(req.Notes),
		CreatedBy:         &tc.ActorUserID,
		UpdatedBy:         &tc.ActorUserID,
	}

	if err := validateProfileFields(p); err != nil {
		return nil, err
	}
	if err := enforceResellerProfileCaps(tc, p); err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Create customer commercial profile"
	}

	isSensitive := p.Status == "active" || p.Status == "suspended" || p.Status == "closed" || p.CreditLimit > 0 || p.DestinationPolicy != "allow_all"
	if isSensitive && !hasProfileOverride(tc) && s.approvalSvc != nil {
		cr, err := s.submitProfileChange(ctx, tc, nil, nil, "create", nil, p, reason)
		if err != nil {
			return nil, err
		}
		return &ProfileWriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateProfile(ctx, tx, p, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.profile.create",
		Module:      "customer",
		EntityType:  "customer_profile",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetProfileByID(ctx, created.ID)
	if err == nil {
		created = full
	}
	return &ProfileWriteResult{Profile: created}, nil
}

func (s *Service) UpdateProfile(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateProfileRequest, ip, ua string) (*ProfileWriteResult, error) {
	current, err := s.repo.GetProfileByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, "customer.profile.edit", true); err != nil {
		return nil, err
	}

	updated := *current
	sensitiveChange := false

	if req.AccountType != nil && *req.AccountType != current.AccountType {
		if tc.ActorOrgType == "reseller" {
			return nil, ErrPlatformOnlyField
		}
		updated.AccountType = *req.AccountType
	}
	if req.BillingCurrency != nil && !strings.EqualFold(*req.BillingCurrency, current.BillingCurrency) {
		if tc.ActorOrgType == "reseller" {
			return nil, ErrPlatformOnlyField
		}
		updated.BillingCurrency = *req.BillingCurrency
	}
	if req.Timezone != nil {
		updated.Timezone = strings.TrimSpace(*req.Timezone)
	}
	if req.DefaultLanguage != nil {
		updated.DefaultLanguage = strings.TrimSpace(*req.DefaultLanguage)
	}
	if req.TechnicalContact != nil {
		updated.TechnicalContact = strings.TrimSpace(*req.TechnicalContact)
	}
	if req.BillingContact != nil {
		updated.BillingContact = strings.TrimSpace(*req.BillingContact)
	}
	if req.CreditLimit != nil && *req.CreditLimit != current.CreditLimit {
		updated.CreditLimit = *req.CreditLimit
		sensitiveChange = true
	}
	if req.DefaultMaxTPS != nil {
		updated.DefaultMaxTPS = *req.DefaultMaxTPS
	}
	if req.DailyMessageCap != nil {
		updated.DailyMessageCap = *req.DailyMessageCap
	}
	if req.MonthlyMessageCap != nil {
		updated.MonthlyMessageCap = *req.MonthlyMessageCap
	}
	if req.AllowedCountries != nil {
		norm, err := normalizeCountries(*req.AllowedCountries)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(norm, current.AllowedCountries) {
			sensitiveChange = true
		}
		updated.AllowedCountries = norm
	}
	if req.DestinationPolicy != nil && *req.DestinationPolicy != current.DestinationPolicy {
		updated.DestinationPolicy = *req.DestinationPolicy
		sensitiveChange = true
	}
	if req.Status != nil && *req.Status != current.Status {
		if tc.ActorOrgType == "reseller" && (current.Status == "suspended" || current.Status == "closed") && *req.Status == "active" {
			return nil, ErrPlatformOnlyField
		}
		updated.Status = *req.Status
		sensitiveChange = true
	}
	if req.Notes != nil {
		updated.Notes = strings.TrimSpace(*req.Notes)
	}
	if req.Archived != nil {
		updated.Archived = *req.Archived
	}

	if err := validateProfileFields(&updated); err != nil {
		return nil, err
	}
	if err := enforceResellerProfileCaps(tc, &updated); err != nil {
		return nil, err
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Update customer commercial profile"
	}

	if sensitiveChange && !hasProfileOverride(tc) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitProfileChange(ctx, tc, &id, &v, "update", current, &updated, reason)
		if err != nil {
			return nil, err
		}
		return &ProfileWriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateProfile(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.profile.edit",
		Module:      "customer",
		EntityType:  "customer_profile",
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
	full, err := s.repo.GetProfileByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return &ProfileWriteResult{Profile: saved}, nil
}

func (s *Service) ListProfileHistory(ctx context.Context, tc *tenant.Context, profileID uuid.UUID) ([]CustomerProfileHistory, error) {
	p, err := s.repo.GetProfileByID(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, p.OrganizationID, "customer.profile.view", false); err != nil {
		return nil, err
	}
	return s.repo.ListProfileHistory(ctx, profileID)
}

func (s *Service) RollbackProfile(ctx context.Context, tc *tenant.Context, id uuid.UUID, targetVersion int, reason, ip, ua string) (*ProfileWriteResult, error) {
	current, err := s.repo.GetProfileByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, "customer.profile.edit", true); err != nil {
		return nil, err
	}
	hist, err := s.repo.GetProfileHistoryVersion(ctx, id, targetVersion)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(hist.Snapshot)
	if err != nil {
		return nil, err
	}
	var restored CustomerProfile
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.ID = current.ID
	restored.OrganizationID = current.OrganizationID
	restored.UpdatedBy = &tc.ActorUserID

	if err := enforceResellerProfileCaps(tc, &restored); err != nil {
		return nil, err
	}

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rollback customer profile to version %d", targetVersion)
	}

	if !hasProfileOverride(tc) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitProfileChange(ctx, tc, &id, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &ProfileWriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateProfile(ctx, tx, &restored, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.profile.rollback",
		Module:      "customer",
		EntityType:  "customer_profile",
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
	full, err := s.repo.GetProfileByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return &ProfileWriteResult{Profile: saved}, nil
}

func (s *Service) applyProfileChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var p CustomerProfile
	if err := json.Unmarshal(cr.ProposedData, &p); err != nil {
		return err
	}
	actorID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		actorID = *cr.CheckerUserID
	}
	p.UpdatedBy = &actorID

	reason := "Approved customer profile change"
	var m map[string]any
	if err := json.Unmarshal(cr.ProposedData, &m); err == nil {
		if r, ok := m["_reason"].(string); ok && strings.TrimSpace(r) != "" {
			reason = r
		}
	}

	if cr.Operation == "create" {
		p.CreatedBy = &cr.MakerUserID
		_, err := s.repo.CreateProfile(ctx, tx, &p, reason)
		return err
	}
	if cr.EntityID != nil {
		p.ID = *cr.EntityID
	}
	_, err := s.repo.UpdateProfile(ctx, tx, &p, reason)
	return err
}

// ============================================================================
// Customer API Keys
// ============================================================================

func (s *Service) ListAPIKeys(ctx context.Context, tc *tenant.Context, orgID uuid.UUID) ([]CustomerAPIKey, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, orgID, "customer.apikey.view", false); err != nil {
		return nil, err
	}
	return s.repo.ListAPIKeysByOrg(ctx, orgID)
}

func (s *Service) CreateAPIKey(ctx context.Context, tc *tenant.Context, req CreateAPIKeyRequest, ip, ua string) (*IssuedAPIKeyResponse, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, req.OrganizationID, "customer.apikey.create", false); err != nil {
		return nil, err
	}
	label := strings.TrimSpace(req.Label)
	if label == "" || len(label) > 128 {
		return nil, fmt.Errorf("%w: label is required (max 128 chars)", ErrValidation)
	}
	allowlist, err := validateIPAllowlist(req.IPAllowlist)
	if err != nil {
		return nil, err
	}

	gen, err := secrets.GenerateAPIKey()
	if err != nil {
		return nil, err
	}

	k := &CustomerAPIKey{
		OrganizationID: req.OrganizationID,
		Label:          label,
		KeyPrefix:      gen.Prefix,
		KeyHash:        gen.KeyHash,
		Scopes:         req.Scopes,
		IPAllowlist:    allowlist,
		ExpiresAt:      req.ExpiresAt,
		CreatedBy:      &tc.ActorUserID,
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateAPIKey(ctx, tx, k)
	if err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Created API key %q (%s)", created.Label, created.KeyPrefix)
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.apikey.create",
		Module:      "customer",
		EntityType:  "customer_api_key",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &IssuedAPIKeyResponse{
		APIKey:       *created,
		PlaintextKey: gen.PlaintextKey,
		Warning:      OneTimeSecretWarning,
	}, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, tc *tenant.Context, keyID uuid.UUID, reason, ip, ua string) (*CustomerAPIKey, error) {
	existing, err := s.repo.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, existing.OrganizationID, "customer.apikey.revoke", false); err != nil {
		return nil, err
	}
	if existing.Status == "revoked" {
		return nil, ErrAPIKeyAlreadyRevoked
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	revoked, err := s.repo.RevokeAPIKey(ctx, tx, keyID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Revoked API key %q (%s)", revoked.Label, revoked.KeyPrefix)
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.apikey.revoke",
		Module:      "customer",
		EntityType:  "customer_api_key",
		EntityID:    revoked.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(revoked),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return revoked, nil
}

func (s *Service) RotateAPIKey(ctx context.Context, tc *tenant.Context, keyID uuid.UUID, reason, ip, ua string) (*IssuedAPIKeyResponse, error) {
	existing, err := s.repo.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, existing.OrganizationID, "customer.apikey.rotate", false); err != nil {
		return nil, err
	}
	if existing.Status == "revoked" {
		return nil, ErrAPIKeyAlreadyRevoked
	}

	gen, err := secrets.GenerateAPIKey()
	if err != nil {
		return nil, err
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := s.repo.RevokeAPIKey(ctx, tx, keyID); err != nil {
		return nil, err
	}

	newKey := &CustomerAPIKey{
		OrganizationID: existing.OrganizationID,
		Label:          existing.Label,
		KeyPrefix:      gen.Prefix,
		KeyHash:        gen.KeyHash,
		Scopes:         existing.Scopes,
		IPAllowlist:    existing.IPAllowlist,
		ExpiresAt:      existing.ExpiresAt,
		CreatedBy:      &tc.ActorUserID,
	}
	created, err := s.repo.CreateAPIKey(ctx, tx, newKey)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rotated API key %q (%s -> %s)", existing.Label, existing.KeyPrefix, created.KeyPrefix)
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.apikey.rotate",
		Module:      "customer",
		EntityType:  "customer_api_key",
		EntityID:    created.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &IssuedAPIKeyResponse{
		APIKey:       *created,
		PlaintextKey: gen.PlaintextKey,
		Warning:      OneTimeSecretWarning,
	}, nil
}

// ============================================================================
// Customer SMPP Accounts
// ============================================================================

func (s *Service) ListSMPPAccounts(ctx context.Context, tc *tenant.Context, orgID uuid.UUID) ([]CustomerSMPPAccount, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, orgID, "customer.smppaccount.view", false); err != nil {
		return nil, err
	}
	return s.repo.ListSMPPAccountsByOrg(ctx, orgID)
}

func (s *Service) CreateSMPPAccount(ctx context.Context, tc *tenant.Context, req CreateSMPPAccountRequest, ip, ua string) (*IssuedSMPPAccountResponse, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, req.OrganizationID, "customer.smppaccount.create", true); err != nil {
		return nil, err
	}

	sysID := strings.TrimSpace(req.SystemID)
	if !reSystemID.MatchString(sysID) {
		return nil, fmt.Errorf("%w: system_id must be 4..16 alphanumeric or underscore characters", ErrValidation)
	}

	allowIPs, err := validateIPAllowlist(req.AllowedIPs)
	if err != nil {
		return nil, err
	}

	bindTypes := req.AllowedBindTypes
	if len(bindTypes) == 0 {
		bindTypes = []string{"transceiver"}
	}
	for _, bt := range bindTypes {
		if !validBindTypes[bt] {
			return nil, fmt.Errorf("%w: invalid bind type %q", ErrValidation, bt)
		}
	}

	maxBinds := intOrDefault(req.MaxBinds, 2)
	if maxBinds < 1 || maxBinds > 100 {
		return nil, fmt.Errorf("%w: max_binds must be between 1 and 100", ErrValidation)
	}

	maxTPS := intOrDefault(req.MaxTPS, 10)
	if maxTPS < 1 || maxTPS > 100000 {
		return nil, fmt.Errorf("%w: max_tps must be between 1 and 100000", ErrValidation)
	}
	if tc.ActorOrgType == "reseller" && maxTPS > MaxResellerTPS {
		return nil, fmt.Errorf("%w: max_tps (%d) exceeds reseller ceiling (%d)", ErrResellerCapExceeded, maxTPS, MaxResellerTPS)
	}
	if prof, err := s.repo.GetProfileByOrgID(ctx, req.OrganizationID); err == nil && prof != nil {
		if maxTPS > prof.DefaultMaxTPS {
			return nil, fmt.Errorf("%w: SMPP account max_tps (%d) cannot exceed customer profile default_max_tps (%d)", ErrValidation, maxTPS, prof.DefaultMaxTPS)
		}
	}

	status := req.Status
	if status == "" {
		status = "active"
	}
	if !validSMPPStatuses[status] {
		return nil, fmt.Errorf("%w: invalid SMPP account status %q", ErrValidation, status)
	}

	plainPass, err := secrets.GenerateRandomPassword(18)
	if err != nil {
		return nil, err
	}
	hash, err := password.Hash(plainPass)
	if err != nil {
		return nil, err
	}

	acct := &CustomerSMPPAccount{
		OrganizationID:   req.OrganizationID,
		SystemID:         sysID,
		PasswordHash:     hash,
		AllowedIPs:       allowIPs,
		AllowedBindTypes: bindTypes,
		MaxBinds:         maxBinds,
		MaxTPS:           maxTPS,
		Status:           status,
		CreatedBy:        &tc.ActorUserID,
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateSMPPAccount(ctx, tx, acct)
	if err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Created customer SMPP account %s", created.SystemID)
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.smppaccount.create",
		Module:      "customer",
		EntityType:  "customer_smpp_account",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &IssuedSMPPAccountResponse{
		Account:           *created,
		PlaintextPassword: plainPass,
		Warning:           OneTimeSecretWarning,
	}, nil
}

func (s *Service) UpdateSMPPAccount(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateSMPPAccountRequest, ip, ua string) (*CustomerSMPPAccount, error) {
	current, err := s.repo.GetSMPPAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	perm := "customer.smppaccount.edit"
	if req.Status != nil && (*req.Status == "disabled" || *req.Status == "suspended") && !tc.HasPermission("customer.smppaccount.edit") {
		perm = "customer.smppaccount.revoke"
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, perm, true); err != nil {
		return nil, err
	}

	updated := *current
	if req.AllowedIPs != nil {
		ips, err := validateIPAllowlist(*req.AllowedIPs)
		if err != nil {
			return nil, err
		}
		updated.AllowedIPs = ips
	}
	if req.AllowedBindTypes != nil {
		if len(*req.AllowedBindTypes) == 0 {
			return nil, fmt.Errorf("%w: allowed_bind_types cannot be empty", ErrValidation)
		}
		for _, bt := range *req.AllowedBindTypes {
			if !validBindTypes[bt] {
				return nil, fmt.Errorf("%w: invalid bind type %q", ErrValidation, bt)
			}
		}
		updated.AllowedBindTypes = *req.AllowedBindTypes
	}
	if req.MaxBinds != nil {
		if *req.MaxBinds < 1 || *req.MaxBinds > 100 {
			return nil, fmt.Errorf("%w: max_binds must be between 1 and 100", ErrValidation)
		}
		updated.MaxBinds = *req.MaxBinds
	}
	if req.MaxTPS != nil {
		if *req.MaxTPS < 1 || *req.MaxTPS > 100000 {
			return nil, fmt.Errorf("%w: max_tps must be between 1 and 100000", ErrValidation)
		}
		if tc.ActorOrgType == "reseller" && *req.MaxTPS > MaxResellerTPS {
			return nil, fmt.Errorf("%w: max_tps (%d) exceeds reseller ceiling (%d)", ErrResellerCapExceeded, *req.MaxTPS, MaxResellerTPS)
		}
		if prof, err := s.repo.GetProfileByOrgID(ctx, current.OrganizationID); err == nil && prof != nil {
			if *req.MaxTPS > prof.DefaultMaxTPS {
				return nil, fmt.Errorf("%w: SMPP account max_tps (%d) cannot exceed customer profile default_max_tps (%d)", ErrValidation, *req.MaxTPS, prof.DefaultMaxTPS)
			}
		}
		updated.MaxTPS = *req.MaxTPS
	}
	if req.Status != nil {
		if !validSMPPStatuses[*req.Status] {
			return nil, fmt.Errorf("%w: invalid status %q", ErrValidation, *req.Status)
		}
		updated.Status = *req.Status
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateSMPPAccount(ctx, tx, &updated)
	if err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Updated customer SMPP account %s", saved.SystemID)
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.smppaccount.edit",
		Module:      "customer",
		EntityType:  "customer_smpp_account",
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
	return saved, nil
}

func (s *Service) ResetSMPPPassword(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason, ip, ua string) (*IssuedSMPPAccountResponse, error) {
	current, err := s.repo.GetSMPPAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeOrgAccess(ctx, tc, current.OrganizationID, "customer.smppaccount.edit", true); err != nil {
		return nil, err
	}

	plainPass, err := secrets.GenerateRandomPassword(18)
	if err != nil {
		return nil, err
	}
	hash, err := password.Hash(plainPass)
	if err != nil {
		return nil, err
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.ResetSMPPPassword(ctx, tx, id, hash)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(reason) == "" {
		reason = "credential changed"
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "customer.smppaccount.reset_password",
		Module:      "customer",
		EntityType:  "customer_smpp_account",
		EntityID:    saved.ID.String(),
		AfterValue:  secrets.AuditSecretChangeMarker("password", saved.PasswordChangedAt),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &IssuedSMPPAccountResponse{
		Account:           *saved,
		PlaintextPassword: plainPass,
		Warning:           OneTimeSecretWarning,
	}, nil
}

func floatOrDefault(v *float64, def float64) float64 {
	if v != nil {
		return *v
	}
	return def
}

func intOrDefault(v *int, def int) int {
	if v != nil {
		return *v
	}
	return def
}

func int64OrDefault(v *int64, def int64) int64 {
	if v != nil {
		return *v
	}
	return def
}

func toMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
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

func (s *Service) ExportProfiles(ctx context.Context, tc *tenant.Context, filter ProfileListFilter, format string) ([]byte, string, error) {
	if tc == nil || !tc.HasPermission("customer.profile.view") {
		return nil, "", ErrPermissionDenied
	}
	switch tc.ActorOrgType {
	case "platform":
	case "reseller":
		if filter.OrganizationID != nil {
			if _, err := s.authorizeOrgAccess(ctx, tc, *filter.OrganizationID, "customer.profile.view", false); err != nil {
				return nil, "", err
			}
		}
		resellerID := tc.ActorOrgID
		filter.ParentOrgID = &resellerID
	case "customer":
		if filter.OrganizationID != nil && *filter.OrganizationID != tc.ActorOrgID {
			return nil, "", ErrCrossTenantAccess
		}
		ownID := tc.ActorOrgID
		filter.OrganizationID = &ownID
	default:
		return nil, "", ErrPermissionDenied
	}
	filter.Page = 1
	filter.Limit = 1000
	items, _, err := s.repo.ListProfiles(ctx, filter)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "organization_id", "organization_name", "account_type", "billing_currency", "credit_limit", "default_max_tps", "daily_message_cap", "monthly_message_cap", "destination_policy", "status"})
	for _, p := range items {
		_ = w.Write([]string{
			p.ID.String(),
			p.OrganizationID.String(),
			sanitizeCSVCell(p.OrganizationName),
			sanitizeCSVCell(p.AccountType),
			sanitizeCSVCell(p.BillingCurrency),
			fmt.Sprintf("%.2f", p.CreditLimit),
			fmt.Sprintf("%d", p.DefaultMaxTPS),
			fmt.Sprintf("%d", p.DailyMessageCap),
			fmt.Sprintf("%d", p.MonthlyMessageCap),
			sanitizeCSVCell(p.DestinationPolicy),
			sanitizeCSVCell(p.Status),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

func (s *Service) ExportAPIKeys(ctx context.Context, tc *tenant.Context, orgID uuid.UUID, format string) ([]byte, string, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, orgID, "customer.apikey.view", false); err != nil {
		return nil, "", err
	}
	items, err := s.repo.ListAPIKeysByOrg(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "organization_id", "label", "key_prefix", "status", "scopes", "created_at", "last_used_at"})
	for _, k := range items {
		lastUsed := ""
		if k.LastUsedAt != nil {
			lastUsed = k.LastUsedAt.Format(time.RFC3339)
		}
		_ = w.Write([]string{
			k.ID.String(),
			k.OrganizationID.String(),
			sanitizeCSVCell(k.Label),
			sanitizeCSVCell(k.KeyPrefix),
			sanitizeCSVCell(k.Status),
			sanitizeCSVCell(strings.Join(k.Scopes, ";")),
			k.CreatedAt.Format(time.RFC3339),
			lastUsed,
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

func (s *Service) ExportSMPPAccounts(ctx context.Context, tc *tenant.Context, orgID uuid.UUID, format string) ([]byte, string, error) {
	if _, err := s.authorizeOrgAccess(ctx, tc, orgID, "customer.smppaccount.view", false); err != nil {
		return nil, "", err
	}
	items, err := s.repo.ListSMPPAccountsByOrg(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "organization_id", "system_id", "status", "max_binds", "max_tps", "allowed_bind_types", "created_at"})
	for _, a := range items {
		_ = w.Write([]string{
			a.ID.String(),
			a.OrganizationID.String(),
			sanitizeCSVCell(a.SystemID),
			sanitizeCSVCell(a.Status),
			fmt.Sprintf("%d", a.MaxBinds),
			fmt.Sprintf("%d", a.MaxTPS),
			sanitizeCSVCell(strings.Join(a.AllowedBindTypes, ";")),
			a.CreatedAt.Format(time.RFC3339),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}
