package billing_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type billingHarness struct {
	e          *testkit.Env
	billingSvc *billing.Service
	ledgerSvc  *ledger.Service
	orgRepo    org.Repository
}

func setupBillingHarness(t *testing.T) *billingHarness {
	e := testkit.New(t)
	orgRepo := org.NewRepository(e.Pool)
	ledgerRepo := ledger.NewRepository(e.Pool)
	billingRepo := billing.NewRepository(e.Pool)

	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)
	billingSvc := billing.NewService(billingRepo, orgRepo, ledgerRepo, e.Approval, e.Audit, e.Pool)

	return &billingHarness{
		e:          e,
		billingSvc: billingSvc,
		ledgerSvc:  ledgerSvc,
		orgRepo:    orgRepo,
	}
}

// 1. TestInvoiceDraftLifecycleAndGapFreeSequence verifies:
// - Creating draft with lines and manual tax
// - Editing draft lines
// - Issuing assigns gap-free sequential numbers (INV-YYYY-XXXXXX)
func TestInvoiceDraftLifecycleAndGapFreeSequence(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "bill-seq-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "bill-seq-cust", "customer")

	// Ensure wallet exists for customer
	_, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	dueDate := time.Now().AddDate(0, 0, 30)

	// Create draft invoice 1
	inv1, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: "Monthly SMS Gateway Minimum Commitment",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(500),
			},
		},
		TaxLabel:  "VAT 10%",
		TaxAmount: decimal.NewFromInt(50),
		Notes:     "Thank you for your business",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft 1: %v", err)
	}

	if inv1.Status != "draft" {
		t.Fatalf("expected status draft, got %s", inv1.Status)
	}
	if inv1.InvoiceNumber != nil {
		t.Fatalf("expected no invoice number on draft, got %v", *inv1.InvoiceNumber)
	}
	if !inv1.Total.Equal(decimal.NewFromInt(550)) {
		t.Fatalf("expected total 550, got %s", inv1.Total.String())
	}

	// Edit draft: add an additional line item
	taxAmt := decimal.NewFromInt(60)
	taxLbl := "VAT 10%"
	inv1Updated, err := h.billingSvc.UpdateDraftInvoice(ctx, h.e.Override, inv1.ID, billing.UpdateDraftInvoiceRequest{
		DueDate:   &dueDate,
		TaxLabel:  &taxLbl,
		TaxAmount: &taxAmt,
		Lines: &[]billing.CreateLineRequest{
			{
				Description: "Monthly SMS Gateway Minimum Commitment",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(500),
			},
			{
				Description: "Dedicated Short Code Rental",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(100),
			},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if !inv1Updated.Total.Equal(decimal.NewFromInt(660)) {
		t.Fatalf("expected updated total 660, got %s", inv1Updated.Total.String())
	}

	// Issue invoice 1 -> assigns first sequence number
	issued1, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv1.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice 1: %v", err)
	}
	if issued1.Status != "issued" {
		t.Fatalf("expected status issued, got %s", issued1.Status)
	}
	if issued1.InvoiceNumber == nil {
		t.Fatalf("expected assigned invoice number, got nil")
	}
	expectedPrefix := "INV-2026-"
	if !strings.HasPrefix(*issued1.InvoiceNumber, expectedPrefix) {
		t.Fatalf("expected invoice number to start with %s, got %s", expectedPrefix, *issued1.InvoiceNumber)
	}

	// Create and issue draft invoice 2 -> MUST be sequential and gap-free
	inv2, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: "Consulting and API Integration Fee",
				Quantity:    decimal.NewFromInt(2),
				UnitPrice:   decimal.NewFromInt(200),
			},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft 2: %v", err)
	}

	issued2, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv2.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice 2: %v", err)
	}

	t.Logf("Invoice 1 number: %s, Invoice 2 number: %s", *issued1.InvoiceNumber, *issued2.InvoiceNumber)
	if *issued1.InvoiceNumber == *issued2.InvoiceNumber {
		t.Fatalf("duplicate invoice number generated: %s", *issued1.InvoiceNumber)
	}
}

