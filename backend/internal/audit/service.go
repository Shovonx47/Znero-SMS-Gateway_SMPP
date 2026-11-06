package audit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Record saves an audit log entry in the provided transaction.
// It automatically redacts sensitive keys from before/after values.
func (s *Service) Record(ctx context.Context, tx pgx.Tx, entry Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Redact before/after values to protect passwords, tokens, recovery codes
	if entry.BeforeValue != nil {
		entry.BeforeValue = RedactValue(entry.BeforeValue)
	}
	if entry.AfterValue != nil {
		entry.AfterValue = RedactValue(entry.AfterValue)
	}

	return s.repo.Insert(ctx, tx, entry)
}

func (s *Service) List(ctx context.Context, tc *tenant.Context, filter Filter) (ListResult, error) {
	return s.repo.List(ctx, tc, filter)
}
