package number

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

const MaxBulkNumbers = 1000

var (
	ErrTooManyBulkNumbers     = fmt.Errorf("bulk lookup limit exceeded: maximum %d numbers per request", MaxBulkNumbers)
	ErrPlatformOnlyRuleManage = errors.New("number transformation rules can only be created or modified by platform administrators")
)

type Service struct {
	engine      *Engine
	trie        *PrefixTrie
	ruleRepo    Repository
	prefixRepo  prefix.Repository
	approvalSvc *approval.Service
	auditSvc    *audit.Service
	pool        *pgxpool.Pool
	logger      *slog.Logger
}

func NewService(
	ruleRepo Repository,
	prefixRepo prefix.Repository,
	prefixSvc *prefix.Service,
	approvalSvc *approval.Service,
	auditSvc *audit.Service,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) *Service {
	trie := NewPrefixTrie()
	engine := NewEngine(trie)

	s := &Service{
		engine:      engine,
		trie:        trie,
		ruleRepo:    ruleRepo,
		prefixRepo:  prefixRepo,
		approvalSvc: approvalSvc,
		auditSvc:    auditSvc,
		pool:        pool,
		logger:      logger,
	}

	// Register change applier for approval engine
	approvalSvc.RegisterApplier("number_rule", s)
	approvalSvc.RegisterApprovePermission("number_rule", "number.rule.approve")

	// Initial load of prefixes into in-memory trie
	s.ReloadTrie(context.Background())

	// Subscribe to prefix changes for atomic in-memory trie reload
	if prefixSvc != nil {
		prefixSvc.OnChange(func() {
			s.ReloadTrie(context.Background())
		})
	}

	return s
}

func (s *Service) ReloadTrie(ctx context.Context) {
	prefixes, err := s.prefixRepo.ListAllActive(ctx)
	if err != nil {
		s.logger.Error("failed to load active prefixes into trie", slog.String("error", err.Error()))
		return
	}
	s.trie.Reload(prefixes)
	s.logger.Info("reloaded number prefix trie", slog.Int("prefix_count", len(prefixes)))
}

func (s *Service) Lookup(ctx context.Context, req LookupRequest) NumberLookupResult {
	original := req.Number
	direction := req.Direction
	if direction == "" {
		direction = "outbound"
	}

	// 1. Fetch active transformation rules
	rules, err := s.ruleRepo.ListAllActiveRules(ctx)
	if err != nil {
		s.logger.Warn("could not fetch transform rules for lookup", slog.String("error", err.Error()))
	}

	// 2. Sort and filter rules
	ordered := OrderRules(rules)

	currentNum := original
	var appliedRules []AppliedRule

	// Initial best-effort lookup to get preliminary country/operator for scope filtering
	preliminary := s.engine.Lookup(currentNum, req.DefaultCountryISO)
	var countryID *uuid.UUID
	var opID *uuid.UUID
	if preliminary.Country != nil {
		countryID = &preliminary.Country.ID
	}
	if preliminary.Operator != nil && preliminary.Operator.ID != nil {
		opID = preliminary.Operator.ID
	}

	activeRules := FilterActiveRules(ordered, direction, countryID, opID, time.Now())

	for _, rule := range activeRules {
		transformed, applied := ApplyRule(rule, currentNum)
		if applied && transformed != currentNum {
			appliedRules = append(appliedRules, AppliedRule{
				RuleID:    rule.ID,
				RuleName:  rule.Name,
				Operation: rule.Operation,
				Before:    currentNum,
				After:     transformed,
			})
			currentNum = transformed
		}
	}

	// 3. Perform final carrier & country resolution with Engine
	result := s.engine.Lookup(currentNum, req.DefaultCountryISO)
	// Preserve the exact original input number!
	result.OriginalNumber = original
	result.AppliedRules = appliedRules

	return result
}

func (s *Service) BulkLookup(ctx context.Context, req BulkLookupRequest) (BulkLookupResult, error) {
	if len(req.Numbers) > MaxBulkNumbers {
		return BulkLookupResult{}, ErrTooManyBulkNumbers
	}

	start := time.Now()
	results := make([]NumberLookupResult, 0, len(req.Numbers))
	validCount := 0
	invalidCount := 0

	for _, num := range req.Numbers {
		if strings.TrimSpace(num) == "" {
			continue
		}
		res := s.Lookup(ctx, LookupRequest{
			Number:            num,
			Direction:         req.Direction,
			DefaultCountryISO: req.DefaultCountryISO,
		})
		if res.IsValid {
			validCount++
		} else {
			invalidCount++
		}
		results = append(results, res)
	}

	return BulkLookupResult{
		Results:   results,
		Total:     len(results),
		Valid:     validCount,
		Invalid:   invalidCount,
		ProcessMs: time.Since(start).Milliseconds(),
	}, nil
}

func (s *Service) ListRules(ctx context.Context, f RuleFilter) (RuleListResult, error) {
	items, total, err := s.ruleRepo.ListRules(ctx, f)
	if err != nil {
		return RuleListResult{}, err
	}
	return RuleListResult{
		Items:      items,
		TotalCount: total,
		Page:       f.Page,
		PageSize:   f.PageSize,
	}, nil
}

func (s *Service) GetRule(ctx context.Context, id uuid.UUID) (*TransformRule, error) {
	return s.ruleRepo.GetRuleByID(ctx, id)
}