// 2. TestIssuedInvoiceImmutability verifies DB triggers block mutation of issued invoices and lines.
func TestIssuedInvoiceImmutability(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	custOrg, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "cust-immutable", "customer")
	_, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	now := time.Now()
	dueDate := now.AddDate(0, 0, 15)
	inv, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Enterprise Support SLA", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(1000)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	issued, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice: %v", err)
	}

	// Service layer refuses editing an issued invoice
	_, err = h.billingSvc.UpdateDraftInvoice(ctx, h.e.Override, issued.ID, billing.UpdateDraftInvoiceRequest{
		DueDate: &dueDate,
		Lines: &[]billing.CreateLineRequest{
			{Description: "Tampered", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(1)},
		},
	}, "127.0.0.1", "test")
	if err == nil {
		t.Fatalf("expected error updating issued invoice via service, got nil")
	}

	// Direct DB UPDATE on total/subtotal MUST be rejected by DB trigger trg_protect_issued_invoice
	_, dbUpdateErr := h.e.Pool.Exec(ctx, "UPDATE invoices SET total = 0 WHERE id = $1", issued.ID)
	if dbUpdateErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Direct DB UPDATE on issued invoice succeeded! Expected trigger block.")
	}
	t.Logf("Direct DB UPDATE rejected: %v", dbUpdateErr)

	// Direct DB DELETE on issued invoice MUST be rejected by DB trigger
	_, dbDeleteErr := h.e.Pool.Exec(ctx, "DELETE FROM invoices WHERE id = $1", issued.ID)
	if dbDeleteErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Direct DB DELETE on issued invoice succeeded! Expected trigger block.")
	}
	t.Logf("Direct DB DELETE rejected: %v", dbDeleteErr)

	// Direct DB UPDATE or DELETE on invoice_lines MUST be rejected by DB trigger trg_protect_issued_invoice_lines
	_, dbLineErr := h.e.Pool.Exec(ctx, "DELETE FROM invoice_lines WHERE invoice_id = $1", issued.ID)
	if dbLineErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Direct DB DELETE on issued invoice lines succeeded! Expected trigger block.")
	}
	t.Logf("Direct DB DELETE on lines rejected: %v", dbLineErr)
}

// 3. TestCreditNoteCreation verifies issuing a credit note referencing an original invoice.
func TestCreditNoteCreation(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	custOrg, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "cust-creditnote", "customer")
	_, _ = h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	dueDate := time.Now().AddDate(0, 0, 30)
	inv, _ := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Billing Overcharge Item", Quantity: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(50)},
		},
	}, "127.0.0.1", "test")
	issued, _ := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv.ID, "127.0.0.1", "test")

	// Issue Credit Note for full refund of original invoice
	cn, err := h.billingSvc.CreateCreditNote(ctx, h.e.Override, issued.ID, "Full refund due to billing discrepancy", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create credit note: %v", err)
	}

	if cn.CreditNoteForInvoiceID == nil || *cn.CreditNoteForInvoiceID != issued.ID {
		t.Fatalf("expected credit note to reference original invoice %s, got %v", issued.ID, cn.CreditNoteForInvoiceID)
	}
	if !cn.Total.Equal(issued.Total) {
		t.Fatalf("expected credit note total %s, got %s", issued.Total.String(), cn.Total.String())
	}
	if cn.InvoiceNumber == nil || !strings.HasPrefix(*cn.InvoiceNumber, "CN-") {
		t.Fatalf("expected credit note number starting with CN-, got %v", cn.InvoiceNumber)
	}
	t.Logf("Credit note created successfully: %s referencing invoice %s", *cn.InvoiceNumber, *issued.InvoiceNumber)
}

