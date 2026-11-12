package export

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo       *Repository
	generator  *Generator
	storageDir string
	logger     *slog.Logger
}

func NewService(repo *Repository, storageDir string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if storageDir == "" {
		storageDir = filepath.Join("tmp", "exports")
	}
	_ = os.MkdirAll(storageDir, 0755)

	return &Service{
		repo:       repo,
		generator:  NewGenerator(),
		storageDir: storageDir,
		logger:     logger.With(slog.String("component", "export_service")),
	}
}

// SubmitJob schedules an export job asynchronously.
func (s *Service) SubmitJob(ctx context.Context, tc *tenant.Context, req CreateJobRequest) (*ExportJob, error) {
	switch req.Format {
	case FormatCSV, FormatJSON, FormatXLSX:
	default:
		return nil, ErrUnsupportedFormat
	}

	switch req.ExportType {
	case TypeMessages, TypeDeliveryReport, TypeFinancialReport:
	default:
		return nil, ErrUnsupportedType
	}

	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)
	job := &ExportJob{
		ID:             uuid.New(),
		OrganizationID: tc.ActorOrgID,
		UserID:         tc.ActorUserID,
		ExportType:     req.ExportType,
		Format:         req.Format,
		Status:         StatusPending,
		FilterParams:   req.FilterParams,
		RowCount:       0,
		ExpiresAt:      &expiresAt,
		CreatedAt:      now,
	}

	if err := s.repo.CreateJob(ctx, job); err != nil {
		return nil, err
	}

	// Trigger async processing
	go func() {
		processCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.ProcessJobSync(processCtx, job.ID, tc)
	}()

	return job, nil
}

// ProcessJobSync performs data querying, file generation, and disk storage synchronously.
func (s *Service) ProcessJobSync(ctx context.Context, jobID uuid.UUID, tc *tenant.Context) {
	job, err := s.repo.GetJob(ctx, jobID)
	if err != nil {
		s.logger.Error("failed to get job for processing", slog.String("job_id", jobID.String()), slog.Any("error", err))
		return
	}

	startedAt := time.Now().UTC()
	_ = s.repo.UpdateJobProgress(ctx, jobID, StatusProcessing, 0, nil, nil, &startedAt, nil)

	defer func() {
		if r := recover(); r != nil {
			errStr := fmt.Sprintf("panic in export job: %v", r)
			s.logger.Error("panic in export execution", slog.String("job_id", jobID.String()), slog.Any("panic", r))
			completedAt := time.Now().UTC()
			_ = s.repo.UpdateJobProgress(ctx, jobID, StatusFailed, 0, nil, &errStr, nil, &completedAt)
		}
	}()

	var headers []string
	var matrix [][]string
	var records []map[string]interface{}

	switch job.ExportType {
	case TypeMessages:
		headers, matrix, records, err = s.repo.QueryMessagesExport(ctx, tc, job.FilterParams)
	case TypeDeliveryReport:
		headers, matrix, records, err = s.repo.QueryDeliveryReportExport(ctx, tc, job.FilterParams)
	case TypeFinancialReport:
		headers, matrix, records, err = s.repo.QueryFinancialReportExport(ctx, tc, job.FilterParams)
	default:
		err = ErrUnsupportedType
	}

	if err != nil {
		errMsg := err.Error()
		completedAt := time.Now().UTC()
		_ = s.repo.UpdateJobProgress(ctx, jobID, StatusFailed, 0, nil, &errMsg, nil, &completedAt)
		return
	}

	var data []byte
	switch job.Format {
	case FormatCSV:
		data, err = s.generator.GenerateCSV(headers, matrix)
	case FormatJSON:
		data, err = s.generator.GenerateJSON(records)
	case FormatXLSX:
		data, err = s.generator.GenerateXLSX("Export", headers, matrix)
	}

	if err != nil {
		errMsg := fmt.Sprintf("generation error: %v", err)
		completedAt := time.Now().UTC()
		_ = s.repo.UpdateJobProgress(ctx, jobID, StatusFailed, 0, nil, &errMsg, nil, &completedAt)
		return
	}

	fileName := fmt.Sprintf("%s.%s", jobID.String(), job.Format)
	filePath := filepath.Join(s.storageDir, fileName)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		errMsg := fmt.Sprintf("file write error: %v", err)
		completedAt := time.Now().UTC()
		_ = s.repo.UpdateJobProgress(ctx, jobID, StatusFailed, 0, nil, &errMsg, nil, &completedAt)
		return
	}

	completedAt := time.Now().UTC()
	rowCount := int64(len(matrix))
	if rowCount == 0 && len(records) > 0 {
		rowCount = int64(len(records))
	}

	_ = s.repo.UpdateJobProgress(ctx, jobID, StatusCompleted, rowCount, &filePath, nil, nil, &completedAt)
	s.logger.Info("export job completed",
		slog.String("job_id", jobID.String()),
		slog.String("type", job.ExportType),
		slog.String("format", job.Format),
		slog.Int64("rows", rowCount),
	)
}

// GetJob retrieves an export job verifying tenancy access.
func (s *Service) GetJob(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*ExportJob, error) {
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}

	if !tc.IsPlatformAdmin && job.OrganizationID != tc.ActorOrgID {
		return nil, ErrUnauthorizedTenant
	}
	return job, nil
}

// ListJobs retrieves export jobs for the caller.
func (s *Service) ListJobs(ctx context.Context, tc *tenant.Context, limit, offset int) ([]*ExportJob, int64, error) {
	return s.repo.ListJobs(ctx, tc, limit, offset)
}

// DownloadJob prepares file info for HTTP download verifying tenancy and state.
func (s *Service) DownloadJob(ctx context.Context, tc *tenant.Context, id uuid.UUID) (filePath, filename, contentType string, err error) {
	job, err := s.GetJob(ctx, tc, id)
	if err != nil {
		return "", "", "", err
	}

	if job.Status != StatusCompleted {
		return "", "", "", ErrJobNotReady
	}
	if job.FilePath == nil || *job.FilePath == "" {
		return "", "", "", ErrJobNotFound
	}
	if job.ExpiresAt != nil && time.Now().After(*job.ExpiresAt) {
		return "", "", "", ErrJobExpired
	}

	if _, statErr := os.Stat(*job.FilePath); statErr != nil {
		return "", "", "", ErrJobNotFound
	}

	switch job.Format {
	case FormatCSV:
		contentType = "text/csv; charset=utf-8"
	case FormatJSON:
		contentType = "application/json"
	case FormatXLSX:
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		contentType = "application/octet-stream"
	}

	filename = fmt.Sprintf("export_%s_%s.%s", job.ExportType, job.ID.String()[:8], job.Format)
	return *job.FilePath, filename, contentType, nil
}
