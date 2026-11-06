package importjob

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrPlatformOnlyImport     = errors.New("import operations are restricted to platform administrators")
	ErrInvalidJobStatus       = errors.New("job is not in a valid state for this action")
	ErrPartialNotPermitted    = errors.New("cannot import file with validation errors unless allow_partial is enabled")
	ErrNoValidRows            = errors.New("file contains no valid rows to import")
	ErrCredentialImportDenied = errors.New("connection credentials and secrets cannot be imported via file upload")
)

type Service struct {
	repo         Repository
	countryRepo  country.Repository
	operatorRepo operator.Repository
	prefixRepo   prefix.Repository
	prefixSvc    *prefix.Service
	providerRepo provider.Repository
	senderIDRepo senderid.Repository
	ratingRepo   rating.Repository
	ratingSvc    *rating.Service
	approvalSvc  *approval.Service
	auditSvc     *audit.Service
	pool         *pgxpool.Pool
	uploadDir    string
}

func (s *Service) SetPhase4Repositories(providerRepo provider.Repository, senderIDRepo senderid.Repository) {
	s.providerRepo = providerRepo
	s.senderIDRepo = senderIDRepo
}

func (s *Service) SetRatingServices(ratingRepo rating.Repository, ratingSvc *rating.Service) {
	s.ratingRepo = ratingRepo
	s.ratingSvc = ratingSvc
}

func NewService(
	repo Repository,
	countryRepo country.Repository,
	operatorRepo operator.Repository,
	prefixRepo prefix.Repository,
	prefixSvc *prefix.Service,
	approvalSvc *approval.Service,
	auditSvc *audit.Service,
	pool *pgxpool.Pool,
	uploadDir string,
) *Service {
	if uploadDir == "" {
		uploadDir = filepath.Join("data", "uploads")
	}
	_ = os.MkdirAll(uploadDir, 0755)

	svc := &Service{
		repo:         repo,
		countryRepo:  countryRepo,
		operatorRepo: operatorRepo,
		prefixRepo:   prefixRepo,
		prefixSvc:    prefixSvc,
		approvalSvc:  approvalSvc,
		auditSvc:     auditSvc,
		pool:         pool,
		uploadDir:    uploadDir,
	}

	if approvalSvc != nil {
		approvalSvc.RegisterApplier("import_job", svc)
		approvalSvc.RegisterApprovePermission("import_job", "import.job.approve")
	}

	return svc
}

func (s *Service) ListJobs(ctx context.Context, f Filter) (ListResult, error) {
	items, total, err := s.repo.ListJobs(ctx, f)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{
		Items:      items,
		TotalCount: total,
		Page:       f.Page,
		PageSize:   f.PageSize,
	}, nil
}

func (s *Service) GetJob(ctx context.Context, id uuid.UUID) (*ImportJob, error) {
	return s.repo.GetJobByID(ctx, id)
}

func (s *Service) ListJobRows(ctx context.Context, jobID uuid.UUID, statusFilter string, page, pageSize int) (RowListResult, error) {
	items, total, err := s.repo.ListJobRows(ctx, jobID, statusFilter, page, pageSize)
	if err != nil {
		return RowListResult{}, err
	}
	return RowListResult{
		Items:      items,
		TotalCount: total,
		Page:       page,
		PageSize:   pageSize,
	}, nil
}

func (s *Service) UploadAndPreview(
	ctx context.Context,
	tc *tenant.Context,
	entityType, fileName string,
	fileSize int64,
	contentType string,
	content io.Reader,
) (*ImportJob, error) {
	entityType = strings.ToLower(strings.TrimSpace(entityType))
	switch entityType {
	case "provider_connection", "customer_api_key", "customer_smpp_account", "credentials", "secret", "secrets":
		return nil, ErrCredentialImportDenied
	case "country", "operator", "prefix", "provider_coverage", "sender_id", "rate_card_rate":
		if !tc.IsPlatformOrg() {
			return nil, ErrPlatformOnlyImport
		}
	case "customer_sell_rate":
		if !tc.IsPlatformOrg() && !tc.IsResellerOrg() {
			return nil, ErrPlatformOnlyImport
		}
	default:
		return nil, fmt.Errorf("invalid entity type %q: must be country, operator, prefix, provider_coverage, sender_id, rate_card_rate, or customer_sell_rate", entityType)
	}

	ext, err := ValidateUpload(fileName, fileSize)
	if err != nil {
		return nil, err
	}

	jobID := uuid.New()
	safeFileName := fmt.Sprintf("%s_%d%s", jobID.String(), time.Now().Unix(), ext)
	storedPath := filepath.Join(s.uploadDir, safeFileName)

	fileBytes, err := io.ReadAll(io.LimitReader(content, MaxFileSize+1024))
	if err != nil {
		return nil, fmt.Errorf("read uploaded file: %w", err)
	}
	if int64(len(fileBytes)) > MaxFileSize {
		return nil, ErrFileTooLarge
	}

	if err := ValidateContentSniffing(ext, fileBytes); err != nil {
		return nil, err
	}

	if err := os.WriteFile(storedPath, fileBytes, 0600); err != nil {
		return nil, fmt.Errorf("write stored file: %w", err)
	}

	// Parse file into rows
	maxRows := MaxRowCount
	if entityType == "rate_card_rate" || entityType == "customer_sell_rate" {
		maxRows = MaxRatingRowCount
	}
	parsedRows, err := ParseFileWithLimit(ext, bytes.NewReader(fileBytes), maxRows)
	if err != nil {
		return nil, fmt.Errorf("parse file error: %w", err)
	}

	// Validate & Detect conflicts against file and database
	validatedRows, validCount, invalidCount, errSummary := s.validateRows(ctx, tc, entityType, parsedRows, fileName)

	job := ImportJob{
		ID:           jobID,
		EntityType:   entityType,
		FileName:     fileName,
		FileSize:     int64(len(fileBytes)),
		ContentType:  contentType,
		StoredPath:   storedPath,
		Status:       "preview_ready",
		TotalRows:    len(validatedRows),
		ValidRows:    validCount,
		InvalidRows:  invalidCount,
		AllowPartial: false,
		ErrorSummary: errSummary,
		ActorUserID:  tc.ActorUserID,
	}

	createdJob, err := s.repo.CreateJob(ctx, job)
	if err != nil {
		return nil, err
	}

	// Attach job ID to rows and bulk insert
	for i := range validatedRows {
		validatedRows[i].JobID = createdJob.ID
	}
	if err := s.repo.CreateJobRows(ctx, validatedRows); err != nil {
		return nil, fmt.Errorf("create job preview rows: %w", err)
	}

	_ = s.auditSvc.Record(ctx, nil, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.import.preview", entityType),
		Module:      "import",
		EntityType:  "import_job",
		EntityID:    createdJob.ID.String(),
		Reason:      fmt.Sprintf("Uploaded import file %q (%d total rows, %d valid, %d invalid)", fileName, len(validatedRows), validCount, invalidCount),
	})

	return createdJob, nil
}

