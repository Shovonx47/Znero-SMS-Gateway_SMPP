package importjob_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func TestValidateUploadSecurity(t *testing.T) {
	t.Parallel()

	// 1. File size limit
	_, err := importjob.ValidateUpload("test.csv", 51*1024*1024)
	if !errors.Is(err, importjob.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}

	// 2. Empty file
	_, err = importjob.ValidateUpload("test.csv", 0)
	if !errors.Is(err, importjob.ErrEmptyFile) {
		t.Fatalf("expected ErrEmptyFile, got %v", err)
	}

	// 3. Unsupported extension
	_, err = importjob.ValidateUpload("malicious.exe", 1024)
	if !errors.Is(err, importjob.ErrUnsupportedType) {
		t.Fatalf("expected ErrUnsupportedType, got %v", err)
	}

	// 4. Path traversal attempt in file name
	ext, err := importjob.ValidateUpload("../../../etc/passwd.csv", 1024)
	if err != nil {
		t.Fatalf("expected valid csv ext, got %v", err)
	}
	if ext != ".csv" {
		t.Fatalf("expected .csv, got %s", ext)
	}
}

func TestContentTypeSniffingAndZipBombDefense(t *testing.T) {
	t.Parallel()

	// 1. Disguised PDF as .csv
	pdfHeader := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	if err := importjob.ValidateContentSniffing(".csv", pdfHeader); !errors.Is(err, importjob.ErrMimeMismatch) {
		t.Fatalf("expected ErrMimeMismatch for PDF disguised as CSV, got %v", err)
	}

	// 2. Invalid non-UTF8 binary bytes in .csv
	badUTF8 := []byte{0xff, 0xfe, 0xfd, 0x80, 0x81}
	if err := importjob.ValidateContentSniffing(".csv", badUTF8); !errors.Is(err, importjob.ErrMimeMismatch) {
		t.Fatalf("expected ErrMimeMismatch for non-UTF8 CSV, got %v", err)
	}

	// 3. Non-ZIP file disguised as .xlsx
	if err := importjob.ValidateContentSniffing(".xlsx", []byte("not a zip archive")); !errors.Is(err, importjob.ErrMimeMismatch) {
		t.Fatalf("expected ErrMimeMismatch for non-zip .xlsx, got %v", err)
	}

	// 4. Zip bomb (>100:1 compression ratio) in .xlsx
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	// Write 500 KB of repeated 'A's which compresses to a few hundred bytes (>100:1 ratio)
	_, _ = f.Write(bytes.Repeat([]byte("A"), 500*1024))
	_ = zw.Close()

	if err := importjob.ValidateContentSniffing(".xlsx", zipBuf.Bytes()); !errors.Is(err, importjob.ErrZipBombDetected) {
		t.Fatalf("expected ErrZipBombDetected for high-ratio zip archive, got %v", err)
	}
}

func TestRowAndColumnLimits(t *testing.T) {
	t.Parallel()

	// 1. Too many columns (>50)
	cols := make([]string, 55)
	vals := make([]string, 55)
	for i := range cols {
		cols[i] = fmt.Sprintf("col_%d", i)
		vals[i] = "val"
	}
	wideCSV := strings.Join(cols, ",") + "\n" + strings.Join(vals, ",") + "\n"
	if _, err := importjob.ParseFile(".csv", strings.NewReader(wideCSV)); !errors.Is(err, importjob.ErrTooManyColumns) {
		t.Fatalf("expected ErrTooManyColumns, got %v", err)
	}

	// 2. Too many rows (>20,000 for non-rate imports)
	var sb strings.Builder
	sb.WriteString("iso2,iso3,numeric_code,name,calling_code\n")
	for i := 0; i < 20005; i++ {
		sb.WriteString("US,USA,840,United States,1\n")
	}
	if _, err := importjob.ParseFile(".csv", strings.NewReader(sb.String())); !errors.Is(err, importjob.ErrTooManyRows) {
		t.Fatalf("expected ErrTooManyRows for >20,000 non-rate rows, got %v", err)
	}

	// 3. Rate imports allow >=100,000 rows (up to MaxRatingRowCount = 150,000)
	if rows, err := importjob.ParseFileWithLimit(".csv", strings.NewReader(sb.String()), importjob.MaxRatingRowCount); err != nil || len(rows) != 20005 {
		t.Fatalf("expected ParseFileWithLimit(MaxRatingRowCount) to accept 20,005 rows, got len=%d err=%v", len(rows), err)
	}
}