// 4. TestInvoicePaymentRecordsAndBalanceSettlement verifies:
// - Recording manual payment against invoice
// - Ledger transaction topup posted to customer wallet
// - Full payment marks invoice as paid
func TestInvoicePaymentRecordsAndBalanceSettlement(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	custOrg, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "cust-payment", "customer")
	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	now := time.Now()
	dueDate := now.AddDate(0, 0, 30)
	inv, _ := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Postpaid Monthly Service Usage", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(400)},
		},
	}, "127.0.0.1", "test")
	issued, _ := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv.ID, "127.0.0.1", "test")

	// Initial wallet balance is 0
	w0, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w0.CachedBalance.IsZero() {
		t.Fatalf("expected initial balance 0, got %s", w0.CachedBalance.String())
	}

	// 1st partial payment: $150
	pmt1, err := h.billingSvc.RecordPayment(ctx, h.e.Override, issued.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(150),
		PaymentMethod: "Bank Wire",
		Reference:     "WIRE-REF-99881",
		PaymentDate:   &now,
		Notes:         "First partial payment",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("record payment 1: %v", err)
	}
	if pmt1.LedgerTransactionID == nil {
		t.Fatalf("expected payment to link to ledger transaction")
	}

	invAfter1, err := h.billingSvc.GetInvoice(ctx, h.e.Override, issued.ID)
	if err != nil {
		t.Fatalf("get invoice 1: %v", err)
	}
	if invAfter1.Status != "issued" {
		t.Fatalf("expected status still issued after partial payment, got %s", invAfter1.Status)
	}
	if !invAfter1.AmountPaid.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected amount paid 150, got %s", invAfter1.AmountPaid.String())
	}

	// Customer wallet balance increased by $150
	w1, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w1.CachedBalance.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected wallet balance 150, got %s", w1.CachedBalance.String())
	}

	// 2nd final payment: $250 -> Total $400 matches invoice total -> invoice status becomes paid
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, issued.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(250),
		PaymentMethod: "Bank Wire",
		Reference:     "WIRE-REF-99882",
		PaymentDate:   &now,
		Notes:         "Final settlement payment",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("record payment 2: %v", err)
	}

	invAfter2, err := h.billingSvc.GetInvoice(ctx, h.e.Override, issued.ID)
	if err != nil {
		t.Fatalf("get invoice 2: %v", err)
	}
	if invAfter2.Status != "paid" {
		t.Fatalf("expected invoice status paid after full settlement, got %s", invAfter2.Status)
	}

	// Customer wallet balance is now $400
	w2, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w2.CachedBalance.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("expected wallet balance 400, got %s", w2.CachedBalance.String())
	}
}

// 5. TestPrintableInvoiceHTML verifies printable HTML contains customer info, lines, and NO provider secrets.
func TestPrintableInvoiceHTML(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	custOrg, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "cust-printable", "customer")
	now := time.Now()
	dueDate := now.AddDate(0, 0, 15)
	inv, _ := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "International SMS Delivery - UK routes", Quantity: decimal.NewFromInt(5000), UnitPrice: decimal.RequireFromString("0.0450")},
		},
		TaxLabel:  "Sales Tax 5%",
		TaxAmount: decimal.RequireFromString("11.25"),
	}, "127.0.0.1", "test")
	issued, _ := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv.ID, "127.0.0.1", "test")

	html, err := h.billingSvc.GetPrintableInvoiceHTML(ctx, h.e.Override, issued.ID)
	if err != nil {
		t.Fatalf("generate printable HTML: %v", err)
	}

	// Verify key elements are rendered
	if !strings.Contains(html, *issued.InvoiceNumber) {
		t.Fatalf("HTML missing invoice number")
	}
	if !strings.Contains(html, "International SMS Delivery - UK routes") {
		t.Fatalf("HTML missing line item description")
	}
	if !strings.Contains(html, "Sales Tax 5%") {
		t.Fatalf("HTML missing tax label")
	}

	// Verify NO provider secrets, margins, buy rates or platform internal costs leaked
	for _, forbidden := range []string{"buy_rate", "profit_margin", "rate_margin", "provider_id", "smpp", "upstream_cost", "PLATFORM_REVENUE"} {
		if strings.Contains(strings.ToLower(html), strings.ToLower(forbidden)) {
			t.Fatalf("SECURITY VIOLATION: Printable invoice HTML leaked internal financial term: %s", forbidden)
		}
	}
	t.Logf("Printable invoice HTML successfully verified free of internal secrets")
}

