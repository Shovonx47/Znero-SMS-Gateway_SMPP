package provider

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type applierFunc func(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error

func (f applierFunc) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	return f(ctx, tx, cr)
}

type stagedSecret struct {
	smppCipher string
	httpCipher string
}

type Service struct {
	repo        Repository
	auditSvc    *audit.Service
	approvalSvc *approval.Service
	keyRing     *secrets.KeyRing
	appEnv      string

	mu            sync.Mutex
	stagedSecrets map[string]stagedSecret
}

func NewService(repo Repository, auditSvc *audit.Service, approvalSvc *approval.Service, keyRing *secrets.KeyRing, appEnv string) *Service {
	svc := &Service{
		repo:          repo,
		auditSvc:      auditSvc,
		approvalSvc:   approvalSvc,
		keyRing:       keyRing,
		appEnv:        appEnv,
		stagedSecrets: make(map[string]stagedSecret),
	}
	if approvalSvc != nil {
		svc.RegisterWithApproval(approvalSvc)
	}
	return svc
}

func (s *Service) RegisterWithApproval(approvalSvc *approval.Service) {
	approvalSvc.RegisterApplier("provider", applierFunc(s.applyProviderChange))
	approvalSvc.RegisterApprovePermission("provider", "provider.provider.approve")

	approvalSvc.RegisterApplier("provider_connection", applierFunc(s.applyConnectionChange))
	approvalSvc.RegisterApprovePermission("provider_connection", "provider.connection.approve")

	approvalSvc.RegisterApplier("provider_coverage", applierFunc(s.applyCoverageChange))
	approvalSvc.RegisterApprovePermission("provider_coverage", "provider.coverage.approve")
}

func requirePlatformPerm(tc *tenant.Context, perm string) error {
	if tc == nil || !tc.IsPlatformOrg() {
		return ErrPlatformOnlyProvider
	}
	if !tc.HasPermission(perm) {
		return ErrPermissionDenied
	}
	return nil
}

func hasOverride(tc *tenant.Context, overridePerm string) bool {
	return tc != nil && tc.IsPlatformOrg() && tc.HasPermission(overridePerm)
}

func isSensitiveProviderStatus(status string) bool {
	return status == "active" || status == "suspended"
}

func isSensitiveConnectionStatus(status string) bool {
	return status == "active" || status == "disabled"
}

func isSensitiveCoverageStatus(status string) bool {
	return status == "active" || status == "suspended"
}

func (s *Service) submitChange(ctx context.Context, tc *tenant.Context, entityType string, entityID *uuid.UUID, entityVersion *int, op string, before, after any, reason string) (*approval.ChangeRequest, error) {
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
		EntityType:    entityType,
		EntityID:      entityID,
		Operation:     op,
		ProposedData:  propRaw,
		CurrentData:   curRaw,
		EntityVersion: entityVersion,
		MakerUserID:   tc.ActorUserID,
	})
}

// ============================================================================
// Providers
// ============================================================================