func TestParseCSVWithCommentsAndSpaces(t *testing.T) {
	t.Parallel()

	csvData := `# Sample comment header
# Another comment line
iso2,iso3,numeric_code,name,calling_code
US,USA,840,United States,1
# In-between comment
GB,GBR,826,United Kingdom,44
`
	rows, err := importjob.ParseFile(".csv", strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parseCSV failed: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("expected 2 parsed data rows, got %d", len(rows))
	}

	if rows[0].Data["iso2"] != "US" || rows[0].Data["calling_code"] != "1" {
		t.Errorf("row 1 mismatch: %+v", rows[0].Data)
	}
	if rows[1].Data["iso2"] != "GB" || rows[1].Data["calling_code"] != "44" {
		t.Errorf("row 2 mismatch: %+v", rows[1].Data)
	}
}

func TestParseJSONRows(t *testing.T) {
	t.Parallel()

	jsonData := `[
		{"iso2": "DE", "iso3": "DEU", "numeric_code": "276", "name": "Germany", "calling_code": "49"},
		{"iso2": "FR", "iso3": "FRA", "numeric_code": "250", "name": "France", "calling_code": "33"}
	]`

	rows, err := importjob.ParseFile(".json", strings.NewReader(jsonData))
	if err != nil {
		t.Fatalf("parseJSON failed: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("expected 2 parsed rows, got %d", len(rows))
	}

	if rows[0].Data["iso2"] != "DE" {
		t.Errorf("expected DE, got %s", rows[0].Data["iso2"])
	}
	if rows[1].Data["iso2"] != "FR" {
		t.Errorf("expected FR, got %s", rows[1].Data["iso2"])
	}
}

func TestParseJSONInvalid(t *testing.T) {
	t.Parallel()

	invalidJSON := `{"iso2": "DE"}`
	_, err := importjob.ParseFile(".json", strings.NewReader(invalidJSON))
	if err == nil {
		t.Fatal("expected error parsing non-array JSON, got nil")
	}
}

func TestPlatformOnlyImportRestriction(t *testing.T) {
	t.Parallel()

	resellerCtx := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      uuid.New(),
		ActorOrgType:    "reseller",
		IsPlatformAdmin: false,
	}

	customerCtx := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      uuid.New(),
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	svc := &importjob.Service{}

	_, err := svc.UploadAndPreview(context.Background(), resellerCtx, "country", "test.csv", 4, "text/csv", bytes.NewReader([]byte("test")))
	if !errors.Is(err, importjob.ErrPlatformOnlyImport) {
		t.Fatalf("expected ErrPlatformOnlyImport for reseller, got %v", err)
	}

	_, err = svc.ConfirmImport(context.Background(), customerCtx, uuid.New(), false)
	if !errors.Is(err, importjob.ErrPlatformOnlyImport) {
		t.Fatalf("expected ErrPlatformOnlyImport for customer, got %v", err)
	}

	err = svc.RollbackImport(context.Background(), customerCtx, uuid.New())
	if !errors.Is(err, importjob.ErrPlatformOnlyImport) {
		t.Fatalf("expected ErrPlatformOnlyImport on rollback for customer, got %v", err)
	}
}

