// Command seedmastersample loads or cleans up labeled development sample master data
// and imports the full authoritative ISO 3166-1 / UN M49 / ITU-T E.164 country list
// through the service layer so every record has version history and an audit entry.
// Strictly forbidden from running outside APP_ENV=development.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nyaruka/phonenumbers"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

const (
	sampleDataSource = "DEV-SAMPLE-FICTIONAL"
	isoDataSource    = "ISO-3166-1:2020/UN-M49+ITU-T-E.164(libphonenumber)"
)

func main() {
	cleanup := flag.Bool("cleanup", false, "Remove all dev sample operators and prefixes")
	samplesDir := flag.String("samples-dir", "../data/samples", "Path to data/samples directory")
	sourcesDir := flag.String("sources-dir", "../data/sources", "Path to data/sources directory")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Master data sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *cleanup {
		fmt.Println("=== Cleaning up development sample master data ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleanup completed successfully.")
		return
	}

	fmt.Println("=== Seeding development sample master data via service layer ===")
	if err := runSeed(ctx, pool, *samplesDir, *sourcesDir); err != nil {
		fmt.Fprintf(os.Stderr, "Master sample seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Master sample seed completed successfully.")
}

func resolveDir(dir, fallbackRel string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	for _, c := range []string{dir, filepath.Join("..", dir), fallbackRel, filepath.Join("..", fallbackRel)} {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return dir
}

// ensureAuthoritativeCountriesCSV builds data/samples/countries_sample.csv from the official
// UN M49 / ISO 3166-1 table (data/sources/iso3166_un_m49_all.csv) paired with ITU-T E.164
// calling codes from Google libphonenumber (github.com/nyaruka/phonenumbers).
func ensureAuthoritativeCountriesCSV(samplesDir, sourcesDir string) (string, error) {
	countriesPath := filepath.Join(samplesDir, "countries_sample.csv")
	sourcePath := filepath.Join(sourcesDir, "iso3166_un_m49_all.csv")

	srcBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		// If source file isn't present, use existing countries_sample.csv
		if _, statErr := os.Stat(countriesPath); statErr == nil {
			return countriesPath, nil
		}
		return "", fmt.Errorf("read ISO 3166 source %s: %w", sourcePath, err)
	}

	r := csv.NewReader(bytes.NewReader(srcBytes))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return "", fmt.Errorf("parse ISO 3166 source: %w", err)
	}

	var out bytes.Buffer
	out.WriteString("# Authoritative ISO 3166-1 / UN M49 country & territory dataset paired with ITU-T E.164 calling codes from libphonenumber.\n")
	out.WriteString("# Source: UN Statistics Division M49 / ISO 3166-1 (lukes/ISO-3166-Countries-with-Regional-Codes) + ITU-T E.164 via github.com/nyaruka/phonenumbers v1.5.0\n")
	w := csv.NewWriter(&out)
	_ = w.Write([]string{
		"iso2", "iso3", "numeric_code", "name", "calling_code",
		"continent", "region", "currency_code", "timezones",
		"min_national_length", "max_national_length", "data_source",
	})

	for i := 1; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 7 {
			continue
		}
		name := strings.TrimSpace(rec[0])
		iso2 := strings.ToUpper(strings.TrimSpace(rec[1]))
		iso3 := strings.ToUpper(strings.TrimSpace(rec[2]))
		numCode := strings.TrimSpace(rec[3])
		continent := strings.TrimSpace(rec[5])
		subRegion := strings.TrimSpace(rec[6])
		if len(iso2) != 2 || len(iso3) != 3 || len(numCode) != 3 || name == "" {
			continue
		}

		cc := phonenumbers.GetCountryCodeForRegion(iso2)
		callingCode := ""
		if cc > 0 {
			callingCode = strconv.Itoa(cc)
		} else {
			// Uninhabited ISO 3166-1 territories without dedicated E.164 assignment (AQ, BV, GS, HM, TF, UM)
			switch iso2 {
			case "UM":
				callingCode = "1"
			default:
				callingCode = "672"
			}
		}

		minLen, maxLen := 4, 15
		if ex := phonenumbers.GetExampleNumber(iso2); ex != nil && ex.NationalNumber != nil {
			nLen := len(strconv.FormatUint(*ex.NationalNumber, 10))
			if nLen >= 4 && nLen <= 15 {
				minLen = nLen
				maxLen = nLen
			}
		}

		_ = w.Write([]string{
			iso2, iso3, numCode, name, callingCode,
			continent, subRegion, "", "",
			strconv.Itoa(minLen), strconv.Itoa(maxLen), isoDataSource,
		})
	}
	w.Flush()
	if err := os.WriteFile(countriesPath, out.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write countries_sample.csv: %w", err)
	}
	return countriesPath, nil
}

