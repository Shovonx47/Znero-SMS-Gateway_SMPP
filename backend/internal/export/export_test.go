package export_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
	"github.com/znero-international/znero/backend/internal/export"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// TestStage9_FormulaInjectionSanitization verifies that dangerous formula characters
// are escaped with a leading single quote.
func TestStage9_FormulaInjectionSanitization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"=cmd|' /C calc'!A0", "'=cmd|' /C calc'!A0"},
		{"+123456789", "'+123456789"},
		{"-987654321", "'-987654321"},
		{"@SUM(A1:A10)", "'@SUM(A1:A10)"},
		{"\t=1+1", "'\t=1+1"},
		{"\r@evil", "'\r@evil"},
		{"   =malicious", "'   =malicious"},
		{"Hello World", "Hello World"},
		{"12345", "12345"},
		{"normal text", "normal text"},
	}

	for _, tc := range testCases {
		sanitized := export.SanitizeCSVCell(tc.input)
		if sanitized != tc.expected {
			t.Errorf("SanitizeCSVCell(%q) = %q, want %q", tc.input, sanitized, tc.expected)
		}
	}

	// Verify CSV generation
	gen := export.NewGenerator()
	headers := []string{"Name", "Formula"}
	rows := [][]string{
		{"Alice", "=cmd|' /C calc'!A0"},
		{"Bob", "+1234"},
	}
	csvBytes, err := gen.GenerateCSV(headers, rows)
	if err != nil {
		t.Fatalf("GenerateCSV failed: %v", err)
	}

	csvStr := string(csvBytes)
	if !strings.Contains(csvStr, "'=cmd|' /C calc'!A0") {
		t.Errorf("CSV output missing sanitized formula: %s", csvStr)
	}
	if !strings.Contains(csvStr, "'+1234") {
		t.Errorf("CSV output missing sanitized plus: %s", csvStr)
	}

	// Verify XLSX generation
	xlsxBytes, err := gen.GenerateXLSX("SanitizeTest", headers, rows)
	if err != nil {
		t.Fatalf("GenerateXLSX failed: %v", err)
	}

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("test_sanitize_%d.xlsx", time.Now().UnixNano()))
	defer os.Remove(tmpFile)
	if err := os.WriteFile(tmpFile, xlsxBytes, 0644); err != nil {
		t.Fatalf("write temp xlsx: %v", err)
	}

	f, err := excelize.OpenFile(tmpFile)
	if err != nil {
		t.Fatalf("open excel file: %v", err)
	}
	defer f.Close()

	val, err := f.GetCellValue("SanitizeTest", "B2")
	if err != nil {
		t.Fatalf("get cell B2: %v", err)
	}
	if val != "'=cmd|' /C calc'!A0" {
		t.Errorf("XLSX cell B2 = %q, want sanitized formula", val)
	}
}