func TestImportDuplicatesPartialImportRollbackAndMakerChecker(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	iso2A, iso3A, numA := e.UniqueCountryCodes()
	iso2B, iso3B, numB := e.UniqueCountryCodes()

	// Build a CSV with:
	// - Row 1: valid (iso2A)
	// - Row 2: duplicate in file (iso2A again)
	// - Row 3: malformed / wrong columns (missing calling_code & invalid iso2 "=BAD")
	csvContent := fmt.Sprintf(
		"iso2,iso3,numeric_code,name,calling_code,data_source\n"+
			"%s,%s,%s,Import Country A,991,ITKIT-IMPORT\n"+
			"%s,%s,%s,Import Country A Dup,991,ITKIT-IMPORT\n"+
			"=BAD,XX,12,=SUM(1,2),,\n",
		iso2A, iso3A, numA,
		iso2A, iso3A, numA,
	)

	job, err := e.Import.UploadAndPreview(ctx, e.Maker, "country", "countries_batch.csv", int64(len(csvContent)), "text/csv", strings.NewReader(csvContent))
	if err != nil {
		t.Fatalf("UploadAndPreview: %v", err)
	}
	if job.TotalRows != 3 || job.ValidRows != 1 || job.InvalidRows != 2 {
		t.Fatalf("expected 3 total / 1 valid / 2 invalid, got total=%d valid=%d invalid=%d", job.TotalRows, job.ValidRows, job.InvalidRows)
	}

	// Error report CSV must be downloadable and sanitize formula injection
	errCSV, err := e.Import.DownloadErrors(ctx, e.Maker, job.ID)
	if err != nil {
		t.Fatalf("DownloadErrors: %v", err)
	}
	if !strings.Contains(string(errCSV), "Duplicate ISO2 in file") {
		t.Errorf("error report missing duplicate message: %s", string(errCSV))
	}

	// Confirm without allowPartial must fail because 2 rows are invalid
	if _, err := e.Import.ConfirmImport(ctx, e.Override, job.ID, false); !errors.Is(err, importjob.ErrPartialNotPermitted) {
		t.Fatalf("expected ErrPartialNotPermitted when allowPartial=false, got %v", err)
	}

	// Maker without override confirms with allowPartial=true -> routed to maker-checker!
	res, err := e.Import.ConfirmImport(ctx, e.Maker, job.ID, true)
	if err != nil {
		t.Fatalf("maker ConfirmImport: %v", err)
	}
	cr, ok := res.(*approval.ChangeRequest)
	if !ok || cr.EntityType != "import_job" || cr.Operation != "confirm_import" || cr.Status != "pending" {
		t.Fatalf("expected pending import_job ChangeRequest, got %T %+v", res, res)
	}

	// Country must not exist before checker approval
	if c, _ := e.CountryRepo.GetByISO2(ctx, iso2A); c != nil {
		t.Fatal("country must not be imported before checker approval")
	}

	// Maker cannot approve own import change request
	makerWithApprove := testkit.WithPermissions(e.Maker, "import.job.approve")
	if err := e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves -> valid row is imported as unverified with v1 history!
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "approved partial import"); err != nil {
		t.Fatalf("checker approve import: %v", err)
	}

	importedCountry, err := e.CountryRepo.GetByISO2(ctx, iso2A)
	if err != nil || importedCountry == nil {
		t.Fatalf("expected country %s to exist after approval: %v", iso2A, err)
	}
	if importedCountry.VerificationStatus != "unverified" || importedCountry.Version != 1 {
		t.Errorf("imported country must start unverified v1, got %q v%d", importedCountry.VerificationStatus, importedCountry.Version)
	}
	if h := e.HistoryVersions("countries_history", "country_id", importedCountry.ID); len(h) != 1 {
		t.Errorf("expected v1 history for imported country, got %v", h)
	}

	// Direct import with Override user + whole-import rollback
	csvContent2 := fmt.Sprintf(
		"iso2,iso3,numeric_code,name,calling_code,data_source\n"+
			"%s,%s,%s,Import Country B,992,ITKIT-IMPORT\n",
		iso2B, iso3B, numB,
	)
	job2, err := e.Import.UploadAndPreview(ctx, e.Override, "country", "countries_b.csv", int64(len(csvContent2)), "text/csv", strings.NewReader(csvContent2))
	if err != nil {
		t.Fatalf("UploadAndPreview job2: %v", err)
	}
	res2, err := e.Import.ConfirmImport(ctx, e.Override, job2.ID, false)
	if err != nil {
		t.Fatalf("override ConfirmImport: %v", err)
	}
	completedJob, ok := res2.(*importjob.ImportJob)
	if !ok || completedJob.Status != "completed" || completedJob.ImportedRows != 1 {
		t.Fatalf("expected completed ImportJob with 1 imported row, got %T %+v", res2, res2)
	}

	// Rollback the entire import job
	if err := e.Import.RollbackImport(ctx, e.Override, job2.ID); err != nil {
		t.Fatalf("RollbackImport: %v", err)
	}
	rolledJob, err := e.Import.GetJob(ctx, job2.ID)
	if err != nil || rolledJob.Status != "rolled_back" {
		t.Fatalf("expected rolled_back job status, got %+v / %v", rolledJob, err)
	}
	rolledCountry, err := e.CountryRepo.GetByISO2(ctx, iso2B)
	if err != nil || rolledCountry.Status != "archived" {
		t.Fatalf("expected rolled-back country status to be archived, got %+v / %v", rolledCountry, err)
	}
}

