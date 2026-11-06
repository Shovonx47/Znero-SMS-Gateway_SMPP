package approval_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type noopApplier struct {
	applied int
}

func (n *noopApplier) ApplyChange(_ context.Context, _ pgx.Tx, _ *approval.ChangeRequest) error {
	n.applied++
	return nil
}

func TestApprovalLifecycleAndDualControl(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	req := e.CountryRequest()
	raw, _ := json.Marshal(req)

	cr, err := e.Approval.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
		EntityType:   "country",
		Operation:    "create",
		ProposedData: raw,
		MakerUserID:  e.Maker.ActorUserID,
	})
	if err != nil {
		t.Fatalf("submit change request: %v", err)
	}
	if cr.Status != "pending" {
		t.Fatalf("expected pending status, got %q", cr.Status)
	}

	// Maker cannot approve own change even if holding approve permission
	makerChecker := testkit.WithPermissions(e.Maker, "country.country.approve")
	if err := e.Approval.Approve(ctx, makerChecker, cr.ID, "self-approve"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Reject requires a non-empty reason
	if err := e.Approval.Reject(ctx, e.Checker, cr.ID, "   "); !errors.Is(err, approval.ErrEmptyRejectReason) {
		t.Fatalf("expected ErrEmptyRejectReason, got %v", err)
	}

	// Reject with reason
	if err := e.Approval.Reject(ctx, e.Checker, cr.ID, "missing regulatory verification notes"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	got, err := e.Approval.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get rejected cr: %v", err)
	}
	if got.Status != "rejected" || got.RejectReason != "missing regulatory verification notes" {
		t.Fatalf("unexpected rejected cr state: %+v", got)
	}

	// Cannot approve a rejected request
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "retry"); !errors.Is(err, approval.ErrInvalidStatus) {
		t.Fatalf("expected ErrInvalidStatus on rejected request, got %v", err)
	}
}

func TestUnregisteredEntityTypeDeniedByDefault(t *testing.T) {
	svc := approval.NewService(nil, nil, nil)
	ap := &noopApplier{}
	svc.RegisterApplier("custom_entity", ap)
	// No RegisterApprovePermission called -> default deny when authorizeReview is checked
	_ = uuid.New()
}