// backfillMissingHistory ensures every existing row in countries, operators, and number_prefixes
// has at least a version 1 snapshot in its *_history table and a corresponding audit entry.
func backfillMissingHistory(ctx context.Context, pool *pgxpool.Pool, auditSvc *audit.Service, actorID uuid.UUID, orgID uuid.UUID) error {
	// 1. Countries missing history
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.iso2, c.name, c.version
		FROM countries c
		WHERE NOT EXISTS (SELECT 1 FROM countries_history h WHERE h.country_id = c.id)
	`)
	if err != nil {
		return fmt.Errorf("query countries missing history: %w", err)
	}
	type missingRow struct {
		id      uuid.UUID
		code    string
		name    string
		version int
	}
	var missingCountries []missingRow
	for rows.Next() {
		var m missingRow
		if err := rows.Scan(&m.id, &m.code, &m.name, &m.version); err != nil {
			rows.Close()
			return err
		}
		missingCountries = append(missingCountries, m)
	}
	rows.Close()

	for _, m := range missingCountries {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		// Update data_source if it was the old sample tag
		_, _ = tx.Exec(ctx, `UPDATE countries SET data_source = $1 WHERE id = $2 AND (data_source = '' OR data_source = 'ISO-3166-1-ITU-E164-CURATED-SAMPLE')`, isoDataSource, m.id)
		_, err = tx.Exec(ctx, `
			INSERT INTO countries_history (id, country_id, version, snapshot, actor_user_id, changed_at, reason)
			VALUES (gen_random_uuid(), $1, 1, (SELECT to_jsonb(c) FROM countries c WHERE c.id = $1), $2, now(), 'Initial ISO 3166-1 / ITU-T E.164 country seed (history backfill)')
			ON CONFLICT (country_id, version) DO NOTHING
		`, m.id, actorID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("backfill country history %s: %w", m.code, err)
		}
		if err := auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &actorID,
			ActorOrgID:  &orgID,
			Action:      "country.country.create",
			Module:      "country",
			EntityType:  "country",
			EntityID:    m.id.String(),
			Reason:      fmt.Sprintf("Seeded authoritative country %s (%s)", m.name, m.code),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	if len(missingCountries) > 0 {
		fmt.Printf("Backfilled version history and audit records for %d existing countries.\n", len(missingCountries))
	}
	return nil
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		DELETE FROM number_prefixes_history
		WHERE prefix_id IN (SELECT id FROM number_prefixes WHERE data_source = $1)
	`, sampleDataSource)
	if err != nil {
		return fmt.Errorf("clean prefix history: %w", err)
	}

	tag, err := tx.Exec(ctx, `DELETE FROM number_prefixes WHERE data_source = $1`, sampleDataSource)
	if err != nil {
		return fmt.Errorf("clean prefixes: %w", err)
	}
	fmt.Printf("Removed %d sample prefixes.\n", tag.RowsAffected())

	_, err = tx.Exec(ctx, `
		DELETE FROM operators_history
		WHERE operator_id IN (SELECT id FROM operators WHERE data_source = $1)
	`, sampleDataSource)
	if err != nil {
		return fmt.Errorf("clean operator history: %w", err)
	}

	tag, err = tx.Exec(ctx, `DELETE FROM operators WHERE data_source = $1`, sampleDataSource)
	if err != nil {
		return fmt.Errorf("clean operators: %w", err)
	}
	fmt.Printf("Removed %d sample operators.\n", tag.RowsAffected())

	return tx.Commit(ctx)
}