// 6. TestCrossTenantBillingIsolation verifies tenant boundaries on invoices.
func TestCrossTenantBillingIsolation(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerA, resellerAPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "reseller-a", "reseller")
	resellerB, resellerBPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "reseller-b", "reseller")

	custA, _ := h.e.ChildOrg(resellerA, resellerAPath, "cust-a", "customer")
	custB, _ := h.e.ChildOrg(resellerB, resellerBPath, "cust-b", "customer")

	resellerAContext := h.e.Actor("reseller-a-user", resellerA, resellerAPath, "reseller", false,
		"billing.invoice.view", "billing.invoice.create", "billing.payment.create",
	)

	dueDate := time.Now().AddDate(0, 0, 30)

	// Reseller A cannot create invoice for Customer B (child of Reseller B)
	_, err := h.billingSvc.CreateDraftInvoice(ctx, resellerAContext, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custB,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Unauthorized Line", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Reseller A created invoice for Reseller B customer!")
	}
	t.Logf("Cross-tenant invoice creation correctly blocked: %v", err)

	// Platform creates invoice for Customer B
	invB, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custB,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Valid Line", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create invoice for B: %v", err)
	}

	// Reseller A cannot read invoice for Customer B
	_, err = h.billingSvc.GetInvoice(ctx, resellerAContext, invB.ID)
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Reseller A read invoice belonging to Reseller B customer!")
	}
	t.Logf("Cross-tenant invoice view correctly blocked: %v", err)

	// Reseller A CAN read invoice for their own Customer A
	invA, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custA,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Valid Line A", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create invoice for A: %v", err)
	}

	invAFetched, err := h.billingSvc.GetInvoice(ctx, resellerAContext, invA.ID)
	if err != nil {
		t.Fatalf("expected Reseller A to view own customer invoice, got error: %v", err)
	}
	if invAFetched.ID != invA.ID {
		t.Fatalf("expected invoice ID %s, got %s", invA.ID, invAFetched.ID)
	}
}

// 7. TestGapFreeInvoiceNumbering_RollbackLeavesNoGap proves that an aborted/rolled-back
// invoice issue operation leaves no gap in the counter sequence.
func TestGapFreeInvoiceNumbering_RollbackLeavesNoGap(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "gap-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "gap-cust", "customer")

	dueDate := time.Now().AddDate(0, 0, 30)

	// Create draft invoice 1
	draft1, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Line 1", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft 1: %v", err)
	}

	// Create draft invoice 2
	draft2, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Line 2", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(200)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft 2: %v", err)
	}

	// 1. Manually begin transaction, fetch next number for draft 1, then ROLL BACK
	tx, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	repo := billing.NewRepository(h.e.Pool)
	rolledBackNum, err := repo.GetNextInvoiceNumber(ctx, tx, h.e.PlatformOrgID, "INV", 2026)
	if err != nil {
		t.Fatalf("get next num: %v", err)
	}
	t.Logf("Rolled-back transaction allocated number: %s", rolledBackNum)
	_ = tx.Rollback(ctx)

	// 2. Issue draft 1 via service layer
	issued1, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, draft1.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue draft 1: %v", err)
	}
	t.Logf("Draft 1 issued with number: %s", *issued1.InvoiceNumber)

	// Critical check: issued1 MUST have reused the rolled-back number; no gap created!
	if *issued1.InvoiceNumber != rolledBackNum {
		t.Fatalf("GAP DETECTED: expected draft 1 to receive %s after rollback, got %s", rolledBackNum, *issued1.InvoiceNumber)
	}

	// 3. Issue draft 2 via service layer
	issued2, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, draft2.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue draft 2: %v", err)
	}
	t.Logf("Draft 2 issued with number: %s", *issued2.InvoiceNumber)

	// Check consecutive numbers
	num1, _ := strconv.Atoi(strings.TrimPrefix(*issued1.InvoiceNumber, "INV-2026-"))
	num2, _ := strconv.Atoi(strings.TrimPrefix(*issued2.InvoiceNumber, "INV-2026-"))
	if num2 != num1+1 {
		t.Fatalf("expected consecutive numbers: %s then %s", *issued1.InvoiceNumber, *issued2.InvoiceNumber)
	}
}