func (s *Service) validateRows(ctx context.Context, tc *tenant.Context, entityType string, parsed []ParsedRow, fileName string) ([]ImportJobRow, int, int, json.RawMessage) {
	var jobRows []ImportJobRow
	validCount := 0
	invalidCount := 0
	errorMap := make(map[string]int)

	switch entityType {
	case "country":
		seenISO2 := make(map[string]int)
		seenISO3 := make(map[string]int)
		seenNum := make(map[string]int)

		for _, r := range parsed {
			rawJSON, _ := json.Marshal(r.Data)
			var rowErrs []RowError

			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			iso3 := strings.ToUpper(strings.TrimSpace(r.Data["iso3"]))
			numCode := strings.TrimSpace(r.Data["numeric_code"])
			name := strings.TrimSpace(r.Data["name"])
			callingCode := strings.TrimSpace(r.Data["calling_code"])

			if len(iso2) != 2 {
				rowErrs = append(rowErrs, RowError{Field: "iso2", Message: "ISO2 code must be exactly 2 characters"})
			}
			if len(iso3) != 3 {
				rowErrs = append(rowErrs, RowError{Field: "iso3", Message: "ISO3 code must be exactly 3 characters"})
			}
			if len(numCode) != 3 {
				rowErrs = append(rowErrs, RowError{Field: "numeric_code", Message: "Numeric code must be exactly 3 digits"})
			}
			if name == "" {
				rowErrs = append(rowErrs, RowError{Field: "name", Message: "Country name is required"})
			}
			if callingCode == "" {
				rowErrs = append(rowErrs, RowError{Field: "calling_code", Message: "Calling code is required"})
			}

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				// Duplicate check in file
				if prev, exists := seenISO2[iso2]; exists {
					status = "duplicate"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "iso2", Message: fmt.Sprintf("Duplicate ISO2 in file (already in row %d)", prev)})
					errorMap["iso2_duplicate_in_file"]++
				} else if prev, exists := seenISO3[iso3]; exists {
					status = "duplicate"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "iso3", Message: fmt.Sprintf("Duplicate ISO3 in file (already in row %d)", prev)})
					errorMap["iso3_duplicate_in_file"]++
				} else {
					seenISO2[iso2] = r.RowNumber
					seenISO3[iso3] = r.RowNumber
					seenNum[numCode] = r.RowNumber

					// Conflict check in DB
					if existing, _ := s.countryRepo.GetByISO2(ctx, iso2); existing != nil {
						status = "conflict"
						invalidCount++
						rowErrs = append(rowErrs, RowError{Field: "iso2", Message: fmt.Sprintf("Country with ISO2 %s already exists in database", iso2)})
						errorMap["iso2_db_conflict"]++
					} else {
						validCount++
					}
				}
			}

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   rawJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}

	case "operator":
		seenKey := make(map[string]int)

		for _, r := range parsed {
			rawJSON, _ := json.Marshal(r.Data)
			var rowErrs []RowError

			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			mcc := strings.TrimSpace(r.Data["mcc"])
			mnc := strings.TrimSpace(r.Data["mnc"])
			name := strings.TrimSpace(r.Data["name"])

			if len(iso2) != 2 {
				rowErrs = append(rowErrs, RowError{Field: "iso2", Message: "Country ISO2 code must be 2 characters"})
			}
			if len(mcc) != 3 {
				rowErrs = append(rowErrs, RowError{Field: "mcc", Message: "MCC must be 3 digits"})
			}
			if len(mnc) < 2 || len(mnc) > 3 {
				rowErrs = append(rowErrs, RowError{Field: "mnc", Message: "MNC must be 2 or 3 digits"})
			}
			if name == "" {
				rowErrs = append(rowErrs, RowError{Field: "name", Message: "Operator name is required"})
			}

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				// Check country exists in DB
				c, err := s.countryRepo.GetByISO2(ctx, iso2)
				if err != nil || c == nil {
					status = "invalid"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "iso2", Message: fmt.Sprintf("Referenced country ISO2 %s not found in database", iso2)})
					errorMap["country_not_found"]++
				} else {
					key := fmt.Sprintf("%s:%s:%s", iso2, mcc, mnc)
					if prev, exists := seenKey[key]; exists {
						status = "duplicate"
						invalidCount++
						rowErrs = append(rowErrs, RowError{Field: "mcc_mnc", Message: fmt.Sprintf("Duplicate MCC/MNC in file (already in row %d)", prev)})
						errorMap["mcc_mnc_duplicate_in_file"]++
					} else {
						seenKey[key] = r.RowNumber
						// Conflict check in DB
						if existing, _ := s.operatorRepo.GetByMCCMNC(ctx, c.ID, mcc, mnc); existing != nil {
							status = "conflict"
							invalidCount++
							rowErrs = append(rowErrs, RowError{Field: "mcc_mnc", Message: fmt.Sprintf("Operator with MCC %s and MNC %s already exists for %s in DB", mcc, mnc, iso2)})
							errorMap["operator_db_conflict"]++
						} else {
							validCount++
						}
					}
				}
			}

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   rawJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}

	case "prefix":
		seenPrefix := make(map[string]int)

		for _, r := range parsed {
			rawJSON, _ := json.Marshal(r.Data)
			var rowErrs []RowError

			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			pref := strings.TrimSpace(r.Data["prefix"])

			if len(iso2) != 2 {
				rowErrs = append(rowErrs, RowError{Field: "iso2", Message: "Country ISO2 code must be 2 characters"})
			}
			if pref == "" {
				rowErrs = append(rowErrs, RowError{Field: "prefix", Message: "Prefix string is required"})
			}

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				c, err := s.countryRepo.GetByISO2(ctx, iso2)
				if err != nil || c == nil {
					status = "invalid"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "iso2", Message: fmt.Sprintf("Country ISO2 %s not found", iso2)})
					errorMap["country_not_found"]++
				} else {
					if prev, exists := seenPrefix[pref]; exists {
						status = "duplicate"
						invalidCount++
						rowErrs = append(rowErrs, RowError{Field: "prefix", Message: fmt.Sprintf("Duplicate prefix in file (already in row %d)", prev)})
						errorMap["prefix_duplicate_in_file"]++
					} else {
						seenPrefix[pref] = r.RowNumber
						validCount++
					}
				}
			}

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   rawJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}

	case "provider_coverage":
		seenKey := make(map[string]int)

		for _, r := range parsed {
			rawJSON, _ := json.Marshal(r.Data)
			var rowErrs []RowError

			if hasForbiddenCredentialColumns(r.Data) {
				rowErrs = append(rowErrs, RowError{Field: "credentials", Message: ErrCredentialImportDenied.Error()})
			}

			provCode := strings.ToUpper(strings.TrimSpace(r.Data["provider_code"]))
			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			}
			mcc := strings.TrimSpace(r.Data["operator_mcc"])
			mnc := strings.TrimSpace(r.Data["operator_mnc"])

			if provCode == "" {
				rowErrs = append(rowErrs, RowError{Field: "provider_code", Message: "provider_code is required"})
			}
			if len(iso2) != 2 {
				rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: "country_iso2 must be 2 characters"})
			}
			if (mcc != "" && mnc == "") || (mcc == "" && mnc != "") {
				rowErrs = append(rowErrs, RowError{Field: "operator_mcc", Message: "Both operator_mcc and operator_mnc must be provided when specifying an operator"})
			}

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				var p *provider.Provider
				if s.providerRepo != nil {
					p, _ = s.providerRepo.GetProviderByCode(ctx, provCode)
				}
				c, _ := s.countryRepo.GetByISO2(ctx, iso2)
				if p == nil {
					status = "invalid"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "provider_code", Message: fmt.Sprintf("Provider code %s not found", provCode)})
					errorMap["provider_not_found"]++
				} else if c == nil {
					status = "invalid"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: fmt.Sprintf("Country ISO2 %s not found", iso2)})
					errorMap["country_not_found"]++
				} else {
					var opID *uuid.UUID
					opFailed := false
					if mcc != "" && mnc != "" {
						op, _ := s.operatorRepo.GetByMCCMNC(ctx, c.ID, mcc, mnc)
						if op == nil {
							status = "invalid"
							invalidCount++
							rowErrs = append(rowErrs, RowError{Field: "operator_mcc", Message: fmt.Sprintf("Operator MCC %s / MNC %s not found for country %s", mcc, mnc, iso2)})
							errorMap["operator_not_found"]++
							opFailed = true
						} else {
							opID = &op.ID
						}
					}
					if !opFailed {
						key := fmt.Sprintf("%s:%s:%s:%s", provCode, iso2, mcc, mnc)
						if prev, exists := seenKey[key]; exists {
							status = "duplicate"
							invalidCount++
							rowErrs = append(rowErrs, RowError{Field: "provider_code", Message: fmt.Sprintf("Duplicate provider coverage target in file (already in row %d)", prev)})
							errorMap["coverage_duplicate_in_file"]++
						} else {
							seenKey[key] = r.RowNumber
							var existing int
							_ = s.pool.QueryRow(ctx, `
								SELECT 1 FROM provider_coverage
								WHERE provider_id = $1 AND country_id = $2
								  AND COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($3::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
								p.ID, c.ID, opID,
							).Scan(&existing)
							if existing == 1 {
								status = "conflict"
								invalidCount++
								rowErrs = append(rowErrs, RowError{Field: "provider_code", Message: "Provider coverage already exists in database for this target"})
								errorMap["coverage_db_conflict"]++
							} else {
								validCount++
							}
						}
					}
				}
			}

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   rawJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}

	case "sender_id":
		seenKey := make(map[string]int)

		for _, r := range parsed {
			rawJSON, _ := json.Marshal(r.Data)
			var rowErrs []RowError

			if hasForbiddenCredentialColumns(r.Data) {
				rowErrs = append(rowErrs, RowError{Field: "credentials", Message: ErrCredentialImportDenied.Error()})
			}

			orgIDStr := strings.TrimSpace(r.Data["organization_id"])
			orgName := strings.TrimSpace(r.Data["organization_name"])
			sidVal := strings.TrimSpace(r.Data["sender_id"])
			sidType := strings.ToLower(strings.TrimSpace(r.Data["sender_type"]))
			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			}

			normalizedSid, err := senderid.ValidateSenderIDFormat(sidVal, sidType)
			if err != nil {
				rowErrs = append(rowErrs, RowError{Field: "sender_id", Message: err.Error()})
			}
			if orgIDStr == "" && orgName == "" {
				rowErrs = append(rowErrs, RowError{Field: "organization_name", Message: "organization_id or organization_name is required"})
			}
			if iso2 != "" && len(iso2) != 2 {
				rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: "country_iso2 must be 2 characters"})
			}

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				orgID, orgErr := s.resolveCustomerOrg(ctx, orgIDStr, orgName)
				if orgErr != nil {
					status = "invalid"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "organization_name", Message: orgErr.Error()})
					errorMap["organization_not_found"]++
				} else {
					var countryID *uuid.UUID
					countryFailed := false
					if iso2 != "" {
						c, _ := s.countryRepo.GetByISO2(ctx, iso2)
						if c == nil {
							status = "invalid"
							invalidCount++
							rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: fmt.Sprintf("Country ISO2 %s not found", iso2)})
							errorMap["country_not_found"]++
							countryFailed = true
						} else {
							countryID = &c.ID
						}
					}
					if !countryFailed {
						key := fmt.Sprintf("%s:%s:%s", orgID.String(), strings.ToUpper(normalizedSid), iso2)
						if prev, exists := seenKey[key]; exists {
							status = "duplicate"
							invalidCount++
							rowErrs = append(rowErrs, RowError{Field: "sender_id", Message: fmt.Sprintf("Duplicate sender_id in file (already in row %d)", prev)})
							errorMap["sender_id_duplicate_in_file"]++
						} else {
							seenKey[key] = r.RowNumber
							var existing int
							_ = s.pool.QueryRow(ctx, `
								SELECT 1 FROM sender_ids
								WHERE organization_id = $1 AND UPPER(sender_id) = UPPER($2)
								  AND COALESCE(country_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($3::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
								orgID, normalizedSid, countryID,
							).Scan(&existing)
							if existing == 1 {
								status = "conflict"
								invalidCount++
								rowErrs = append(rowErrs, RowError{Field: "sender_id", Message: "Sender ID already exists in database for this organization and country"})
								errorMap["sender_id_db_conflict"]++
							} else {
								validCount++
							}
						}
					}
				}
			}

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   rawJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}

	case "rate_card_rate", "customer_sell_rate":
		seenKey := make(map[string]int)
		cardCache := make(map[string]*rating.RateCard)
		countryCache := make(map[string]*country.Country)
		operatorCache := make(map[string]*operator.Operator)
		activeCardRates := make(map[string]*rating.Rate)
		lowestBuyRates := make(map[string]decimal.Decimal)

		thresholdPct := rating.GetRateChangeThresholdPct()
		now := time.Now().UTC()

		// Pre-populate country cache if repository is available
		if s.countryRepo != nil {
			if countries, _, err := s.countryRepo.List(ctx, country.Filter{Page: 1, PageSize: 500}); err == nil {
				for i := range countries {
					countryCache[strings.ToUpper(countries[i].ISO2)] = &countries[i]
				}
			}
		}

		for _, r := range parsed {
			rawMap := make(map[string]any)
			for k, v := range r.Data {
				rawMap[k] = v
			}
			var rowErrs []RowError

			if hasForbiddenCredentialColumns(r.Data) {
				rowErrs = append(rowErrs, RowError{Field: "credentials", Message: ErrCredentialImportDenied.Error()})
			}

			// Card resolution
			cardIDStr := strings.TrimSpace(r.Data["rate_card_id"])
			if cardIDStr == "" {
				cardIDStr = strings.TrimSpace(r.Data["card_id"])
			}
			cardName := strings.TrimSpace(r.Data["rate_card_name"])

			var targetCard *rating.RateCard
			if cardIDStr != "" {
				if cached, ok := cardCache[cardIDStr]; ok {
					targetCard = cached
				} else if cid, err := uuid.Parse(cardIDStr); err == nil && s.ratingRepo != nil {
					card, err := s.ratingRepo.GetCardByID(ctx, cid)
					if err == nil && card != nil {
						targetCard = card
						cardCache[cardIDStr] = card
					}
				}
			} else if cardName != "" && s.ratingRepo != nil {
				if cached, ok := cardCache[strings.ToLower(cardName)]; ok {
					targetCard = cached
				} else {
					cards, _, err := s.ratingRepo.ListCards(ctx, rating.RateCardListFilter{
						Search:   cardName,
						PageSize: 10,
					})
					if err == nil {
						for i := range cards {
							if strings.EqualFold(cards[i].Name, cardName) {
								targetCard = &cards[i]
								cardCache[strings.ToLower(cardName)] = targetCard
								break
							}
						}
					}
				}
			}

			if targetCard == nil {
				rowErrs = append(rowErrs, RowError{Field: "rate_card_id", Message: "Rate card not found or rating repository unavailable"})
			} else {
				if targetCard.Status != "active" {
					rowErrs = append(rowErrs, RowError{Field: "rate_card_id", Message: fmt.Sprintf("Rate card %q is %s, must be active", targetCard.Name, targetCard.Status)})
				}
				if tc.IsResellerOrg() && targetCard.OwnerOrgID != tc.ActorOrgID {
					rowErrs = append(rowErrs, RowError{Field: "rate_card_id", Message: "Rate card does not belong to your reseller organization"})
				}
				if entityType == "customer_sell_rate" && targetCard.Kind != "customer_sell" {
					rowErrs = append(rowErrs, RowError{Field: "rate_card_id", Message: fmt.Sprintf("Rate card must be of kind 'customer_sell' (got %q)", targetCard.Kind)})
				}

				// Pre-load active rates for target card into cache once
				cardKey := targetCard.ID.String()
				if _, ok := cardCache[cardKey+":rates_loaded"]; !ok && s.ratingRepo != nil {
					if existingRates, err := s.ratingRepo.ListActiveRatesByCard(ctx, targetCard.ID); err == nil {
						for idx := range existingRates {
							er := &existingRates[idx]
							opStr := "nil"
							if er.OperatorID != nil {
								opStr = er.OperatorID.String()
							}
							rk := fmt.Sprintf("%s:%s:%s:%s", targetCard.ID, er.CountryID, opStr, er.MessageType)
							activeCardRates[rk] = er
						}
					}
					if targetCard.Kind == "customer_sell" {
						if lowestMap, err := s.ratingRepo.ListLowestActiveBuyRatesByCurrency(ctx, targetCard.CurrencyCode, now); err == nil {
							for k, v := range lowestMap {
								lowestBuyRates[targetCard.CurrencyCode+":"+k] = v
							}
						}
					}
					cardCache[cardKey+":rates_loaded"] = targetCard
				}
			}

			// Country resolution
			iso2 := strings.ToUpper(strings.TrimSpace(r.Data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(r.Data["iso2"]))
			}
			var targetCountry *country.Country
			if iso2 == "" {
				rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: "country_iso2 is required"})
			} else {
				if cached, ok := countryCache[iso2]; ok {
					targetCountry = cached
				} else if s.countryRepo != nil {
					c, err := s.countryRepo.GetByISO2(ctx, iso2)
					if err == nil && c != nil {
						targetCountry = c
						countryCache[iso2] = c
					}
				}
				if targetCountry == nil {
					rowErrs = append(rowErrs, RowError{Field: "country_iso2", Message: fmt.Sprintf("Country ISO2 %s not found", iso2)})
					errorMap["unknown_country_operator"]++
				}
			}

			// Operator resolution (optional, country default if missing)
			var targetOpID *uuid.UUID
			mcc := strings.TrimSpace(r.Data["operator_mcc"])
			mnc := strings.TrimSpace(r.Data["operator_mnc"])
			opIDStr := strings.TrimSpace(r.Data["operator_id"])
			if opIDStr != "" {
				if parsedID, err := uuid.Parse(opIDStr); err == nil {
					targetOpID = &parsedID
				} else {
					rowErrs = append(rowErrs, RowError{Field: "operator_id", Message: "Invalid operator_id UUID"})
				}
			} else if mcc != "" && mnc != "" && targetCountry != nil {
				opKey := fmt.Sprintf("%s:%s:%s", targetCountry.ID, mcc, mnc)
				if cached, ok := operatorCache[opKey]; ok {
					targetOpID = &cached.ID
				} else if s.operatorRepo != nil {
					op, err := s.operatorRepo.GetByMCCMNC(ctx, targetCountry.ID, mcc, mnc)
					if err == nil && op != nil {
						targetOpID = &op.ID
						operatorCache[opKey] = op
					} else {
						rowErrs = append(rowErrs, RowError{Field: "operator_mcc", Message: fmt.Sprintf("Operator with MCC %s / MNC %s not found in %s", mcc, mnc, iso2)})
						errorMap["unknown_country_operator"]++
					}
				}
			}

			// Message type (normalize "" and "sms" to "default" to satisfy rates_message_type_check constraint)
			msgType := strings.ToLower(strings.TrimSpace(r.Data["message_type"]))
			if msgType == "" {
				msgType = strings.ToLower(strings.TrimSpace(r.Data["msg_type"]))
			}
			if msgType == "" || msgType == "sms" {
				msgType = "default"
			}
			switch msgType {
			case "default", "transactional", "promotional", "otp":
				// allowed
			default:
				rowErrs = append(rowErrs, RowError{Field: "message_type", Message: fmt.Sprintf("Invalid message_type %q: must be default, transactional, promotional, or otp", msgType)})
			}

			// Price decimal validation
			priceStr := strings.TrimSpace(r.Data["price"])
			if priceStr == "" {
				priceStr = strings.TrimSpace(r.Data["rate"])
			}
			if priceStr == "" {
				priceStr = strings.TrimSpace(r.Data["cost"])
			}
			var parsedPrice decimal.Decimal
			if priceStr == "" {
				rowErrs = append(rowErrs, RowError{Field: "price", Message: "price/rate is required"})
			} else {
				d, err := decimal.NewFromString(priceStr)
				if err != nil || d.LessThanOrEqual(decimal.Zero) {
					rowErrs = append(rowErrs, RowError{Field: "price", Message: "Price must be a valid positive decimal number"})
				} else {
					parsedPrice = rating.TruncateMoney(d)
				}
			}

			// Dates
			effFrom := parseOptionalTime(r.Data["effective_from"])
			if effFrom == nil {
				effFrom = &now
			}
			effTo := parseOptionalTime(r.Data["effective_to"])
			if effTo != nil && !effTo.After(*effFrom) {
				rowErrs = append(rowErrs, RowError{Field: "effective_to", Message: "effective_to must be after effective_from"})
			}

			changeType := "new"
			if effTo != nil {
				changeType = "closing"
			}
			thresholdFlagged := false
			thresholdDirection := ""
			var oldPriceStr string
			var pctChangeStr string

			status := "valid"
			if len(rowErrs) > 0 {
				status = "invalid"
				invalidCount++
				for _, e := range rowErrs {
					errorMap[e.Field]++
				}
			} else {
				// In-file duplicate check
				opStr := "nil"
				if targetOpID != nil {
					opStr = targetOpID.String()
				}
				key := fmt.Sprintf("%s:%s:%s:%s:%s", targetCard.ID, targetCountry.ID, opStr, msgType, effFrom.Format(time.RFC3339))
				if prev, exists := seenKey[key]; exists {
					status = "duplicate"
					invalidCount++
					rowErrs = append(rowErrs, RowError{Field: "price", Message: fmt.Sprintf("Duplicate rate in file (already in row %d)", prev)})
					errorMap["rate_duplicate_in_file"]++
				} else {
					seenKey[key] = r.RowNumber

					// Check existing rate for destination
					rk := fmt.Sprintf("%s:%s:%s:%s", targetCard.ID, targetCountry.ID, opStr, msgType)
					prevRate := activeCardRates[rk]
					if prevRate != nil && !prevRate.Price.IsZero() {
						oldPriceStr = prevRate.Price.StringFixed(6)
						if !prevRate.Price.Equal(parsedPrice) {
							if changeType != "closing" {
								changeType = "changed"
							}
							diff := parsedPrice.Sub(prevRate.Price)
							pct := diff.Div(prevRate.Price).Mul(decimal.NewFromInt(100)).Truncate(2)
							pctChangeStr = pct.StringFixed(2)
							if diff.GreaterThan(decimal.Zero) {
								thresholdDirection = "increase"
							} else {
								thresholdDirection = "decrease"
							}
							if pct.Abs().GreaterThan(thresholdPct) {
								thresholdFlagged = true
								errorMap["rate_change_threshold_exceeded"]++
							}
						}
					}

					// Margin checks for customer sell cards
					if targetCard.Kind == "customer_sell" {
						lbKey := fmt.Sprintf("%s:%s:%s:%s", targetCard.CurrencyCode, targetCountry.ID, opStr, msgType)
						if lowestBuy, ok := lowestBuyRates[lbKey]; ok {
							if parsedPrice.LessThan(lowestBuy) {
								status = "conflict"
								invalidCount++
								changeType = "margin_violation"
								rowErrs = append(rowErrs, RowError{
									Field: "price",
									Message: fmt.Sprintf("Margin violation: Sell price %s is below lowest provider buy cost %s",
										parsedPrice.StringFixed(6), lowestBuy.StringFixed(6)),
								})
								errorMap["margin_violation"]++
							}
						}

						if tc.IsResellerOrg() && s.ratingRepo != nil {
							resellerCost, err := s.ratingRepo.GetResellerCostRateAt(ctx, tc.ActorOrgID, targetCountry.ID, targetOpID, msgType, *effFrom)
							if err == nil && resellerCost != nil && parsedPrice.LessThan(*resellerCost) {
								status = "conflict"
								invalidCount++
								changeType = "margin_violation"
								rowErrs = append(rowErrs, RowError{
									Field: "price",
									Message: fmt.Sprintf("Reseller floor violation: Sell price %s is below reseller cost floor %s",
										parsedPrice.StringFixed(6), resellerCost.StringFixed(6)),
								})
								errorMap["reseller_floor_violation"]++
							}
						}
					}

					if status == "valid" {
						validCount++
					}
				}
			}

			// Enrich preview metadata
			rawMap["change_type"] = changeType
			rawMap["new_price"] = parsedPrice.StringFixed(6)
			if oldPriceStr != "" {
				rawMap["old_price"] = oldPriceStr
				rawMap["pct_change"] = pctChangeStr
			}
			rawMap["threshold_flagged"] = thresholdFlagged
			if thresholdDirection != "" {
				rawMap["threshold_direction"] = thresholdDirection
			}
			enrichedJSON, _ := json.Marshal(rawMap)

			jobRows = append(jobRows, ImportJobRow{
				RowNumber: r.RowNumber,
				RawData:   enrichedJSON,
				Status:    status,
				Errors:    rowErrs,
			})
		}
	}

	summaryBytes, _ := json.Marshal(errorMap)
	return jobRows, validCount, invalidCount, summaryBytes
}