func (s *Service) CreateRule(ctx context.Context, tc *tenant.Context, req CreateTransformRuleRequest) (any, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyRuleManage
	}

	prio := req.Priority
	if prio <= 0 {
		prio = 100
	}
	dir := req.Direction
	if dir == "" {
		dir = "both"
	}
	scope := req.Scope
	if scope == "" {
		scope = "global"
	}

	rule := TransformRule{
		Name:         strings.TrimSpace(req.Name),
		Description:  strings.TrimSpace(req.Description),
		Operation:    req.Operation,
		MatchPattern: strings.TrimSpace(req.MatchPattern),
		Params:       req.Params,
		Scope:        scope,
		CountryID:    req.CountryID,
		OperatorID:   req.OperatorID,
		Direction:    dir,
		Priority:     prio,
		ActiveFrom:   req.ActiveFrom,
		ActiveTo:     req.ActiveTo,
		Status:       "active",
	}

	if err := ValidateRule(rule); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Created transformation rule %q", rule.Name)
	}

	created, err := s.ruleRepo.CreateRule(ctx, tx, rule, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.rule.create",
		Module:      "number",
		EntityType:  "number_rule",
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

func (s *Service) UpdateRule(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateTransformRuleRequest) (*TransformRule, error) {
	if !tc.IsPlatformOrg() {
		return nil, ErrPlatformOnlyRuleManage
	}

	existing, err := s.ruleRepo.GetRuleByID(ctx, id)
	if err != nil {
		return nil, err
	}

	prio := req.Priority
	if prio <= 0 {
		prio = existing.Priority
	}
	dir := req.Direction
	if dir == "" {
		dir = existing.Direction
	}
	scope := req.Scope
	if scope == "" {
		scope = existing.Scope
	}
	status := req.Status
	if status == "" {
		status = existing.Status
	}

	rule := TransformRule{
		ID:           id,
		Name:         strings.TrimSpace(req.Name),
		Description:  strings.TrimSpace(req.Description),
		Operation:    req.Operation,
		MatchPattern: strings.TrimSpace(req.MatchPattern),
		Params:       req.Params,
		Scope:        scope,
		CountryID:    req.CountryID,
		OperatorID:   req.OperatorID,
		Direction:    dir,
		Priority:     prio,
		ActiveFrom:   req.ActiveFrom,
		ActiveTo:     req.ActiveTo,
		Status:       status,
	}

	if err := ValidateRule(rule); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Updated transformation rule %q", rule.Name)
	}

	updated, err := s.ruleRepo.UpdateRule(ctx, tx, rule, &tc.ActorUserID, reason)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.rule.edit",
		Module:      "number",
		EntityType:  "number_rule",
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

func (s *Service) DeleteRule(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) error {
	if !tc.IsPlatformOrg() {
		return ErrPlatformOnlyRuleManage
	}

	existing, err := s.ruleRepo.GetRuleByID(ctx, id)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if reason == "" {
		reason = fmt.Sprintf("Deactivated transformation rule %q", existing.Name)
	}

	if err := s.ruleRepo.DeleteRule(ctx, tx, id, &tc.ActorUserID, reason); err != nil {
		return err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "number.rule.delete",
		Module:      "number",
		EntityType:  "number_rule",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		Reason:      reason,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *Service) PreviewRule(req RulePreviewRequest) RulePreviewResult {
	transformed, applied := ApplyRule(req.Rule, req.Number)
	return RulePreviewResult{
		Original:    req.Number,
		Transformed: transformed,
		Applied:     applied,
	}
}

func (s *Service) TestChain(ctx context.Context, req ChainTestRequest) (ChainTestResult, error) {
	rules, err := s.ruleRepo.ListAllActiveRules(ctx)
	if err != nil {
		return ChainTestResult{}, err
	}

	ordered := OrderRules(rules)
	activeRules := FilterActiveRules(ordered, req.Direction, req.CountryID, req.OperatorID, time.Now())

	currentNum := req.Number
	var steps []ChainStep

	for _, rule := range activeRules {
		transformed, applied := ApplyRule(rule, currentNum)
		if applied && transformed != currentNum {
			steps = append(steps, ChainStep{
				RuleID:    rule.ID,
				RuleName:  rule.Name,
				Operation: rule.Operation,
				Before:    currentNum,
				After:     transformed,
			})
			currentNum = transformed
		}
	}

	return ChainTestResult{
		Original: req.Number,
		Final:    currentNum,
		Steps:    steps,
	}, nil
}

func (s *Service) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var rule TransformRule
		if err := json.Unmarshal(cr.ProposedData, &rule); err != nil {
			return fmt.Errorf("unmarshal create rule: %w", err)
		}
		reason := fmt.Sprintf("Approved rule change request %s", cr.ID)
		_, err := s.ruleRepo.CreateRule(ctx, tx, rule, cr.CheckerUserID, reason)
		return err
	case "update":
		if cr.EntityID == nil {
			return errors.New("missing entity_id for rule update")
		}
		var rule TransformRule
		if err := json.Unmarshal(cr.ProposedData, &rule); err != nil {
			return fmt.Errorf("unmarshal update rule: %w", err)
		}
		rule.ID = *cr.EntityID
		reason := fmt.Sprintf("Approved rule update request %s", cr.ID)
		_, err := s.ruleRepo.UpdateRule(ctx, tx, rule, cr.CheckerUserID, reason)
		return err
	default:
		return fmt.Errorf("unsupported rule operation: %s", cr.Operation)
	}
}