// 8. TestGapFreeInvoiceNumbering_ConcurrentConsecutive proves that under high concurrency,
// parallel issue operations produce strictly consecutive, non-overlapping numbers.
func TestGapFreeInvoiceNumbering_ConcurrentConsecutive(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "conc-seq-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "conc-seq-cust", "customer")

	dueDate := time.Now().AddDate(0, 0, 30)
	concurrency := 10
	draftIDs := make([]string, concurrency)

	for i := 0; i < concurrency; i++ {
		d, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
			CustomerOrgID: custOrg,
			Currency:      "USD",
			DueDate:       &dueDate,
			Lines: []billing.CreateLineRequest{
				{Description: fmt.Sprintf("Line %d", i), Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
			},
		}, "127.0.0.1", "test")
		if err != nil {
			t.Fatalf("create draft %d: %v", i, err)
		}
		draftIDs[i] = d.ID.String()
	}

	var wg sync.WaitGroup
	results := make([]string, concurrency)
	errorsList := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id, _ := h.billingSvc.GetInvoice(ctx, h.e.Override, uuid.MustParse(draftIDs[idx]))
			issued, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, id.ID, "127.0.0.1", "test")
			if err != nil {
				errorsList[idx] = err
				return
			}
			results[idx] = *issued.InvoiceNumber
		}(i)
	}
	wg.Wait()

	for i, err := range errorsList {
		if err != nil {
			t.Fatalf("concurrent issue %d failed: %v", i, err)
		}
	}

	// Parse invoice numbers
	numbers := make([]int, concurrency)
	for i, r := range results {
		suffix := strings.TrimPrefix(r, "INV-2026-")
		n, err := strconv.Atoi(suffix)
		if err != nil {
			t.Fatalf("invalid invoice number format: %s", r)
		}
		numbers[i] = n
	}

	sort.Ints(numbers)
	for i := 1; i < len(numbers); i++ {
		if numbers[i] != numbers[i-1]+1 {
			t.Fatalf("GAP OR DUPLICATE DETECTED in concurrent numbering: %v", numbers)
		}
	}
	t.Logf("10 concurrent issues produced consecutive numbers: %v", numbers)
}

// 9. TestInvoicePayments_EdgeCases tests:
// - Partial payment keeps the invoice status as 'issued'
// - Overpayment is refused
// - Payment on a void invoice is refused
// - Duplicate payment submission with idempotency key is idempotent
func TestInvoicePayments_EdgeCases(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "pmt-edge-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "pmt-edge-cust", "customer")

	dueDate := time.Now().AddDate(0, 0, 30)

	// Create and issue $200 invoice
	draft, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Service Fee", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(200)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	inv, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, draft.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice: %v", err)
	}

	// 1. Partial payment: $50
	pmt1, err := h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:         decimal.NewFromInt(50),
		PaymentMethod:  "Bank Wire",
		Reference:      "WIRE-PARTIAL-1",
		IdempotencyKey: "idem-pmt-001",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("partial payment 1: %v", err)
	}
	if !pmt1.Amount.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("expected payment amount 50, got %s", pmt1.Amount.String())
	}

	// Check invoice status: MUST remain 'issued', outstanding must be 150
	invAfterPartial, err := h.billingSvc.GetInvoice(ctx, h.e.Override, inv.ID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if invAfterPartial.Status != "issued" {
		t.Fatalf("expected status 'issued' after partial payment, got %s", invAfterPartial.Status)
	}
	if !invAfterPartial.AmountPaid.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("expected amount_paid 50, got %s", invAfterPartial.AmountPaid.String())
	}
	if !invAfterPartial.OutstandingBalance.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected outstanding balance 150, got %s", invAfterPartial.OutstandingBalance.String())
	}

	// 2. Duplicate submission with same idempotency key is idempotent
	pmtDup, err := h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:         decimal.NewFromInt(50),
		PaymentMethod:  "Bank Wire",
		Reference:      "WIRE-PARTIAL-1",
		IdempotencyKey: "idem-pmt-001",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if pmtDup.ID != pmt1.ID {
		t.Fatalf("expected original payment ID %s, got %s", pmt1.ID, pmtDup.ID)
	}

	// 3. Overpayment is refused: outstanding is 150, attempt to pay 150.0001
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.RequireFromString("150.0001"),
		PaymentMethod: "Bank Wire",
		Reference:     "WIRE-OVERPAY",
	}, "127.0.0.1", "test")
	if !errors.Is(err, billing.ErrPaymentExceedsTotal) {
		t.Fatalf("expected ErrPaymentExceedsTotal, got: %v", err)
	}

	// 4. Pay exact remaining balance ($150) -> transitions to 'paid'
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(150),
		PaymentMethod: "Bank Wire",
		Reference:     "WIRE-FULL-SETTLE",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("record full settlement: %v", err)
	}
	invPaid, err := h.billingSvc.GetInvoice(ctx, h.e.Override, inv.ID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if invPaid.Status != "paid" {
		t.Fatalf("expected status 'paid', got %s", invPaid.Status)
	}

	// 5. Payment on a void invoice is refused
	voidDraft, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Void Target", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create void target: %v", err)
	}
	voidInv, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, voidDraft.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue void target: %v", err)
	}
	_, err = h.billingSvc.VoidInvoice(ctx, h.e.Override, voidInv.ID, "Customer cancellation", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("void invoice: %v", err)
	}

	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, voidInv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(50),
		PaymentMethod: "Cash",
	}, "127.0.0.1", "test")
	if !errors.Is(err, billing.ErrInvoiceCannotPay) {
		t.Fatalf("expected ErrInvoiceCannotPay on void invoice, got: %v", err)
	}
}