func (s *Service) ListProviders(ctx context.Context, tc *tenant.Context, filter ProviderListFilter) ([]Provider, int, error) {
	if err := requirePlatformPerm(tc, "provider.provider.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.ListProviders(ctx, filter)
}

func (s *Service) GetProvider(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Provider, error) {
	if err := requirePlatformPerm(tc, "provider.provider.view"); err != nil {
		return nil, err
	}
	return s.repo.GetProviderByID(ctx, id)
}

func (s *Service) CreateProvider(ctx context.Context, tc *tenant.Context, req CreateProviderRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.provider.create"); err != nil {
		return nil, err
	}
	if err := validateCreateProvider(&req); err != nil {
		return nil, err
	}
	if existing, err := s.repo.GetProviderByCode(ctx, req.Code); err == nil && existing != nil {
		return nil, ErrProviderCodeExists
	}

	p := &Provider{
		Name:              req.Name,
		Code:              req.Code,
		ProviderType:      req.ProviderType,
		LegalEntityName:   strings.TrimSpace(req.LegalEntityName),
		ContactTechnical:  strings.TrimSpace(req.ContactTechnical),
		ContactCommercial: strings.TrimSpace(req.ContactCommercial),
		ContactNOC:        strings.TrimSpace(req.ContactNOC),
		CountryID:         req.CountryID,
		Timezone:          req.Timezone,
		CurrencyCode:      req.CurrencyCode,
		Notes:             strings.TrimSpace(req.Notes),
		Status:            req.Status,
		CreatedBy:         &tc.ActorUserID,
		UpdatedBy:         &tc.ActorUserID,
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Initial provider creation"
	}

	if isSensitiveProviderStatus(p.Status) && !hasOverride(tc, "provider.provider.override") && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "provider", nil, nil, "create", nil, p, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateProvider(ctx, tx, p, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.provider.create",
		Module:      "provider",
		EntityType:  "provider",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &WriteResult{Provider: created}, nil
}

func (s *Service) UpdateProvider(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateProviderRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.provider.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetProviderByID(ctx, id)
	if err != nil {
		return nil, err
	}
	updated := *current

	if req.Name != nil {
		v := strings.TrimSpace(*req.Name)
		if v == "" || len(v) > 255 {
			return nil, fmt.Errorf("%w: name must be 1..255 chars", ErrValidation)
		}
		updated.Name = v
	}
	if req.ProviderType != nil {
		if !validProviderTypes[*req.ProviderType] {
			return nil, fmt.Errorf("%w: invalid provider_type", ErrValidation)
		}
		updated.ProviderType = *req.ProviderType
	}
	if req.LegalEntityName != nil {
		updated.LegalEntityName = strings.TrimSpace(*req.LegalEntityName)
	}
	if req.ContactTechnical != nil {
		updated.ContactTechnical = strings.TrimSpace(*req.ContactTechnical)
	}
	if req.ContactCommercial != nil {
		updated.ContactCommercial = strings.TrimSpace(*req.ContactCommercial)
	}
	if req.ContactNOC != nil {
		updated.ContactNOC = strings.TrimSpace(*req.ContactNOC)
	}
	if req.CountryID != nil {
		updated.CountryID = req.CountryID
	}
	if req.Timezone != nil {
		updated.Timezone = strings.TrimSpace(*req.Timezone)
	}
	if req.CurrencyCode != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.CurrencyCode))
		if !reCurrencyCode.MatchString(c) {
			return nil, fmt.Errorf("%w: currency_code must be 3 uppercase letters", ErrValidation)
		}
		updated.CurrencyCode = c
	}
	if req.Notes != nil {
		updated.Notes = strings.TrimSpace(*req.Notes)
	}

	statusChangedToSensitive := false
	if req.Status != nil && *req.Status != current.Status {
		if !validProviderStatuses[*req.Status] {
			return nil, fmt.Errorf("%w: invalid status", ErrValidation)
		}
		if !tc.HasPermission("provider.provider.status") {
			return nil, ErrPermissionDenied
		}
		if isSensitiveProviderStatus(*req.Status) {
			statusChangedToSensitive = true
		}
		updated.Status = *req.Status
	}
	if req.Archived != nil && *req.Archived != current.Archived {
		if !tc.HasPermission("provider.provider.archive") {
			return nil, ErrPermissionDenied
		}
		updated.Archived = *req.Archived
	}

	updated.UpdatedBy = &tc.ActorUserID
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Update provider"
	}

	if statusChangedToSensitive && !hasOverride(tc, "provider.provider.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider", &id, &v, "update", current, &updated, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateProvider(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.provider.edit",
		Module:      "provider",
		EntityType:  "provider",
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
	return &WriteResult{Provider: saved}, nil
}

func (s *Service) ListProviderHistory(ctx context.Context, tc *tenant.Context, providerID uuid.UUID) ([]ProviderHistory, error) {
	if err := requirePlatformPerm(tc, "provider.provider.view"); err != nil {
		return nil, err
	}
	return s.repo.ListProviderHistory(ctx, providerID)
}

func (s *Service) RollbackProvider(ctx context.Context, tc *tenant.Context, id uuid.UUID, targetVersion int, reason, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.provider.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetProviderByID(ctx, id)
	if err != nil {
		return nil, err
	}
	hist, err := s.repo.GetProviderHistoryVersion(ctx, id, targetVersion)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(hist.Snapshot)
	if err != nil {
		return nil, err
	}
	var restored Provider
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.ID = current.ID
	restored.Code = current.Code
	restored.UpdatedBy = &tc.ActorUserID

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rollback to version %d", targetVersion)
	}

	if !hasOverride(tc, "provider.provider.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider", &id, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateProvider(ctx, tx, &restored, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.provider.rollback",
		Module:      "provider",
		EntityType:  "provider",
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
	return &WriteResult{Provider: saved}, nil
}

func (s *Service) ExportProviders(ctx context.Context, tc *tenant.Context, filter ProviderListFilter, format string) ([]byte, string, error) {
	if err := requirePlatformPerm(tc, "provider.provider.view"); err != nil {
		return nil, "", err
	}
	filter.Page = 1
	filter.Limit = 500
	items, _, err := s.repo.ListProviders(ctx, filter)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"code", "name", "legal_entity_name", "provider_type", "currency_code", "timezone", "status"})
	for _, p := range items {
		_ = w.Write([]string{
			sanitizeCSVCell(p.Code),
			sanitizeCSVCell(p.Name),
			sanitizeCSVCell(p.LegalEntityName),
			sanitizeCSVCell(p.ProviderType),
			sanitizeCSVCell(p.CurrencyCode),
			sanitizeCSVCell(p.Timezone),
			sanitizeCSVCell(p.Status),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

// ============================================================================
// Provider Connections
// ============================================================================

func (s *Service) ListConnections(ctx context.Context, tc *tenant.Context, providerID uuid.UUID) ([]ProviderConnection, error) {
	if err := requirePlatformPerm(tc, "provider.connection.view"); err != nil {
		return nil, err
	}
	return s.repo.ListConnectionsByProvider(ctx, providerID)
}

func (s *Service) ExportConnections(ctx context.Context, tc *tenant.Context, providerID uuid.UUID, format string) ([]byte, string, error) {
	if err := requirePlatformPerm(tc, "provider.connection.view"); err != nil {
		return nil, "", err
	}
	items, err := s.repo.ListConnectionsByProvider(ctx, providerID)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "provider_id", "name", "protocol", "status", "max_tps", "smpp_host", "smpp_port", "smpp_system_id", "smpp_system_type", "smpp_bind_type", "smpp_tls"})
	for _, c := range items {
		_ = w.Write([]string{
			c.ID.String(),
			c.ProviderID.String(),
			sanitizeCSVCell(c.Name),
			sanitizeCSVCell(c.Protocol),
			sanitizeCSVCell(c.Status),
			fmt.Sprintf("%d", c.MaxTPS),
			sanitizeCSVCell(c.SMPPHost),
			fmt.Sprintf("%d", c.SMPPPort),
			sanitizeCSVCell(c.SMPPSystemID),
			sanitizeCSVCell(c.SMPPSystemType),
			sanitizeCSVCell(c.SMPPBindType),
			strconv.FormatBool(c.SMPPTLS),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

func (s *Service) GetConnection(ctx context.Context, tc *tenant.Context, connID uuid.UUID) (*ProviderConnection, error) {
	if err := requirePlatformPerm(tc, "provider.connection.view"); err != nil {
		return nil, err
	}
	return s.repo.GetConnectionByID(ctx, connID)
}

func (s *Service) CreateConnection(ctx context.Context, tc *tenant.Context, providerID uuid.UUID, req CreateConnectionRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.connection.create"); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetProviderByID(ctx, providerID); err != nil {
		return nil, err
	}

	c := &ProviderConnection{
		ProviderID:           providerID,
		Name:                 req.Name,
		Protocol:             req.Protocol,
		RoleNotes:            strings.TrimSpace(req.RoleNotes),
		Status:               req.Status,
		MaxTPS:               req.MaxTPS,
		SMPPHost:             strings.TrimSpace(req.SMPPHost),
		SMPPPort:             intOrDefault(req.SMPPPort, 2775),
		SMPPSystemID:         strings.TrimSpace(req.SMPPSystemID),
		SMPPSystemType:       strings.TrimSpace(req.SMPPSystemType),
		SMPPBindType:         req.SMPPBindType,
		SMPPTLS:              boolOrDefault(req.SMPPTLS, true),
		SMPPWindowSize:       intOrDefault(req.SMPPWindowSize, 10),
		SMPPEnquireLinkSec:   intOrDefault(req.SMPPEnquireLinkSec, 30),
		SMPPSourceTON:        intOrDefault(req.SMPPSourceTON, 5),
		SMPPSourceNPI:        intOrDefault(req.SMPPSourceNPI, 0),
		SMPPDestTON:          intOrDefault(req.SMPPDestTON, 1),
		SMPPDestNPI:          intOrDefault(req.SMPPDestNPI, 1),
		SMPPCharset:          req.SMPPCharset,
		IPAllowlistNotes:     strings.TrimSpace(req.IPAllowlistNotes),
		HTTPBaseURL:          strings.TrimSpace(req.HTTPBaseURL),
		HTTPAuthType:         req.HTTPAuthType,
		HTTPFormatNotes:      strings.TrimSpace(req.HTTPFormatNotes),
		HTTPDLRCallbackNotes: strings.TrimSpace(req.HTTPDLRCallbackNotes),
		CreatedBy:            &tc.ActorUserID,
		UpdatedBy:            &tc.ActorUserID,
	}

	if err := validateConnectionFields(c, s.appEnv); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var cipherSMPP, cipherHTTP string
	secretChanged := false
	if req.SMPPPassword != nil && strings.TrimSpace(*req.SMPPPassword) != "" {
		if !tc.HasPermission("provider.connection.secret.update") {
			return nil, ErrPermissionDenied
		}
		enc, err := s.keyRing.Encrypt(*req.SMPPPassword)
		if err != nil {
			return nil, err
		}
		cipherSMPP = enc
		c.SMPPPasswordEncrypted = enc
		c.SMPPPasswordChangedAt = &now
		c.SMPPPassword = secrets.NewSecretStatus(enc, &now)
		secretChanged = true
	}
	if req.HTTPCredentials != nil && strings.TrimSpace(*req.HTTPCredentials) != "" {
		if !tc.HasPermission("provider.connection.secret.update") {
			return nil, ErrPermissionDenied
		}
		enc, err := s.keyRing.Encrypt(*req.HTTPCredentials)
		if err != nil {
			return nil, err
		}
		cipherHTTP = enc
		c.HTTPCredentialsEncrypted = enc
		c.HTTPCredentialsChangedAt = &now
		c.HTTPCredentials = secrets.NewSecretStatus(enc, &now)
		secretChanged = true
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Create provider connection"
	}

	if isSensitiveConnectionStatus(c.Status) && !hasOverride(tc, "provider.connection.override") && s.approvalSvc != nil {
		after := toMap(c)
		if secretChanged {
			ref := uuid.NewString()
			s.mu.Lock()
			s.stagedSecrets[ref] = stagedSecret{smppCipher: cipherSMPP, httpCipher: cipherHTTP}
			s.mu.Unlock()
			after["secret_ref"] = ref
			after["credential"] = "credential changed"
		}
		cr, err := s.submitChange(ctx, tc, "provider_connection", nil, nil, "create", nil, after, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateConnection(ctx, tx, c, reason)
	if err != nil {
		return nil, err
	}

	auditAfter := toMap(created)
	if secretChanged {
		auditAfter["credential"] = "credential changed"
	}
	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.connection.create",
		Module:      "provider",
		EntityType:  "provider_connection",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(auditAfter),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &WriteResult{Connection: created}, nil
}

func (s *Service) UpdateConnection(ctx context.Context, tc *tenant.Context, connID uuid.UUID, req UpdateConnectionRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.connection.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetConnectionByID(ctx, connID)
	if err != nil {
		return nil, err
	}
	updated := *current

	if req.Name != nil {
		updated.Name = *req.Name
	}
	if req.Protocol != nil {
		updated.Protocol = *req.Protocol
	}
	if req.RoleNotes != nil {
		updated.RoleNotes = strings.TrimSpace(*req.RoleNotes)
	}
	if req.MaxTPS != nil {
		updated.MaxTPS = *req.MaxTPS
	}
	if req.SMPPHost != nil {
		updated.SMPPHost = strings.TrimSpace(*req.SMPPHost)
	}
	if req.SMPPPort != nil {
		updated.SMPPPort = *req.SMPPPort
	}
	if req.SMPPSystemID != nil {
		updated.SMPPSystemID = strings.TrimSpace(*req.SMPPSystemID)
	}
	if req.SMPPSystemType != nil {
		updated.SMPPSystemType = strings.TrimSpace(*req.SMPPSystemType)
	}
	if req.SMPPBindType != nil {
		updated.SMPPBindType = *req.SMPPBindType
	}
	if req.SMPPTLS != nil {
		updated.SMPPTLS = *req.SMPPTLS
	}
	if req.SMPPWindowSize != nil {
		updated.SMPPWindowSize = *req.SMPPWindowSize
	}
	if req.SMPPEnquireLinkSec != nil {
		updated.SMPPEnquireLinkSec = *req.SMPPEnquireLinkSec
	}
	if req.SMPPSourceTON != nil {
		updated.SMPPSourceTON = *req.SMPPSourceTON
	}
	if req.SMPPSourceNPI != nil {
		updated.SMPPSourceNPI = *req.SMPPSourceNPI
	}
	if req.SMPPDestTON != nil {
		updated.SMPPDestTON = *req.SMPPDestTON
	}
	if req.SMPPDestNPI != nil {
		updated.SMPPDestNPI = *req.SMPPDestNPI
	}
	if req.SMPPCharset != nil {
		updated.SMPPCharset = *req.SMPPCharset
	}
	if req.IPAllowlistNotes != nil {
		updated.IPAllowlistNotes = strings.TrimSpace(*req.IPAllowlistNotes)
	}
	if req.HTTPBaseURL != nil {
		updated.HTTPBaseURL = strings.TrimSpace(*req.HTTPBaseURL)
	}
	if req.HTTPAuthType != nil {
		updated.HTTPAuthType = *req.HTTPAuthType
	}
	if req.HTTPFormatNotes != nil {
		updated.HTTPFormatNotes = strings.TrimSpace(*req.HTTPFormatNotes)
	}
	if req.HTTPDLRCallbackNotes != nil {
		updated.HTTPDLRCallbackNotes = strings.TrimSpace(*req.HTTPDLRCallbackNotes)
	}

	statusChangedToSensitive := false
	if req.Status != nil && *req.Status != current.Status {
		if !tc.HasPermission("provider.connection.status") {
			return nil, ErrPermissionDenied
		}
		if isSensitiveConnectionStatus(*req.Status) {
			statusChangedToSensitive = true
		}
		updated.Status = *req.Status
	}
	if req.Archived != nil && *req.Archived != current.Archived {
		if !tc.HasPermission("provider.connection.archive") {
			return nil, ErrPermissionDenied
		}
		updated.Archived = *req.Archived
	}

	if err := validateConnectionFields(&updated, s.appEnv); err != nil {
		return nil, err
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Update provider connection"
	}

	if statusChangedToSensitive && !hasOverride(tc, "provider.connection.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_connection", &connID, &v, "update", current, &updated, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateConnection(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.connection.edit",
		Module:      "provider",
		EntityType:  "provider_connection",
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
	return &WriteResult{Connection: saved}, nil
}

// UpdateConnectionSecret replaces a connection's SMPP password or HTTP credentials.
// Requires provider.connection.secret.update. Never logs or returns plaintext or ciphertext secrets.
func (s *Service) UpdateConnectionSecret(ctx context.Context, tc *tenant.Context, connID uuid.UUID, req UpdateConnectionSecretRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.connection.secret.update"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetConnectionByID(ctx, connID)
	if err != nil {
		return nil, err
	}

	hasSMPP := req.SMPPPassword != nil && strings.TrimSpace(*req.SMPPPassword) != ""
	hasHTTP := req.HTTPCredentials != nil && strings.TrimSpace(*req.HTTPCredentials) != ""
	if !hasSMPP && !hasHTTP {
		return nil, fmt.Errorf("%w: smpp_password or http_credentials must be non-empty", ErrValidation)
	}

	updated := *current
	now := time.Now().UTC()

	var cipherSMPP, cipherHTTP string
	if hasSMPP {
		enc, err := s.keyRing.Encrypt(*req.SMPPPassword)
		if err != nil {
			return nil, err
		}
		cipherSMPP = enc
		updated.SMPPPasswordEncrypted = enc
		updated.SMPPPasswordChangedAt = &now
		updated.SMPPPassword = secrets.NewSecretStatus(enc, &now)
	}
	if hasHTTP {
		enc, err := s.keyRing.Encrypt(*req.HTTPCredentials)
		if err != nil {
			return nil, err
		}
		cipherHTTP = enc
		updated.HTTPCredentialsEncrypted = enc
		updated.HTTPCredentialsChangedAt = &now
		updated.HTTPCredentials = secrets.NewSecretStatus(enc, &now)
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "credential changed"
	}

	if !hasOverride(tc, "provider.connection.override") && s.approvalSvc != nil {
		ref := uuid.NewString()
		s.mu.Lock()
		s.stagedSecrets[ref] = stagedSecret{smppCipher: cipherSMPP, httpCipher: cipherHTTP}
		s.mu.Unlock()

		after := toMap(&updated)
		after["secret_ref"] = ref
		after["credential"] = "credential changed"
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_connection", &connID, &v, "secret_update", current, after, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateConnection(ctx, tx, &updated, "credential changed")
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.connection.secret.update",
		Module:      "provider",
		EntityType:  "provider_connection",
		EntityID:    saved.ID.String(),
		AfterValue:  secrets.AuditSecretChangeMarker("credential", now),
		Reason:      "credential changed",
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &WriteResult{Connection: saved}, nil
}

func (s *Service) ChangeConnectionStatus(ctx context.Context, tc *tenant.Context, connID uuid.UUID, req UpdateConnectionStatusRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.connection.status"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetConnectionByID(ctx, connID)
	if err != nil {
		return nil, err
	}
	if !validConnectionStatuses[req.Status] {
		return nil, fmt.Errorf("%w: invalid connection status %q", ErrValidation, req.Status)
	}
	updated := *current
	updated.Status = req.Status
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Change connection status to %s", req.Status)
	}

	if isSensitiveConnectionStatus(req.Status) && !hasOverride(tc, "provider.connection.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_connection", &connID, &v, "status_change", current, &updated, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateConnection(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.connection.status",
		Module:      "provider",
		EntityType:  "provider_connection",
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
	return &WriteResult{Connection: saved}, nil
}

func (s *Service) ListConnectionHistory(ctx context.Context, tc *tenant.Context, connID uuid.UUID) ([]ProviderConnectionHistory, error) {
	if err := requirePlatformPerm(tc, "provider.connection.view"); err != nil {
		return nil, err
	}
	return s.repo.ListConnectionHistory(ctx, connID)
}

func (s *Service) RollbackConnection(ctx context.Context, tc *tenant.Context, connID uuid.UUID, targetVersion int, reason, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.connection.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetConnectionByID(ctx, connID)
	if err != nil {
		return nil, err
	}
	hist, err := s.repo.GetConnectionHistoryVersion(ctx, connID, targetVersion)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(hist.Snapshot)
	if err != nil {
		return nil, err
	}
	var restored ProviderConnection
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.ID = current.ID
	restored.ProviderID = current.ProviderID
	restored.SMPPPasswordEncrypted = current.SMPPPasswordEncrypted
	restored.SMPPPasswordChangedAt = current.SMPPPasswordChangedAt
	restored.HTTPCredentialsEncrypted = current.HTTPCredentialsEncrypted
	restored.HTTPCredentialsChangedAt = current.HTTPCredentialsChangedAt
	restored.UpdatedBy = &tc.ActorUserID

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rollback connection to version %d", targetVersion)
	}

	if !hasOverride(tc, "provider.connection.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_connection", &connID, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateConnection(ctx, tx, &restored, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.connection.rollback",
		Module:      "provider",
		EntityType:  "provider_connection",
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
	return &WriteResult{Connection: saved}, nil
}

// ============================================================================
// Provider Coverage
// ============================================================================

func (s *Service) ListCoverage(ctx context.Context, tc *tenant.Context, filter CoverageListFilter) ([]ProviderCoverage, int, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.ListCoverage(ctx, filter)
}

func (s *Service) GetCoverage(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*ProviderCoverage, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.view"); err != nil {
		return nil, err
	}
	return s.repo.GetCoverageByID(ctx, id)
}

func (s *Service) CreateCoverage(ctx context.Context, tc *tenant.Context, req CreateCoverageRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.create"); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetProviderByID(ctx, req.ProviderID); err != nil {
		return nil, err
	}
	if req.OperatorID != nil {
		if err := s.repo.ValidateOperatorCountry(ctx, *req.OperatorID, req.CountryID); err != nil {
			return nil, err
		}
	}

	cov := &ProviderCoverage{
		ProviderID:    req.ProviderID,
		CountryID:     req.CountryID,
		OperatorID:    req.OperatorID,
		Status:        req.Status,
		Notes:         strings.TrimSpace(req.Notes),
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
		DataSource:    strings.TrimSpace(req.DataSource),
		CreatedBy:     &tc.ActorUserID,
		UpdatedBy:     &tc.ActorUserID,
	}

	if err := validateCoverageFields(cov); err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Create provider coverage"
	}

	if isSensitiveCoverageStatus(cov.Status) && !hasOverride(tc, "provider.coverage.override") && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "provider_coverage", nil, nil, "create", nil, cov, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateCoverage(ctx, tx, cov, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.coverage.create",
		Module:      "provider",
		EntityType:  "provider_coverage",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   ua,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	full, err := s.repo.GetCoverageByID(ctx, created.ID)
	if err == nil {
		created = full
	}
	return &WriteResult{Coverage: created}, nil
}

func (s *Service) UpdateCoverage(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateCoverageRequest, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetCoverageByID(ctx, id)
	if err != nil {
		return nil, err
	}
	updated := *current

	if req.Notes != nil {
		updated.Notes = strings.TrimSpace(*req.Notes)
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

	statusChangedToSensitive := false
	if req.Status != nil && *req.Status != current.Status {
		if !tc.HasPermission("provider.coverage.status") {
			return nil, ErrPermissionDenied
		}
		if isSensitiveCoverageStatus(*req.Status) {
			statusChangedToSensitive = true
		}
		updated.Status = *req.Status
	}
	if req.Archived != nil && *req.Archived != current.Archived {
		if !tc.HasPermission("provider.coverage.archive") {
			return nil, ErrPermissionDenied
		}
		updated.Archived = *req.Archived
	}

	if err := validateCoverageFields(&updated); err != nil {
		return nil, err
	}
	updated.UpdatedBy = &tc.ActorUserID

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Update provider coverage"
	}

	if statusChangedToSensitive && !hasOverride(tc, "provider.coverage.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_coverage", &id, &v, "update", current, &updated, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateCoverage(ctx, tx, &updated, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.coverage.edit",
		Module:      "provider",
		EntityType:  "provider_coverage",
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
	full, err := s.repo.GetCoverageByID(ctx, saved.ID)
	if err == nil {
		saved = full
	}
	return &WriteResult{Coverage: saved}, nil
}

func (s *Service) ListCoverageHistory(ctx context.Context, tc *tenant.Context, covID uuid.UUID) ([]ProviderCoverageHistory, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.view"); err != nil {
		return nil, err
	}
	return s.repo.ListCoverageHistory(ctx, covID)
}

func (s *Service) RollbackCoverage(ctx context.Context, tc *tenant.Context, covID uuid.UUID, targetVersion int, reason, ip, ua string) (*WriteResult, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.edit"); err != nil {
		return nil, err
	}
	current, err := s.repo.GetCoverageByID(ctx, covID)
	if err != nil {
		return nil, err
	}
	hist, err := s.repo.GetCoverageHistoryVersion(ctx, covID, targetVersion)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(hist.Snapshot)
	if err != nil {
		return nil, err
	}
	var restored ProviderCoverage
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.ID = current.ID
	restored.ProviderID = current.ProviderID
	restored.CountryID = current.CountryID
	restored.OperatorID = current.OperatorID
	restored.UpdatedBy = &tc.ActorUserID

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Rollback coverage to version %d", targetVersion)
	}

	if !hasOverride(tc, "provider.coverage.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "provider_coverage", &covID, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	saved, err := s.repo.UpdateCoverage(ctx, tx, &restored, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "provider.coverage.rollback",
		Module:      "provider",
		EntityType:  "provider_coverage",
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
	return &WriteResult{Coverage: saved}, nil
}

func (s *Service) ExportCoverage(ctx context.Context, tc *tenant.Context, filter CoverageListFilter, format string) ([]byte, string, error) {
	if err := requirePlatformPerm(tc, "provider.coverage.view"); err != nil {
		return nil, "", err
	}
	filter.Page = 1
	filter.Limit = 500
	items, _, err := s.repo.ListCoverage(ctx, filter)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(format, "json") {
		b, err := json.MarshalIndent(items, "", "  ")
		return b, "application/json", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"provider_code", "country_iso2", "operator_name", "operator_mcc", "operator_mnc", "status", "data_source", "notes"})
	for _, cov := range items {
		opName, mcc, mnc := "", "", ""
		if cov.OperatorName != nil {
			opName = *cov.OperatorName
		}
		if cov.OperatorMCC != nil {
			mcc = *cov.OperatorMCC
		}
		if cov.OperatorMNC != nil {
			mnc = *cov.OperatorMNC
		}
		_ = w.Write([]string{
			sanitizeCSVCell(cov.ProviderCode),
			sanitizeCSVCell(cov.CountryISO2),
			sanitizeCSVCell(opName),
			sanitizeCSVCell(mcc),
			sanitizeCSVCell(mnc),
			sanitizeCSVCell(cov.Status),
			sanitizeCSVCell(cov.DataSource),
			sanitizeCSVCell(cov.Notes),
		})
	}
	w.Flush()
	return buf.Bytes(), "text/csv", w.Error()
}

// ============================================================================
// Approval Appliers (called when a checker approves a pending change)
// ============================================================================

func extractReason(raw json.RawMessage, fallback string) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if r, ok := m["_reason"].(string); ok && strings.TrimSpace(r) != "" {
			return r
		}
	}
	return fallback
}

func (s *Service) applyProviderChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var p Provider
	if err := json.Unmarshal(cr.ProposedData, &p); err != nil {
		return err
	}
	actorID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		actorID = *cr.CheckerUserID
	}
	p.UpdatedBy = &actorID
	reason := extractReason(cr.ProposedData, "Approved provider change")

	if cr.Operation == "create" {
		p.CreatedBy = &cr.MakerUserID
		_, err := s.repo.CreateProvider(ctx, tx, &p, reason)
		return err
	}
	if cr.EntityID != nil {
		p.ID = *cr.EntityID
	}
	_, err := s.repo.UpdateProvider(ctx, tx, &p, reason)
	return err
}

func (s *Service) applyConnectionChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var c ProviderConnection
	if err := json.Unmarshal(cr.ProposedData, &c); err != nil {
		return err
	}
	actorID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		actorID = *cr.CheckerUserID
	}
	c.UpdatedBy = &actorID
	reason := extractReason(cr.ProposedData, "Approved connection change")

	now := time.Now().UTC()
	if cr.EntityID != nil && cr.Operation != "create" {
		c.ID = *cr.EntityID
		if current, err := s.repo.GetConnectionByID(ctx, *cr.EntityID); err == nil {
			c.SMPPPasswordEncrypted = current.SMPPPasswordEncrypted
			c.SMPPPasswordChangedAt = current.SMPPPasswordChangedAt
			c.HTTPCredentialsEncrypted = current.HTTPCredentialsEncrypted
			c.HTTPCredentialsChangedAt = current.HTTPCredentialsChangedAt
		}
	}

	var propMap map[string]any
	_ = json.Unmarshal(cr.ProposedData, &propMap)
	if ref, ok := propMap["secret_ref"].(string); ok && ref != "" {
		s.mu.Lock()
		sec, exists := s.stagedSecrets[ref]
		delete(s.stagedSecrets, ref)
		s.mu.Unlock()
		if exists {
			if sec.smppCipher != "" {
				c.SMPPPasswordEncrypted = sec.smppCipher
				c.SMPPPasswordChangedAt = &now
			}
			if sec.httpCipher != "" {
				c.HTTPCredentialsEncrypted = sec.httpCipher
				c.HTTPCredentialsChangedAt = &now
			}
		}
	}

	if cr.Operation == "create" {
		c.CreatedBy = &cr.MakerUserID
		_, err := s.repo.CreateConnection(ctx, tx, &c, reason)
		return err
	}
	_, err := s.repo.UpdateConnection(ctx, tx, &c, reason)
	return err
}

func (s *Service) applyCoverageChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var cov ProviderCoverage
	if err := json.Unmarshal(cr.ProposedData, &cov); err != nil {
		return err
	}
	actorID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		actorID = *cr.CheckerUserID
	}
	cov.UpdatedBy = &actorID
	reason := extractReason(cr.ProposedData, "Approved coverage change")

	if cr.Operation == "create" {
		cov.CreatedBy = &cr.MakerUserID
		_, err := s.repo.CreateCoverage(ctx, tx, &cov, reason)
		return err
	}
	if cr.EntityID != nil {
		cov.ID = *cr.EntityID
	}
	_, err := s.repo.UpdateCoverage(ctx, tx, &cov, reason)
	return err
}

func boolOrDefault(v *bool, def bool) bool {
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

func toMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}