// TestStage9_ExportAsyncWorkflowAndFormats tests CSV, JSON, and XLSX export generation against live data.
func TestStage9_ExportAsyncWorkflowAndFormats(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	var adminUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&adminUserID)
	if err != nil {
		t.Fatalf("fetch existing user: %v", err)
	}

	testOrgID := uuid.New()
	testPath := "/" + platID.String() + "/" + testOrgID.String() + "/"
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Export Workflow Org', 'customer', $2, $3, 'active')
		ON CONFLICT DO NOTHING;
	`, testOrgID, platID, testPath)
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}

	// Seed multiple test messages
	var msgIDs []uuid.UUID
	for i := 0; i < 5; i++ {
		msgID := uuid.New()
		msgIDs = append(msgIDs, msgID)
		_, err = pool.Exec(ctx, `
			INSERT INTO messages (
				id, created_at, customer_org_id, original_destination, destination,
				sender_id, encrypted_body, key_id, status, sell_price, sell_currency,
				estimated_buy_cost, actual_buy_cost, buy_currency, expires_at
			) VALUES (
				$1, now(), $2, $3, $3,
				'EXPORT', 'testbody'::bytea, 'k1', 'delivered', 0.0450, 'USD',
				0.0150, 0.0150, 'USD', now() + interval '24 hours'
			)
		`, msgID, testOrgID, fmt.Sprintf("+120255501%02d", i))
		if err != nil {
			t.Fatalf("seed msg %d: %v", i, err)
		}
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		for _, mid := range msgIDs {
			_, _ = pool.Exec(cctx, `DELETE FROM messages WHERE id = $1`, mid)
		}
		_, _ = pool.Exec(cctx, `DELETE FROM export_jobs WHERE organization_id = $1`, testOrgID)
		_, _ = pool.Exec(cctx, `DELETE FROM organizations WHERE id = $1`, testOrgID)
	})

	storageDir := filepath.Join(os.TempDir(), fmt.Sprintf("znero_exports_%d", time.Now().UnixNano()))
	defer os.RemoveAll(storageDir)

	repo := export.NewRepository(pool, nil)
	svc := export.NewService(repo, storageDir, nil)

	tc := &tenant.Context{
		ActorUserID:     adminUserID,
		ActorOrgID:      testOrgID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	formats := []string{export.FormatCSV, export.FormatJSON, export.FormatXLSX}
	for _, fmtType := range formats {
		req := export.CreateJobRequest{
			ExportType:   export.TypeMessages,
			Format:       fmtType,
			FilterParams: map[string]interface{}{"status": "delivered"},
		}

		job, err := svc.SubmitJob(ctx, tc, req)
		if err != nil {
			t.Fatalf("SubmitJob(%s) failed: %v", fmtType, err)
		}
		if job.Status != export.StatusPending {
			t.Fatalf("initial status = %s, want %s", job.Status, export.StatusPending)
		}

		// Run sync to guarantee execution completes immediately
		svc.ProcessJobSync(ctx, job.ID, tc)

		updated, err := svc.GetJob(ctx, tc, job.ID)
		if err != nil {
			t.Fatalf("GetJob(%s) failed: %v", fmtType, err)
		}
		if updated.Status != export.StatusCompleted {
			errMsg := ""
			if updated.ErrorMessage != nil {
				errMsg = *updated.ErrorMessage
			}
			t.Fatalf("job status = %s (error: %s), want %s", updated.Status, errMsg, export.StatusCompleted)
		}
		if updated.RowCount != 5 {
			t.Errorf("job row count = %d, want 5", updated.RowCount)
		}
		if updated.FilePath == nil || *updated.FilePath == "" {
			t.Fatalf("job file path is nil or empty")
		}

		// Verify file exists and has content
		filePath, filename, contentType, err := svc.DownloadJob(ctx, tc, job.ID)
		if err != nil {
			t.Fatalf("DownloadJob(%s) failed: %v", fmtType, err)
		}
		if !strings.HasSuffix(filename, "."+fmtType) {
			t.Errorf("download filename %q does not match format %q", filename, fmtType)
		}
		if contentType == "" {
			t.Errorf("content type is empty for %s", fmtType)
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read exported file: %v", err)
		}
		if len(content) == 0 {
			t.Errorf("exported file is empty for format %s", fmtType)
		}

		if fmtType == export.FormatJSON {
			var recs []map[string]interface{}
			if err := json.Unmarshal(content, &recs); err != nil {
				t.Fatalf("unmarshal exported json: %v", err)
			}
			if len(recs) != 5 {
				t.Errorf("json records count = %d, want 5", len(recs))
			}
		}
	}
}

// TestStage9_TenantIsolation verifies that export data strictly isolates tenant records
// and strips provider identities, buy costs, and margins from customer exports.
func TestStage9_TenantIsolation(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	var anyUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&anyUserID)
	if err != nil {
		t.Fatalf("fetch user: %v", err)
	}

	orgAID := uuid.New()
	orgBID := uuid.New()
	pathA := "/" + platID.String() + "/" + orgAID.String() + "/"
	pathB := "/" + platID.String() + "/" + orgBID.String() + "/"

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Tenant Org A', 'customer', $2, $3, 'active'),
		       ($4, 'Tenant Org B', 'customer', $2, $5, 'active')
		ON CONFLICT DO NOTHING;
	`, orgAID, platID, pathA, orgBID, pathB)
	if err != nil {
		t.Fatalf("seed tenant orgs: %v", err)
	}

	provID := uuid.New()
	provCode := "SECRET_PROV_" + testkit.RandHex(4)
	_, err = pool.Exec(ctx, `
		INSERT INTO providers (id, name, code, provider_type, status)
		VALUES ($1, 'Super Secret Provider', $2, 'http', 'active')
	`, provID, provCode)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	connID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO provider_connections (id, provider_id, name, protocol, status)
		VALUES ($1, $2, 'Secret Conn', 'http', 'active')
	`, connID, provID)
	if err != nil {
		t.Fatalf("seed provider connection: %v", err)
	}

	msgAID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO messages (
			id, created_at, customer_org_id, original_destination, destination,
			sender_id, encrypted_body, key_id, status, sell_price, sell_currency,
			provider_connection_id, estimated_buy_cost, actual_buy_cost, buy_currency, expires_at
		) VALUES (
			$1, now(), $2, '+12025550188', '+12025550188',
			'ALERT', 'secret_body'::bytea, 'k1', 'delivered', 0.08, 'USD',
			$3, 0.02, 0.02, 'USD', now() + interval '24 hours'
		)
	`, msgAID, orgAID, connID)
	if err != nil {
		t.Fatalf("seed msg A: %v", err)
	}

	storageDir := filepath.Join(os.TempDir(), fmt.Sprintf("znero_tenant_exports_%d", time.Now().UnixNano()))
	defer os.RemoveAll(storageDir)

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM messages WHERE id = $1`, msgAID)
		_, _ = pool.Exec(cctx, `DELETE FROM provider_connections WHERE id = $1`, connID)
		_, _ = pool.Exec(cctx, `DELETE FROM providers WHERE id = $1`, provID)
		_, _ = pool.Exec(cctx, `DELETE FROM export_jobs WHERE organization_id IN ($1, $2)`, orgAID, orgBID)
		_, _ = pool.Exec(cctx, `DELETE FROM organizations WHERE id IN ($1, $2)`, orgAID, orgBID)
	})

	repo := export.NewRepository(pool, nil)
	svc := export.NewService(repo, storageDir, nil)

	tcCustA := &tenant.Context{
		ActorUserID:     anyUserID,
		ActorOrgID:      orgAID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	tcCustB := &tenant.Context{
		ActorUserID:     anyUserID,
		ActorOrgID:      orgBID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	tcPlat := &tenant.Context{
		ActorUserID:     anyUserID,
		ActorOrgID:      platID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	// 1. Submit export job for Customer A
	jobA, err := svc.SubmitJob(ctx, tcCustA, export.CreateJobRequest{
		ExportType: export.TypeMessages,
		Format:     export.FormatCSV,
	})
	if err != nil {
		t.Fatalf("SubmitJob Cust A: %v", err)
	}
	svc.ProcessJobSync(ctx, jobA.ID, tcCustA)

	// 2. Customer B cannot read or download Customer A's job
	_, err = svc.GetJob(ctx, tcCustB, jobA.ID)
	if err != export.ErrUnauthorizedTenant {
		t.Errorf("Customer B GetJob got error %v, want %v", err, export.ErrUnauthorizedTenant)
	}
	_, _, _, err = svc.DownloadJob(ctx, tcCustB, jobA.ID)
	if err != export.ErrUnauthorizedTenant {
		t.Errorf("Customer B DownloadJob got error %v, want %v", err, export.ErrUnauthorizedTenant)
	}

	// 3. Customer A downloads their file: ensure NO provider or buy cost is leaked!
	filePathA, _, _, err := svc.DownloadJob(ctx, tcCustA, jobA.ID)
	if err != nil {
		t.Fatalf("Customer A DownloadJob: %v", err)
	}
	contentA, err := os.ReadFile(filePathA)
	if err != nil {
		t.Fatalf("read Cust A export file: %v", err)
	}
	csvStrA := string(contentA)

	if strings.Contains(csvStrA, "Provider ID") || strings.Contains(csvStrA, "SECRET_PROV") || strings.Contains(csvStrA, provID.String()) {
		t.Errorf("LEAK: Customer export file contains provider ID or provider code! Content: %s", csvStrA)
	}
	if strings.Contains(csvStrA, "Actual Buy Cost") || strings.Contains(csvStrA, "Buy Currency") {
		t.Errorf("LEAK: Customer export file contains buy cost column! Content: %s", csvStrA)
	}

	// 4. Platform Admin exports messages: DOES contain provider ID and buy cost
	jobPlat, err := svc.SubmitJob(ctx, tcPlat, export.CreateJobRequest{
		ExportType: export.TypeMessages,
		Format:     export.FormatCSV,
	})
	if err != nil {
		t.Fatalf("SubmitJob Platform: %v", err)
	}
	svc.ProcessJobSync(ctx, jobPlat.ID, tcPlat)

	filePathPlat, _, _, err := svc.DownloadJob(ctx, tcPlat, jobPlat.ID)
	if err != nil {
		t.Fatalf("Platform DownloadJob: %v", err)
	}
	contentPlat, err := os.ReadFile(filePathPlat)
	if err != nil {
		t.Fatalf("read Platform export file: %v", err)
	}
	csvStrPlat := string(contentPlat)
	if !strings.Contains(csvStrPlat, "Provider ID") || !strings.Contains(csvStrPlat, "Actual Buy Cost") {
		t.Errorf("Platform export file should contain Provider ID and Buy Cost headers! Content: %s", csvStrPlat)
	}
}

// TestStage9_HTTPRoutesAndRBAC verifies routing, HTTP status codes, and authorization checks.
func TestStage9_HTTPRoutesAndRBAC(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	var adminUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&adminUserID)
	if err != nil {
		t.Fatalf("fetch user: %v", err)
	}

	storageDir := filepath.Join(os.TempDir(), fmt.Sprintf("znero_http_exports_%d", time.Now().UnixNano()))
	defer os.RemoveAll(storageDir)

	repo := export.NewRepository(pool, nil)
	svc := export.NewService(repo, storageDir, nil)
	handler := export.NewHandler(svc)

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(nil),
	})

	mockAuth := func(tc *tenant.Context) fiber.Handler {
		return func(c *fiber.Ctx) error {
			if tc == nil {
				return httpx.Unauthorized("unauthorized")
			}
			c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
			return c.Next()
		}
	}

	tcPlat := &tenant.Context{
		ActorUserID:     adminUserID,
		ActorOrgID:      platID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"analytics.export.create": true,
			"analytics.export.view":   true,
		},
	}

	v1 := app.Group("/api/v1")
	handler.Register(v1, mockAuth(tcPlat))

	// 1. Submit Job via HTTP POST /api/v1/exports
	bodyReq := export.CreateJobRequest{
		ExportType: export.TypeMessages,
		Format:     export.FormatCSV,
	}
	bodyBytes, _ := json.Marshal(bodyReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exports", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("POST /api/v1/exports failed: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /api/v1/exports status = %d, want 202 Accepted", resp.StatusCode)
	}

	var createResp struct {
		Data struct {
			Job *export.ExportJob `json:"job"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Data.Job == nil {
		t.Fatalf("create response missing job")
	}
	createdJobID := createResp.Data.Job.ID

	// 2. List Jobs via HTTP GET /api/v1/exports
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/exports?limit=10", nil)
	listResp, err := app.Test(listReq, 5000)
	if err != nil {
		t.Fatalf("GET /api/v1/exports failed: %v", err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/exports status = %d, want 200", listResp.StatusCode)
	}

	// 3. Get Job via HTTP GET /api/v1/exports/:id
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/exports/%s", createdJobID), nil)
	getResp, err := app.Test(getReq, 5000)
	if err != nil {
		t.Fatalf("GET /api/v1/exports/:id failed: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/exports/:id status = %d, want 200", getResp.StatusCode)
	}

	// 4. Download Job while still pending: insert an explicitly pending job
	pendingJobID := uuid.New()
	_ = repo.CreateJob(ctx, &export.ExportJob{
		ID:             pendingJobID,
		OrganizationID: platID,
		UserID:         adminUserID,
		ExportType:     export.TypeMessages,
		Format:         export.FormatCSV,
		Status:         export.StatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	dlReqPending := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/exports/%s/download", pendingJobID), nil)
	dlRespPending, err := app.Test(dlReqPending, 5000)
	if err != nil {
		t.Fatalf("GET /api/v1/exports/:id/download failed: %v", err)
	}
	if dlRespPending.StatusCode != http.StatusBadRequest {
		t.Errorf("download pending job status = %d, want 400", dlRespPending.StatusCode)
	}

	// 5. Download non-existent job -> 404
	dlReqMissing := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/exports/%s/download", uuid.New()), nil)
	dlRespMissing, err := app.Test(dlReqMissing, 5000)
	if err != nil {
		t.Fatalf("GET /api/v1/exports/missing/download failed: %v", err)
	}
	if dlRespMissing.StatusCode != http.StatusNotFound {
		t.Errorf("download missing job status = %d, want 404", dlRespMissing.StatusCode)
	}

	// 6. Download completed job -> 200 OK
	svc.ProcessJobSync(ctx, createdJobID, tcPlat)

	dlReqCompleted := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/exports/%s/download", createdJobID), nil)
	dlRespCompleted, err := app.Test(dlReqCompleted, 5000)
	if err != nil {
		t.Fatalf("GET /api/v1/exports/:id/download completed failed: %v", err)
	}
	if dlRespCompleted.StatusCode != http.StatusOK {
		t.Errorf("download completed job status = %d, want 200", dlRespCompleted.StatusCode)
	}
}