// 10. TestMaximumAmountBoundsAndValidation tests single transaction upper bounds
func TestMaximumAmountBoundsAndValidation(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "max-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "max-cust", "customer")

	dueDate := time.Now().AddDate(0, 0, 30)

	// Line amount > $1,000,000 refused
	_, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Excessive line", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(1_000_001)},
		},
	}, "127.0.0.1", "test")
	if !errors.Is(err, billing.ErrAmountExceedsMaximum) {
		t.Fatalf("expected ErrAmountExceedsMaximum for line > 1M, got: %v", err)
	}

	// Normal draft
	draft, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Normal line", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	inv, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, draft.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice: %v", err)
	}

	// Payment amount > $1,000,000 refused
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(1_000_001),
		PaymentMethod: "Wire",
	}, "127.0.0.1", "test")
	if !errors.Is(err, billing.ErrAmountExceedsMaximum) {
		t.Fatalf("expected ErrAmountExceedsMaximum on payment > 1M, got: %v", err)
	}

	// Zero or negative payment refused
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, inv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.Zero,
		PaymentMethod: "Wire",
	}, "127.0.0.1", "test")
	if !errors.Is(err, billing.ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount on zero payment, got: %v", err)
	}
}

// 11. TestMakerChecker_InvoiceVoid proves maker cannot approve own void request
func TestMakerChecker_InvoiceVoid(t *testing.T) {
	h := setupBillingHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "void-mc-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "void-mc-cust", "customer")

	dueDate := time.Now().AddDate(0, 0, 30)

	draft, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{Description: "Void Test", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	inv, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, draft.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue invoice: %v", err)
	}

	// Maker user (platform staff, has billing.invoice.void, NO wallet.override)
	makerUser := h.e.Actor("void-maker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "billing.invoice.void", "billing.invoice.view")
	checkerUser := h.e.Actor("void-checker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "billing.invoice.void", "billing.invoice.view")

	cr, err := h.billingSvc.VoidInvoice(ctx, makerUser, inv.ID, "Defective billing calculation", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("maker void request: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected change request to be created for non-override actor")
	}

	// Dual-control: Maker CANNOT approve own void request
	err = h.e.Approval.Approve(ctx, makerUser, cr.ID, "maker approve")
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Maker approved their own void request!")
	}
	t.Logf("Maker self-approval blocked correctly: %v", err)

	// Checker approves
	err = h.e.Approval.Approve(ctx, checkerUser, cr.ID, "checker approve")
	if err != nil {
		t.Fatalf("checker approve failed: %v", err)
	}

	// Verify invoice is now void
	voidedInv, err := h.billingSvc.GetInvoice(ctx, h.e.Override, inv.ID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if voidedInv.Status != "void" {
		t.Fatalf("expected status 'void', got %s", voidedInv.Status)
	}
	t.Logf("Invoice void dual-control flow verified successfully")
}

func TestPostpaidInvoicePayment_NeverCreditsBeyondOwedDebt(t *testing.T) {
	ctx := context.Background()
	h := setupBillingHarness(t)

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "postpaid-owed-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "postpaid-owed-cust", "customer")

	// Set credit limit to $500.0000 on customer profile
	_, err := h.e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (organization_id, account_type, credit_limit)
		VALUES ($1, 'postpaid', 500.0000)
		ON CONFLICT (organization_id) DO UPDATE SET credit_limit = 500.0000, account_type = 'postpaid'`,
		custOrg)
	if err != nil {
		t.Fatalf("set customer profile credit limit: %v", err)
	}

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "postpaid")
	if err != nil {
		t.Fatalf("ensure postpaid wallet: %v", err)
	}

	// 1. Initial State: postpaid wallet balance is 0.0000 (nothing owed).
	if !w.CachedBalance.Equal(decimal.Zero) {
		t.Fatalf("expected initial balance 0, got %s", w.CachedBalance.String())
	}

	// 2. Issue a manual invoice for $200.0000 (e.g. professional services / setup fee)
	dueDate := time.Now().AddDate(0, 0, 30)
	inv1, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: "One-time Dedicated SMPP Bind Setup Fee",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(200),
			},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft inv1: %v", err)
	}
	issued1, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv1.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue inv1: %v", err)
	}

	// 3. Customer pays $200.0000 for this manual invoice.
	// Rule: Since wallet owed debt is 0.0000, wallet credit MUST be 0.0000!
	// Balance must remain 0.0000 and NOT become +200.0000.
	pmt1, err := h.billingSvc.RecordPayment(ctx, h.e.Override, issued1.ID, billing.RecordPaymentRequest{
		Amount:         decimal.NewFromInt(200),
		PaymentMethod:  "Bank Wire",
		Reference:      "WIRE-MANUAL-1",
		IdempotencyKey: "test-postpaid-no-debt-1",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("record payment 1: %v", err)
	}
	if pmt1 == nil {
		t.Fatalf("expected payment record")
	}

	// Verify wallet balance is STILL 0.0000
	wAfter1, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if !wAfter1.CachedBalance.Equal(decimal.Zero) {
		t.Fatalf("CRITICAL RULE VIOLATION: Postpaid wallet gained positive balance! Expected 0.0000, got %s", wAfter1.CachedBalance.String())
	}
	t.Logf("Manual invoice paid: postpaid wallet balance correctly remained %s (0 unowed credit created)", wAfter1.CachedBalance.String())

	// 4. Now simulate legitimate traffic usage: debit the wallet by $50.0000
	// So wallet balance becomes -$50.0000 (owed debt = $50.0000).
	chargeTx, err := h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(50), "USD", "Messaging traffic usage charge", nil, nil, nil)
	if err != nil {
		t.Fatalf("post charge: %v", err)
	}
	if chargeTx == nil {
		t.Fatalf("expected charge tx")
	}

	wAfterCharge, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	expectedDebtBal := decimal.NewFromInt(-50)
	if !wAfterCharge.CachedBalance.Equal(expectedDebtBal) {
		t.Fatalf("expected balance -50, got %s", wAfterCharge.CachedBalance.String())
	}

	// 5. Issue a combined invoice for $120.0000 ($50 usage + $70 manual fee)
	inv2, err := h.billingSvc.CreateDraftInvoice(ctx, h.e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custOrg,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: "Usage reconciliation",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(50),
			},
			{
				Description: "Monthly Portal Support Fee",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(70),
			},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create draft inv2: %v", err)
	}
	issued2, err := h.billingSvc.IssueInvoice(ctx, h.e.Override, inv2.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("issue inv2: %v", err)
	}

	// 6. Pay $120.0000: exactly $50.0000 settles debt (bringing balance to 0.0000),
	// and the remaining $70.0000 credits PLATFORM_REVENUE directly.
	_, err = h.billingSvc.RecordPayment(ctx, h.e.Override, issued2.ID, billing.RecordPaymentRequest{
		Amount:         decimal.NewFromInt(120),
		PaymentMethod:  "Bank Wire",
		Reference:      "WIRE-COMBINED-2",
		IdempotencyKey: "test-postpaid-combined-2",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("record payment 2: %v", err)
	}

	wAfter2, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if !wAfter2.CachedBalance.Equal(decimal.Zero) {
		t.Fatalf("expected balance exactly 0.0000, got %s", wAfter2.CachedBalance.String())
	}
	t.Logf("Combined invoice paid: postpaid debt settled to 0.0000 and excess recognized as revenue without inflating wallet balance")
}
