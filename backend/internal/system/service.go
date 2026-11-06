// Package system exposes read-only information about the running backend and its database schema.
package system

import "context"

// Info is the payload of GET /api/v1/system/info.
type Info struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Environment string         `json:"environment"`
	Migration   MigrationState `json:"migration"`
	Schema      []SchemaEntry  `json:"schema"`
}

// Service builds Info from the build metadata and the database.
type Service struct {
	repo        Repository
	version     string
	environment string
}

func NewService(repo Repository, version, environment string) *Service {
	return &Service{repo: repo, version: version, environment: environment}
}

// Info returns ErrNotMigrated when the database has no migration state yet.
func (s *Service) Info(ctx context.Context) (Info, error) {
	mig, err := s.repo.MigrationState(ctx)
	if err != nil {
		return Info{}, err
	}
	entries, err := s.repo.SchemaInfo(ctx)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Name:        "znero-backend",
		Version:     s.version,
		Environment: s.environment,
		Migration:   mig,
		Schema:      entries,
	}, nil
}