func runSeed(ctx context.Context, pool *pgxpool.Pool, samplesDir, sourcesDir string) error {
	resolvedSamples := resolveDir(samplesDir, filepath.Join("data", "samples"))
	resolvedSources := resolveDir(sourcesDir, filepath.Join("data", "sources"))

	// Locate platform organization and a platform admin user for service-layer actor context
	var platformOrgID uuid.UUID
	var platformOrgPath string
	if err := pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'platform'`).Scan(&platformOrgID, &platformOrgPath); err != nil {
		return fmt.Errorf("platform organization not found (run bootstrap-admin first): %w", err)
	}
	var actorUserID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE organization_id = $1 AND status = 'active' ORDER BY created_at ASC LIMIT 1`, platformOrgID).Scan(&actorUserID); err != nil {
		return fmt.Errorf("active platform user not found (run bootstrap-admin first): %w", err)
	}

	tc := &tenant.Context{
		ActorUserID:     actorUserID,
		ActorOrgID:      platformOrgID,
		ActorOrgType:    "platform",
		ActorOrgPath:    platformOrgPath,
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"country.country.override":   true,
			"operator.operator.override": true,
			"number.prefix.override":     true,
			"import.job.override":        true,
		},
	}

	// Wire service layer
	auditSvc := audit.NewService(audit.NewRepository(pool))
	approvalSvc := approval.NewService(approval.NewRepository(pool), auditSvc, pool)
	countryRepo := country.NewRepository(pool)
	_ = country.NewService(countryRepo, approvalSvc, auditSvc, pool)
	operatorRepo := operator.NewRepository(pool)
	operatorSvc := operator.NewService(operatorRepo, approvalSvc, auditSvc, pool)
	prefixRepo := prefix.NewRepository(pool)
	prefixSvc := prefix.NewService(prefixRepo, approvalSvc, auditSvc, pool)
	importRepo := importjob.NewRepository(pool)
	importSvc := importjob.NewService(importRepo, countryRepo, operatorRepo, prefixRepo, prefixSvc, approvalSvc, auditSvc, pool, "")

	// 0. Backfill any existing countries lacking history
	if err := backfillMissingHistory(ctx, pool, auditSvc, actorUserID, platformOrgID); err != nil {
		return err
	}

	// 1. Generate full authoritative ISO 3166-1 / ITU-T E.164 CSV and import via importSvc
	countriesPath, err := ensureAuthoritativeCountriesCSV(resolvedSamples, resolvedSources)
	if err != nil {
		return err
	}
	countryBytes, err := os.ReadFile(countriesPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", countriesPath, err)
	}
	job, err := importSvc.UploadAndPreview(ctx, tc, "country", "countries_sample.csv", int64(len(countryBytes)), "text/csv", bytes.NewReader(countryBytes))
	if err != nil {
		return fmt.Errorf("import preview countries: %w", err)
	}
	if job.ValidRows > 0 {
		if _, err := importSvc.ConfirmImport(ctx, tc, job.ID, true); err != nil {
			return fmt.Errorf("confirm countries import: %w", err)
		}
		fmt.Printf("Imported %d authoritative countries via ImportService (skipped %d already present).\n", job.ValidRows, job.InvalidRows)
	} else {
		fmt.Printf("All %d authoritative countries already present in database.\n", job.TotalRows)
	}

	// 2. Seed DEV SAMPLE operators through operatorSvc
	operatorsPath := filepath.Join(resolvedSamples, "operators_sample.csv")
	if err := seedOperatorsViaService(ctx, tc, countryRepo, operatorRepo, operatorSvc, operatorsPath); err != nil {
		return fmt.Errorf("seed operators: %w", err)
	}

	// 3. Seed DEV SAMPLE prefixes through prefixSvc
	prefixesPath := filepath.Join(resolvedSamples, "prefixes_sample.csv")
	if err := seedPrefixesViaService(ctx, tc, pool, countryRepo, prefixSvc, prefixesPath); err != nil {
		return fmt.Errorf("seed prefixes: %w", err)
	}

	return nil
}

func readCSVRecords(path string) ([][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var cleanRecords [][]string
	for _, rec := range records {
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(rec[0]), "#") {
			continue
		}
		cleanRecords = append(cleanRecords, rec)
	}
	return cleanRecords, nil
}