func hasForbiddenCredentialColumns(data map[string]string) bool {
	for k, v := range data {
		if strings.TrimSpace(v) == "" {
			continue
		}
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "password" || lk == "smpp_password" || lk == "http_credentials" || lk == "api_key" || lk == "secret" {
			return true
		}
	}
	return false
}

func (s *Service) resolveCustomerOrg(ctx context.Context, orgIDStr, orgName string) (uuid.UUID, error) {
	var id uuid.UUID
	var orgType string
	if orgIDStr != "" {
		parsedID, err := uuid.Parse(orgIDStr)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid organization_id UUID %q", orgIDStr)
		}
		if err := s.pool.QueryRow(ctx, `SELECT id, type FROM organizations WHERE id = $1`, parsedID).Scan(&id, &orgType); err != nil {
			return uuid.Nil, fmt.Errorf("organization %s not found", orgIDStr)
		}
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT id, type FROM organizations WHERE LOWER(name) = LOWER($1)`, orgName).Scan(&id, &orgType); err != nil {
			return uuid.Nil, fmt.Errorf("organization %q not found", orgName)
		}
	}
	if orgType != "customer" {
		return uuid.Nil, fmt.Errorf("organization must be of type 'customer' (got %q)", orgType)
	}
	return id, nil
}

func (s *Service) ConfirmImport(ctx context.Context, tc *tenant.Context, jobID uuid.UUID, allowPartial bool) (any, error) {
	if !tc.IsPlatformOrg() {
		if !(tc.IsResellerOrg() && (tc.HasPermission("rate.sellrate.create") || tc.HasPermission("rate.import.confirm"))) {
			return nil, ErrPlatformOnlyImport
		}
	}

	job, err := s.repo.GetJobByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.Status != "preview_ready" {
		return nil, ErrInvalidJobStatus
	}

	if !tc.IsPlatformOrg() && tc.IsResellerOrg() && job.EntityType != "customer_sell_rate" {
		return nil, ErrPlatformOnlyImport
	}

	if !allowPartial && job.InvalidRows > 0 {
		return nil, ErrPartialNotPermitted
	}

	validRows, err := s.repo.GetJobRowsForImport(ctx, jobID, true)
	if err != nil {
		return nil, err
	}
	if len(validRows) == 0 {
		return nil, ErrNoValidRows
	}

	// Check override permission
	overridePerm := "import.job.override"
	hasOverride := tc.HasPermission(overridePerm) || tc.HasPermission(job.EntityType+"."+job.EntityType+".override")
	if job.EntityType == "prefix" && tc.HasPermission("number.prefix.override") {
		hasOverride = true
	}
	if job.EntityType == "provider_coverage" && tc.HasPermission("provider.coverage.override") {
		hasOverride = true
	}
	if job.EntityType == "sender_id" && tc.HasPermission("customer.senderid.override") {
		hasOverride = true
	}
	if job.EntityType == "rate_card_rate" || job.EntityType == "customer_sell_rate" {
		if tc.HasPermission("rate.buyrate.override") || tc.HasPermission("rate.sellrate.override") || tc.HasPermission("rate.margin.override") || tc.HasPermission("import.job.override") {
			hasOverride = true
		}
	}

	if !hasOverride && s.approvalSvc != nil {
		// Collect sample diffs (up to 50) for maker-checker approver review
		type RateDiffSample struct {
			RowNumber          int    `json:"row_number"`
			Destination        string `json:"destination"`
			ChangeType         string `json:"change_type"`
			OldPrice           string `json:"old_price,omitempty"`
			NewPrice           string `json:"new_price"`
			PctChange          string `json:"pct_change,omitempty"`
			ThresholdFlagged   bool   `json:"threshold_flagged,omitempty"`
			ThresholdDirection string `json:"threshold_direction,omitempty"`
		}
		var sampleDiffs []RateDiffSample
		for _, r := range validRows {
			var d map[string]interface{}
			_ = json.Unmarshal(r.RawData, &d)
			dest := fmt.Sprintf("%v", d["country_iso2"])
			if op := fmt.Sprintf("%v", d["operator_mcc"]); op != "" && op != "<nil>" {
				dest += "/" + op
			}
			ct, _ := d["change_type"].(string)
			np, _ := d["new_price"].(string)
			op, _ := d["old_price"].(string)
			pct, _ := d["pct_change"].(string)
			tf, _ := d["threshold_flagged"].(bool)
			td, _ := d["threshold_direction"].(string)
			if len(sampleDiffs) < 50 {
				sampleDiffs = append(sampleDiffs, RateDiffSample{
					RowNumber:          r.RowNumber,
					Destination:        dest,
					ChangeType:         ct,
					OldPrice:           op,
					NewPrice:           np,
					PctChange:          pct,
					ThresholdFlagged:   tf,
					ThresholdDirection: td,
				})
			}
		}

		prop := struct {
			JobID           uuid.UUID        `json:"job_id"`
			EntityType      string           `json:"entity_type"`
			TotalRows       int              `json:"total_rows"`
			ValidRows       int              `json:"valid_rows"`
			InvalidRows     int              `json:"invalid_rows"`
			AllowPartial    bool             `json:"allow_partial"`
			Summary         json.RawMessage  `json:"summary,omitempty"`
			RateDiffSamples []RateDiffSample `json:"rate_diff_samples,omitempty"`
		}{
			JobID:           jobID,
			EntityType:      job.EntityType,
			TotalRows:       job.TotalRows,
			ValidRows:       job.ValidRows,
			InvalidRows:     job.InvalidRows,
			AllowPartial:    allowPartial,
			Summary:         job.ErrorSummary,
			RateDiffSamples: sampleDiffs,
		}
		propJSON, _ := json.Marshal(prop)

		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback(ctx)

		if err := s.repo.UpdateJobStatus(ctx, tx, jobID, "pending_approval", job.ValidRows, job.InvalidRows, 0, 0, job.ErrorSummary); err != nil {
			return nil, fmt.Errorf("update job status: %w", err)
		}

		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:   "import_job",
			EntityID:     &jobID,
			Operation:    "confirm_import",
			ProposedData: propJSON,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("submit approval request: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit tx: %w", err)
		}

		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	importedCount := 0
	skippedCount := job.InvalidRows

	switch job.EntityType {
	case "country":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			minLen, _ := strconv.Atoi(data["min_national_length"])
			if minLen <= 0 {
				minLen = 4
			}
			maxLen, _ := strconv.Atoi(data["max_national_length"])
			if maxLen <= 0 {
				maxLen = 15
			}
			tzs := []string{}
			if tzStr := data["timezones"]; tzStr != "" {
				for _, t := range strings.Split(tzStr, "|") {
					if t = strings.TrimSpace(t); t != "" {
						tzs = append(tzs, t)
					}
				}
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			c := country.Country{
				Name:                    strings.TrimSpace(data["name"]),
				ISO2:                    strings.ToUpper(strings.TrimSpace(data["iso2"])),
				ISO3:                    strings.ToUpper(strings.TrimSpace(data["iso3"])),
				NumericCode:             strings.TrimSpace(data["numeric_code"]),
				CallingCode:             strings.TrimSpace(data["calling_code"]),
				Continent:               strings.TrimSpace(data["continent"]),
				Region:                  strings.TrimSpace(data["region"]),
				CurrencyCode:            strings.ToUpper(strings.TrimSpace(data["currency_code"])),
				Timezones:               tzs,
				NumberingPlanNotes:      strings.TrimSpace(data["numbering_plan_notes"]),
				MinNationalLength:       minLen,
				MaxNationalLength:       maxLen,
				SenderIDPolicyNotes:     strings.TrimSpace(data["sender_id_policy_notes"]),
				DNDPolicyNotes:          strings.TrimSpace(data["dnd_policy_notes"]),
				ConsentRequirementNotes: strings.TrimSpace(data["consent_requirement_notes"]),
				DataSource:              src,
				VerificationStatus:      "unverified", // Starts unverified as per policy
				Status:                  "active",
			}

			created, err := s.countryRepo.Create(ctx, tx, c, &tc.ActorUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return nil, fmt.Errorf("import country row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return nil, err
			}
			importedCount++
		}

	case "operator":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			c, err := s.countryRepo.GetByISO2(ctx, strings.ToUpper(strings.TrimSpace(data["iso2"])))
			if err != nil {
				return nil, fmt.Errorf("country iso2 %s not found: %w", data["iso2"], err)
			}

			tech := []string{}
			if techStr := data["network_technology"]; techStr != "" {
				for _, t := range strings.Split(techStr, "|") {
					if t = strings.TrimSpace(t); t != "" {
						tech = append(tech, t)
					}
				}
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			op := operator.Operator{
				Name:               strings.TrimSpace(data["name"]),
				BrandName:          strings.TrimSpace(data["brand_name"]),
				ParentCompany:      strings.TrimSpace(data["parent_company"]),
				CountryID:          c.ID,
				MCC:                strings.TrimSpace(data["mcc"]),
				MNC:                strings.TrimSpace(data["mnc"]),
				NetworkTechnology:  tech,
				Capabilities:       json.RawMessage("{}"),
				DataSource:         src,
				VerificationStatus: "unverified",
				Status:             "active",
			}

			created, err := s.operatorRepo.Create(ctx, tx, op, &tc.ActorUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return nil, fmt.Errorf("import operator row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return nil, err
			}
			importedCount++
		}

	case "prefix":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			c, err := s.countryRepo.GetByISO2(ctx, strings.ToUpper(strings.TrimSpace(data["iso2"])))
			if err != nil {
				return nil, fmt.Errorf("country iso2 %s not found: %w", data["iso2"], err)
			}

			prio, _ := strconv.Atoi(data["priority"])
			if prio <= 0 {
				prio = 100
			}
			numType := strings.TrimSpace(data["number_type"])
			if numType == "" {
				numType = "mobile"
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			var opID *uuid.UUID
			if opStr := strings.TrimSpace(data["operator_id"]); opStr != "" {
				if id, err := uuid.Parse(opStr); err == nil {
					opID = &id
				}
			}

			np := prefix.NumberPrefix{
				Prefix:     strings.TrimSpace(data["prefix"]),
				CountryID:  c.ID,
				OperatorID: opID,
				NumberType: numType,
				Priority:   prio,
				DataSource: src,
				Status:     "active",
			}

			created, err := s.prefixRepo.Create(ctx, tx, np, &tc.ActorUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return nil, fmt.Errorf("import prefix row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return nil, err
			}
			importedCount++
		}

	case "provider_coverage", "sender_id":
		cnt, err := s.importPhase4Rows(ctx, tx, job, validRows, tc.ActorUserID)
		if err != nil {
			return nil, err
		}
		importedCount += cnt

	case "rate_card_rate", "customer_sell_rate":
		cnt, err := s.importRatingRows(ctx, tx, job, validRows, tc.ActorUserID)
		if err != nil {
			return nil, err
		}
		importedCount += cnt
	}

	// Update job status to completed
	if err := s.repo.UpdateJobStatus(ctx, tx, jobID, "completed", job.ValidRows, job.InvalidRows, importedCount, skippedCount, job.ErrorSummary); err != nil {
		return nil, fmt.Errorf("update job status: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.import.confirm", job.EntityType),
		Module:      "import",
		EntityType:  "import_job",
		EntityID:    jobID.String(),
		Reason:      fmt.Sprintf("Confirmed import job %s: %d imported, %d skipped", jobID, importedCount, skippedCount),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit import tx: %w", err)
	}

	if job.EntityType == "prefix" {
		defer s.prefixSvc.ListAllActive(context.Background())
	}
	if (job.EntityType == "rate_card_rate" || job.EntityType == "customer_sell_rate") && s.ratingSvc != nil {
		defer s.ratingSvc.ReloadEngine(context.Background())
	}

	return s.repo.GetJobByID(ctx, jobID)
}

func (s *Service) RollbackImport(ctx context.Context, tc *tenant.Context, jobID uuid.UUID) error {
	if !tc.IsPlatformOrg() {
		return ErrPlatformOnlyImport
	}

	job, err := s.repo.GetJobByID(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != "completed" {
		return fmt.Errorf("cannot rollback job in %s status", job.Status)
	}

	entityIDs, err := s.repo.GetImportedEntityIDs(ctx, jobID)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Archive the entities created in this import job
	reason := fmt.Sprintf("Rolled back via import job %s", jobID)
	switch job.EntityType {
	case "country":
		for _, id := range entityIDs {
			_, _ = s.countryRepo.SetStatus(ctx, tx, id, "archived", &tc.ActorUserID, reason)
		}
	case "operator":
		for _, id := range entityIDs {
			_, _ = s.operatorRepo.SetStatus(ctx, tx, id, "archived", &tc.ActorUserID, reason)
		}
	case "prefix":
		for _, id := range entityIDs {
			_, _ = s.prefixRepo.SetStatus(ctx, tx, id, "archived", &tc.ActorUserID, reason)
		}
	case "provider_coverage":
		if s.providerRepo != nil {
			for _, id := range entityIDs {
				if cov, err := s.providerRepo.GetCoverageByID(ctx, id); err == nil && cov != nil {
					cov.Archived = true
					cov.Status = "suspended"
					cov.UpdatedBy = &tc.ActorUserID
					_, _ = s.providerRepo.UpdateCoverage(ctx, tx, cov, reason)
				}
			}
		}
	case "sender_id":
		if s.senderIDRepo != nil {
			for _, id := range entityIDs {
				if sid, err := s.senderIDRepo.GetByID(ctx, id); err == nil && sid != nil {
					sid.Archived = true
					sid.Status = "suspended"
					sid.UpdatedBy = &tc.ActorUserID
					_, _ = s.senderIDRepo.Update(ctx, tx, sid, reason)
				}
			}
		}
	case "rate_card_rate", "customer_sell_rate":
		if s.ratingRepo != nil && len(entityIDs) > 0 {
			autoCloseReason := fmt.Sprintf("auto-closed by import job %s", jobID)
			if err := s.ratingRepo.RollbackImportedRates(ctx, tx, entityIDs, &tc.ActorUserID, autoCloseReason); err != nil {
				return fmt.Errorf("rollback imported rates: %w", err)
			}
		}
	}

	if err := s.repo.UpdateJobStatus(ctx, tx, jobID, "rolled_back", job.ValidRows, job.InvalidRows, 0, job.TotalRows, job.ErrorSummary); err != nil {
		return err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.import.rollback", job.EntityType),
		Module:      "import",
		EntityType:  "import_job",
		EntityID:    jobID.String(),
		Reason:      fmt.Sprintf("Rolled back import job %s (%d records archived)", jobID, len(entityIDs)),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rollback tx: %w", err)
	}

	if (job.EntityType == "rate_card_rate" || job.EntityType == "customer_sell_rate") && s.ratingSvc != nil {
		defer s.ratingSvc.ReloadEngine(context.Background())
	}

	return nil
}

func (s *Service) DownloadErrors(ctx context.Context, tc *tenant.Context, jobID uuid.UUID) ([]byte, error) {
	rows, _, err := s.repo.ListJobRows(ctx, jobID, "", 1, 10000)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	if err := w.Write([]string{"RowNumber", "Status", "Field", "ErrorMessage", "RawData"}); err != nil {
		return nil, err
	}

	for _, r := range rows {
		if len(r.Errors) == 0 {
			continue
		}
		for _, e := range r.Errors {
			row := []string{
				fmt.Sprintf("%d", r.RowNumber),
				sanitizeCSV(r.Status),
				sanitizeCSV(e.Field),
				sanitizeCSV(e.Message),
				sanitizeCSV(string(r.RawData)),
			}
			if err := w.Write(row); err != nil {
				return nil, err
			}
		}
	}
	w.Flush()

	return buf.Bytes(), nil
}

func sanitizeCSV(s string) string {
	if len(s) == 0 {
		return ""
	}
	first := s[0]
	if first == '=' || first == '+' || first == '-' || first == '@' || first == '\t' || first == '\r' {
		return "'" + s
	}
	return s
}

// ApplyChange implements approval.ChangeApplier for import jobs
func (s *Service) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var prop struct {
		JobID        uuid.UUID `json:"job_id"`
		AllowPartial bool      `json:"allow_partial"`
	}
	_ = json.Unmarshal(cr.ProposedData, &prop)
	jobID := prop.JobID
	if jobID == uuid.Nil && cr.EntityID != nil {
		jobID = *cr.EntityID
	}
	if jobID == uuid.Nil {
		return fmt.Errorf("missing job_id for import change request")
	}

	job, err := s.repo.GetJobByID(ctx, jobID)
	if err != nil {
		return err
	}

	validRows, err := s.repo.GetJobRowsForImport(ctx, jobID, true)
	if err != nil {
		return err
	}

	importedCount := 0
	skippedCount := job.InvalidRows

	switch job.EntityType {
	case "country":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			minLen, _ := strconv.Atoi(data["min_national_length"])
			if minLen <= 0 {
				minLen = 4
			}
			maxLen, _ := strconv.Atoi(data["max_national_length"])
			if maxLen <= 0 {
				maxLen = 15
			}
			tzs := []string{}
			if tzStr := data["timezones"]; tzStr != "" {
				for _, t := range strings.Split(tzStr, "|") {
					if t = strings.TrimSpace(t); t != "" {
						tzs = append(tzs, t)
					}
				}
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			c := country.Country{
				Name:                    strings.TrimSpace(data["name"]),
				ISO2:                    strings.ToUpper(strings.TrimSpace(data["iso2"])),
				ISO3:                    strings.ToUpper(strings.TrimSpace(data["iso3"])),
				NumericCode:             strings.TrimSpace(data["numeric_code"]),
				CallingCode:             strings.TrimSpace(data["calling_code"]),
				Continent:               strings.TrimSpace(data["continent"]),
				Region:                  strings.TrimSpace(data["region"]),
				CurrencyCode:            strings.ToUpper(strings.TrimSpace(data["currency_code"])),
				Timezones:               tzs,
				NumberingPlanNotes:      strings.TrimSpace(data["numbering_plan_notes"]),
				MinNationalLength:       minLen,
				MaxNationalLength:       maxLen,
				SenderIDPolicyNotes:     strings.TrimSpace(data["sender_id_policy_notes"]),
				DNDPolicyNotes:          strings.TrimSpace(data["dnd_policy_notes"]),
				ConsentRequirementNotes: strings.TrimSpace(data["consent_requirement_notes"]),
				DataSource:              src,
				VerificationStatus:      "unverified",
				Status:                  "active",
			}

			created, err := s.countryRepo.Create(ctx, tx, c, &cr.MakerUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return fmt.Errorf("import country row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return err
			}
			importedCount++
		}

	case "operator":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			c, err := s.countryRepo.GetByISO2(ctx, strings.ToUpper(strings.TrimSpace(data["iso2"])))
			if err != nil {
				return fmt.Errorf("country iso2 %s not found: %w", data["iso2"], err)
			}

			tech := []string{}
			if techStr := data["network_technology"]; techStr != "" {
				for _, t := range strings.Split(techStr, "|") {
					if t = strings.TrimSpace(t); t != "" {
						tech = append(tech, t)
					}
				}
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			op := operator.Operator{
				Name:               strings.TrimSpace(data["name"]),
				BrandName:          strings.TrimSpace(data["brand_name"]),
				ParentCompany:      strings.TrimSpace(data["parent_company"]),
				CountryID:          c.ID,
				MCC:                strings.TrimSpace(data["mcc"]),
				MNC:                strings.TrimSpace(data["mnc"]),
				NetworkTechnology:  tech,
				Capabilities:       json.RawMessage("{}"),
				DataSource:         src,
				VerificationStatus: "unverified",
				Status:             "active",
			}

			created, err := s.operatorRepo.Create(ctx, tx, op, &cr.MakerUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return fmt.Errorf("import operator row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return err
			}
			importedCount++
		}

	case "prefix":
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			c, err := s.countryRepo.GetByISO2(ctx, strings.ToUpper(strings.TrimSpace(data["iso2"])))
			if err != nil {
				return fmt.Errorf("country iso2 %s not found: %w", data["iso2"], err)
			}

			prio, _ := strconv.Atoi(data["priority"])
			if prio <= 0 {
				prio = 100
			}
			numType := strings.TrimSpace(data["number_type"])
			if numType == "" {
				numType = "mobile"
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			var opID *uuid.UUID
			if opStr := strings.TrimSpace(data["operator_id"]); opStr != "" {
				if id, err := uuid.Parse(opStr); err == nil {
					opID = &id
				}
			}

			np := prefix.NumberPrefix{
				Prefix:     strings.TrimSpace(data["prefix"]),
				CountryID:  c.ID,
				OperatorID: opID,
				NumberType: numType,
				Priority:   prio,
				DataSource: src,
				Status:     "active",
			}

			created, err := s.prefixRepo.Create(ctx, tx, np, &cr.MakerUserID, fmt.Sprintf("Imported via job %s", job.ID))
			if err != nil {
				return fmt.Errorf("import prefix row %d: %w", row.RowNumber, err)
			}

			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return err
			}
			importedCount++
		}

	case "provider_coverage", "sender_id":
		cnt, err := s.importPhase4Rows(ctx, tx, job, validRows, cr.MakerUserID)
		if err != nil {
			return err
		}
		importedCount += cnt

	case "rate_card_rate", "customer_sell_rate":
		cnt, err := s.importRatingRows(ctx, tx, job, validRows, cr.MakerUserID)
		if err != nil {
			return err
		}
		importedCount += cnt
	}

	if err := s.repo.UpdateJobStatus(ctx, tx, jobID, "completed", job.ValidRows, job.InvalidRows, importedCount, skippedCount, job.ErrorSummary); err != nil {
		return fmt.Errorf("update job status: %w", err)
	}

	if job.EntityType == "prefix" {
		defer s.prefixSvc.ListAllActive(context.Background())
	}
	if (job.EntityType == "rate_card_rate" || job.EntityType == "customer_sell_rate") && s.ratingSvc != nil {
		defer s.ratingSvc.ReloadEngine(context.Background())
	}

	return nil
}

func (s *Service) importRatingRows(ctx context.Context, tx pgx.Tx, job *ImportJob, validRows []ImportJobRow, actorUserID uuid.UUID) (int, error) {
	if s.ratingRepo == nil {
		return 0, fmt.Errorf("rating repository not configured on import service")
	}
	importedCount := 0
	reason := fmt.Sprintf("Imported via job %s", job.ID)

	cardCache := make(map[string]*rating.RateCard)
	countryCache := make(map[string]*country.Country)
	operatorCache := make(map[string]*operator.Operator)

	// Process valid rows in streaming chunks of 2500
	const batchSize = 2500
	for i := 0; i < len(validRows); i += batchSize {
		end := i + batchSize
		if end > len(validRows) {
			end = len(validRows)
		}
		chunk := validRows[i:end]

		var ratesToInsert []*rating.Rate
		var rowIDs []uuid.UUID

		for _, row := range chunk {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			// Resolve Card ID
			var cardID uuid.UUID
			cardIDStr := strings.TrimSpace(data["rate_card_id"])
			if cardIDStr == "" {
				cardIDStr = strings.TrimSpace(data["card_id"])
			}
			if cardIDStr != "" {
				if cached, ok := cardCache[cardIDStr]; ok {
					cardID = cached.ID
				} else if cid, err := uuid.Parse(cardIDStr); err == nil {
					if c, err := s.ratingRepo.GetCardByID(ctx, cid); err == nil && c != nil {
						cardID = c.ID
						cardCache[cardIDStr] = c
					}
				}
			} else if cardName := strings.TrimSpace(data["rate_card_name"]); cardName != "" {
				if cached, ok := cardCache[strings.ToLower(cardName)]; ok {
					cardID = cached.ID
				} else {
					cards, _, _ := s.ratingRepo.ListCards(ctx, rating.RateCardListFilter{Search: cardName, PageSize: 10})
					for _, c := range cards {
						if strings.EqualFold(c.Name, cardName) {
							cardID = c.ID
							cardCache[strings.ToLower(cardName)] = &c
							break
						}
					}
				}
			}
			if cardID == uuid.Nil {
				continue
			}

			// Resolve Country
			iso2 := strings.ToUpper(strings.TrimSpace(data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(data["iso2"]))
			}
			var c *country.Country
			if cached, ok := countryCache[iso2]; ok {
				c = cached
			} else if s.countryRepo != nil {
				found, err := s.countryRepo.GetByISO2(ctx, iso2)
				if err == nil && found != nil {
					c = found
					countryCache[iso2] = found
				}
			}
			if c == nil {
				continue
			}

			// Resolve Operator
			var opID *uuid.UUID
			if opStr := strings.TrimSpace(data["operator_id"]); opStr != "" {
				if parsedID, err := uuid.Parse(opStr); err == nil {
					opID = &parsedID
				}
			} else {
				mcc := strings.TrimSpace(data["operator_mcc"])
				mnc := strings.TrimSpace(data["operator_mnc"])
				if mcc != "" && mnc != "" {
					opKey := fmt.Sprintf("%s:%s:%s", c.ID, mcc, mnc)
					if cached, ok := operatorCache[opKey]; ok {
						opID = &cached.ID
					} else if s.operatorRepo != nil {
						if op, err := s.operatorRepo.GetByMCCMNC(ctx, c.ID, mcc, mnc); err == nil && op != nil {
							opID = &op.ID
							operatorCache[opKey] = op
						}
					}
				}
			}

			// Message Type
			msgType := strings.ToLower(strings.TrimSpace(data["message_type"]))
			if msgType == "" {
				msgType = strings.ToLower(strings.TrimSpace(data["msg_type"]))
			}
			if msgType == "" || msgType == "sms" {
				msgType = "default"
			}

			// Price
			priceStr := strings.TrimSpace(data["price"])
			if priceStr == "" {
				priceStr = strings.TrimSpace(data["rate"])
			}
			if priceStr == "" {
				priceStr = strings.TrimSpace(data["cost"])
			}
			price, err := decimal.NewFromString(priceStr)
			if err != nil {
				continue
			}
			price = rating.TruncateMoney(price)

			// Dates
			effFrom := parseOptionalTime(data["effective_from"])
			if effFrom == nil {
				now := time.Now().UTC()
				effFrom = &now
			}
			effTo := parseOptionalTime(data["effective_to"])

			notes := strings.TrimSpace(data["min_billing_note"])
			if notes == "" {
				notes = strings.TrimSpace(data["notes"])
			}

			rate := &rating.Rate{
				ID:             uuid.New(),
				CardID:         cardID,
				CountryID:      c.ID,
				OperatorID:     opID,
				MessageType:    msgType,
				Price:          price,
				MinBillingNote: notes,
				EffectiveFrom:  *effFrom,
				EffectiveTo:    effTo,
				Source:         "import",
				Status:         "active",
				CreatedBy:      &actorUserID,
				UpdatedBy:      &actorUserID,
			}

			ratesToInsert = append(ratesToInsert, rate)
			rowIDs = append(rowIDs, row.ID)
		}

		if len(ratesToInsert) > 0 {
			// Auto-close prior open-ended rates in same batch
			autoCloseReason := fmt.Sprintf("auto-closed by import job %s", job.ID)
			_ = s.ratingRepo.ClosePreviousOpenEndedRatesBatch(ctx, tx, ratesToInsert, &actorUserID, autoCloseReason)

			createdRates, err := s.ratingRepo.CreateRatesBatch(ctx, tx, ratesToInsert, reason)
			if err != nil {
				return importedCount, fmt.Errorf("batch insert rates: %w", err)
			}
			var createdIDs []uuid.UUID
			for _, cr := range createdRates {
				createdIDs = append(createdIDs, cr.ID)
			}
			if len(createdIDs) > 0 && len(createdIDs) == len(rowIDs) {
				_ = s.repo.UpdateRowsImportedBatch(ctx, tx, rowIDs, createdIDs)
			} else {
				for idx, cr := range createdRates {
					if idx < len(rowIDs) {
						_ = s.repo.UpdateRowImported(ctx, tx, rowIDs[idx], cr.ID)
					}
				}
			}
			importedCount += len(createdRates)
		}
	}

	return importedCount, nil
}

func (s *Service) importPhase4Rows(ctx context.Context, tx pgx.Tx, job *ImportJob, validRows []ImportJobRow, actorUserID uuid.UUID) (int, error) {
	importedCount := 0
	reason := fmt.Sprintf("Imported via job %s", job.ID)

	switch job.EntityType {
	case "provider_coverage":
		if s.providerRepo == nil {
			return 0, fmt.Errorf("provider repository not configured on import service")
		}
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			provCode := strings.ToUpper(strings.TrimSpace(data["provider_code"]))
			iso2 := strings.ToUpper(strings.TrimSpace(data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(data["iso2"]))
			}
			mcc := strings.TrimSpace(data["operator_mcc"])
			mnc := strings.TrimSpace(data["operator_mnc"])

			p, err := s.providerRepo.GetProviderByCode(ctx, provCode)
			if err != nil || p == nil {
				return 0, fmt.Errorf("provider code %s not found: %w", provCode, err)
			}
			c, err := s.countryRepo.GetByISO2(ctx, iso2)
			if err != nil || c == nil {
				return 0, fmt.Errorf("country iso2 %s not found: %w", iso2, err)
			}
			var opID *uuid.UUID
			if mcc != "" && mnc != "" {
				op, err := s.operatorRepo.GetByMCCMNC(ctx, c.ID, mcc, mnc)
				if err != nil || op == nil {
					return 0, fmt.Errorf("operator %s/%s not found for %s: %w", mcc, mnc, iso2, err)
				}
				opID = &op.ID
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			cov := provider.ProviderCoverage{
				ProviderID:    p.ID,
				CountryID:     c.ID,
				OperatorID:    opID,
				Status:        "declared", // Always starts as declared per policy
				Notes:         strings.TrimSpace(data["notes"]),
				EffectiveFrom: parseOptionalTime(data["effective_from"]),
				EffectiveTo:   parseOptionalTime(data["effective_to"]),
				DataSource:    src,
				CreatedBy:     &actorUserID,
				UpdatedBy:     &actorUserID,
			}

			created, err := s.providerRepo.CreateCoverage(ctx, tx, &cov, reason)
			if err != nil {
				return 0, fmt.Errorf("import provider_coverage row %d: %w", row.RowNumber, err)
			}
			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return 0, err
			}
			importedCount++
		}

	case "sender_id":
		if s.senderIDRepo == nil {
			return 0, fmt.Errorf("sender_id repository not configured on import service")
		}
		for _, row := range validRows {
			var data map[string]string
			_ = json.Unmarshal(row.RawData, &data)

			orgID, err := s.resolveCustomerOrg(ctx, strings.TrimSpace(data["organization_id"]), strings.TrimSpace(data["organization_name"]))
			if err != nil {
				return 0, fmt.Errorf("row %d: %w", row.RowNumber, err)
			}
			normalizedSid, err := senderid.ValidateSenderIDFormat(strings.TrimSpace(data["sender_id"]), strings.ToLower(strings.TrimSpace(data["sender_type"])))
			if err != nil {
				return 0, fmt.Errorf("row %d: %w", row.RowNumber, err)
			}

			var countryID *uuid.UUID
			iso2 := strings.ToUpper(strings.TrimSpace(data["country_iso2"]))
			if iso2 == "" {
				iso2 = strings.ToUpper(strings.TrimSpace(data["iso2"]))
			}
			if iso2 != "" {
				c, err := s.countryRepo.GetByISO2(ctx, iso2)
				if err != nil || c == nil {
					return 0, fmt.Errorf("country iso2 %s not found: %w", iso2, err)
				}
				countryID = &c.ID
			}

			src := strings.TrimSpace(data["data_source"])
			if src == "" {
				src = fmt.Sprintf("Import: %s", job.FileName)
			}

			sid := senderid.SenderID{
				OrganizationID: orgID,
				SenderID:       normalizedSid,
				SenderType:     strings.ToLower(strings.TrimSpace(data["sender_type"])),
				CountryID:      countryID,
				Status:         "pending", // Always starts as pending per policy
				ApprovalNotes:  strings.TrimSpace(data["notes"]),
				DataSource:     src,
				CreatedBy:      &actorUserID,
				UpdatedBy:      &actorUserID,
			}

			created, err := s.senderIDRepo.Create(ctx, tx, &sid, reason)
			if err != nil {
				return 0, fmt.Errorf("import sender_id row %d: %w", row.RowNumber, err)
			}
			if err := s.repo.UpdateRowImported(ctx, tx, row.ID, created.ID); err != nil {
				return 0, err
			}
			importedCount++
		}
	}

	return importedCount, nil
}

func parseOptionalTime(raw string) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