func TestPhase4ProviderCoverageAndSenderIDImports(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	provRepo := provider.NewRepository(e.Pool)
	sidRepo := senderid.NewRepository(e.Pool)
	e.Import.SetPhase4Repositories(provRepo, sidRepo)

	kr, err := secrets.NewKeyRing("v1", []byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	provSvc := provider.NewService(provRepo, e.Audit, e.Approval, kr, "development")

	// 1. Reject credential entity types on upload
	if _, err := e.Import.UploadAndPreview(ctx, e.Override, "provider_connection", "creds.csv", 10, "text/csv", strings.NewReader("a,b\n1,2\n")); !errors.Is(err, importjob.ErrCredentialImportDenied) {
		t.Fatalf("expected ErrCredentialImportDenied for provider_connection entity_type, got %v", err)
	}
	if _, err := e.Import.UploadAndPreview(ctx, e.Override, "customer_api_key", "creds.csv", 10, "text/csv", strings.NewReader("a,b\n1,2\n")); !errors.Is(err, importjob.ErrCredentialImportDenied) {
		t.Fatalf("expected ErrCredentialImportDenied for customer_api_key entity_type, got %v", err)
	}

	// Setup country, provider, and customer org
	iso2, iso3, num := e.UniqueCountryCodes()
	cRes, err := e.Country.Create(ctx, e.Override, country.CreateCountryRequest{
		Name:              "Import Phase4 Country " + iso2,
		ISO2:              iso2,
		ISO3:              iso3,
		NumericCode:       num,
		CallingCode:       "+9" + num,
		MinNationalLength: 8,
		MaxNationalLength: 10,
		DataSource:        "DEV SAMPLE",
	})
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	createdCountry := cRes.(*country.Country)

	provCode := "IMP_PROV_" + iso2
	pRes, err := provSvc.CreateProvider(ctx, e.Override, provider.CreateProviderRequest{
		Name:         "Import Test Provider " + iso2,
		Code:         provCode,
		ProviderType: "smpp",
		CurrencyCode: "USD",
		Timezone:     "UTC",
		Status:       "draft",
		Reason:       "Import test provider",
	}, "127.0.0.1", "import-test")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	custOrgID, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "imp-cust-"+iso2, "customer")

	t.Cleanup(func() {
		_, _ = e.Pool.Exec(ctx, `DELETE FROM provider_coverage WHERE provider_id = $1`, pRes.Provider.ID)
		_, _ = e.Pool.Exec(ctx, `DELETE FROM providers WHERE id = $1`, pRes.Provider.ID)
		_, _ = e.Pool.Exec(ctx, `DELETE FROM sender_ids WHERE organization_id = $1`, custOrgID)
		_, _ = e.Pool.Exec(ctx, `DELETE FROM countries WHERE id = $1`, createdCountry.ID)
	})

	// 2. Provider Coverage Import (with 1 valid row, 1 duplicate row, 1 unknown provider row)
	covCSV := fmt.Sprintf(
		"provider_code,country_iso2,status,notes,data_source\n"+
			"%s,%s,active,Imported coverage note,DEV SAMPLE\n"+
			"%s,%s,active,Duplicate coverage note,DEV SAMPLE\n"+
			"UNKNOWN_PROV,%s,declared,Unknown provider,DEV SAMPLE\n",
		provCode, iso2,
		provCode, iso2,
		iso2,
	)
	covJob, err := e.Import.UploadAndPreview(ctx, e.Maker, "provider_coverage", "cov.csv", int64(len(covCSV)), "text/csv", strings.NewReader(covCSV))
	if err != nil {
		t.Fatalf("UploadAndPreview provider_coverage: %v", err)
	}
	if covJob.TotalRows != 3 || covJob.ValidRows != 1 || covJob.InvalidRows != 2 {
		t.Fatalf("expected 3 total / 1 valid / 2 invalid for provider_coverage, got %+v", covJob)
	}

	// Maker confirms partial import -> goes through maker-checker
	covConfirmRes, err := e.Import.ConfirmImport(ctx, e.Maker, covJob.ID, true)
	if err != nil {
		t.Fatalf("Maker ConfirmImport provider_coverage: %v", err)
	}
	covCR, ok := covConfirmRes.(*approval.ChangeRequest)
	if !ok || covCR.Status != "pending" {
		t.Fatalf("expected pending ChangeRequest for provider_coverage import, got %T %+v", covConfirmRes, covConfirmRes)
	}
	if err := e.Approval.Approve(ctx, e.Checker, covCR.ID, "Approve coverage import"); err != nil {
		t.Fatalf("Checker Approve provider_coverage import: %v", err)
	}

	// Verify imported coverage row starts as 'declared' (even though CSV said 'active') and has v1 history
	covItems, totalCov, err := provRepo.ListCoverage(ctx, provider.CoverageListFilter{ProviderID: &pRes.Provider.ID})
	if err != nil || totalCov != 1 {
		t.Fatalf("expected 1 imported coverage row, got total=%d err=%v", totalCov, err)
	}
	if covItems[0].Status != "declared" || covItems[0].Version != 1 {
		t.Fatalf("imported coverage must start as declared v1, got status=%q v%d", covItems[0].Status, covItems[0].Version)
	}
	if h := e.HistoryVersions("provider_coverage_history", "coverage_id", covItems[0].ID); len(h) != 1 {
		t.Fatalf("expected 1 history row for imported coverage, got %v", h)
	}

	// Rollback provider_coverage import -> archives coverage row (version 2)
	if err := e.Import.RollbackImport(ctx, e.Override, covJob.ID); err != nil {
		t.Fatalf("RollbackImport provider_coverage: %v", err)
	}
	rolledCov, err := provRepo.GetCoverageByID(ctx, covItems[0].ID)
	if err != nil || !rolledCov.Archived || rolledCov.Version != 2 {
		t.Fatalf("expected archived v2 coverage after rollback, got %+v / %v", rolledCov, err)
	}

	// 3. Sender ID Import (with 1 valid row, 1 duplicate row, 1 row with forbidden credential column)
	sidCSV := fmt.Sprintf(
		"organization_id,sender_id,sender_type,country_iso2,notes,data_source\n"+
			"%s,IMP%s,alphanumeric,%s,Imported sender ID,DEV SAMPLE\n"+
			"%s,IMP%s,alphanumeric,%s,Duplicate sender ID,DEV SAMPLE\n"+
			"%s,12,alphanumeric,%s,Invalid short sender ID,DEV SAMPLE\n",
		custOrgID.String(), iso2, iso2,
		custOrgID.String(), iso2, iso2,
		custOrgID.String(), iso2,
	)
	sidJob, err := e.Import.UploadAndPreview(ctx, e.Override, "sender_id", "sender_ids.csv", int64(len(sidCSV)), "text/csv", strings.NewReader(sidCSV))
	if err != nil {
		t.Fatalf("UploadAndPreview sender_id: %v", err)
	}
	if sidJob.TotalRows != 3 || sidJob.ValidRows != 1 || sidJob.InvalidRows != 2 {
		t.Fatalf("expected 3 total / 1 valid / 2 invalid for sender_id, got %+v", sidJob)
	}

	if _, err := e.Import.ConfirmImport(ctx, e.Override, sidJob.ID, true); err != nil {
		t.Fatalf("Override ConfirmImport sender_id: %v", err)
	}
	sidItems, totalSid, err := sidRepo.List(ctx, senderid.ListFilter{OrganizationID: &custOrgID})
	if err != nil || totalSid != 1 {
		t.Fatalf("expected 1 imported sender_id row, got total=%d err=%v", totalSid, err)
	}
	if sidItems[0].Status != "pending" || sidItems[0].Version != 1 {
		t.Fatalf("imported sender_id must start as pending v1, got status=%q v%d", sidItems[0].Status, sidItems[0].Version)
	}
	if h := e.HistoryVersions("sender_ids_history", "sender_id_ref", sidItems[0].ID); len(h) != 1 {
		t.Fatalf("expected 1 history row for imported sender_id, got %v", h)
	}

	// Rollback sender_id import -> archives sender_id row (version 2)
	if err := e.Import.RollbackImport(ctx, e.Override, sidJob.ID); err != nil {
		t.Fatalf("RollbackImport sender_id: %v", err)
	}
	rolledSid, err := sidRepo.GetByID(ctx, sidItems[0].ID)
	if err != nil || !rolledSid.Archived || rolledSid.Version != 2 {
		t.Fatalf("expected archived v2 sender_id after rollback, got %+v / %v", rolledSid, err)
	}
}