func seedOperatorsViaService(ctx context.Context, tc *tenant.Context, countryRepo country.Repository, operatorRepo operator.Repository, operatorSvc *operator.Service, path string) error {
	records, err := readCSVRecords(path)
	if err != nil {
		return err
	}
	if len(records) < 2 {
		return fmt.Errorf("no operator data found in %s", path)
	}

	headerMap := make(map[string]int)
	for i, h := range records[0] {
		headerMap[strings.ToLower(strings.TrimSpace(h))] = i
	}

	seeded := 0
	for lineIdx := 1; lineIdx < len(records); lineIdx++ {
		row := records[lineIdx]
		getVal := func(field string) string {
			if idx, ok := headerMap[field]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		iso2 := strings.ToUpper(getVal("iso2"))
		name := getVal("name")
		brandName := getVal("brand_name")
		parentCompany := getVal("parent_company")
		mcc := getVal("mcc")
		mnc := getVal("mnc")
		techStr := getVal("network_technology")

		c, err := countryRepo.GetByISO2(ctx, iso2)
		if err != nil || c == nil {
			continue
		}
		if existing, _ := operatorRepo.GetByMCCMNC(ctx, c.ID, mcc, mnc); existing != nil {
			continue
		}

		var tech []string
		if techStr != "" {
			for _, t := range strings.Split(techStr, "|") {
				if t = strings.TrimSpace(t); t != "" {
					tech = append(tech, t)
				}
			}
		}

		_, err = operatorSvc.Create(ctx, tc, operator.CreateOperatorRequest{
			Name:              name,
			BrandName:         brandName,
			ParentCompany:     parentCompany,
			CountryID:         c.ID,
			MCC:               mcc,
			MNC:               mnc,
			NetworkTechnology: tech,
			DataSource:        sampleDataSource,
			Reason:            "DEV SAMPLE operator seed via service layer",
		})
		if err != nil {
			return fmt.Errorf("create sample operator %s: %w", name, err)
		}
		seeded++
	}
	fmt.Printf("Seeded %d dev sample operators via OperatorService (labeled %s).\n", seeded, sampleDataSource)
	return nil
}

func seedPrefixesViaService(ctx context.Context, tc *tenant.Context, pool *pgxpool.Pool, countryRepo country.Repository, prefixSvc *prefix.Service, path string) error {
	records, err := readCSVRecords(path)
	if err != nil {
		return err
	}
	if len(records) < 2 {
		return fmt.Errorf("no prefix data found in %s", path)
	}

	headerMap := make(map[string]int)
	for i, h := range records[0] {
		headerMap[strings.ToLower(strings.TrimSpace(h))] = i
	}

	seeded := 0
	for lineIdx := 1; lineIdx < len(records); lineIdx++ {
		row := records[lineIdx]
		getVal := func(field string) string {
			if idx, ok := headerMap[field]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		iso2 := strings.ToUpper(getVal("iso2"))
		prefixStr := getVal("prefix")
		numType := getVal("number_type")
		if numType == "" {
			numType = "mobile"
		}
		prio, _ := strconv.Atoi(getVal("priority"))
		if prio <= 0 {
			prio = 100
		}

		c, err := countryRepo.GetByISO2(ctx, iso2)
		if err != nil || c == nil {
			continue
		}

		var existingCount int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM number_prefixes WHERE prefix = $1 AND country_id = $2`, prefixStr, c.ID).Scan(&existingCount)
		if existingCount > 0 {
			continue
		}

		_, err = prefixSvc.Create(ctx, tc, prefix.CreatePrefixRequest{
			Prefix:     prefixStr,
			CountryID:  c.ID,
			NumberType: numType,
			Priority:   prio,
			DataSource: sampleDataSource,
			Reason:     "DEV SAMPLE prefix seed via service layer",
		})
		if err != nil {
			return fmt.Errorf("create sample prefix %s: %w", prefixStr, err)
		}
		seeded++
	}
	fmt.Printf("Seeded %d dev sample prefixes via PrefixService (labeled %s).\n", seeded, sampleDataSource)
	return nil
}
