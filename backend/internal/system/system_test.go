package system_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/internal/system"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/logx"
)

type fakeRepo struct {
	state   system.MigrationState
	stateEr error
	entries []system.SchemaEntry
	infoEr  error
}

func (f fakeRepo) MigrationState(context.Context) (system.MigrationState, error) {
	return f.state, f.stateEr
}
func (f fakeRepo) SchemaInfo(context.Context) ([]system.SchemaEntry, error) {
	return f.entries, f.infoEr
}

func app(t *testing.T, repo system.Repository) *fiber.App {
	t.Helper()
	logger, _ := logx.New(io.Discard, "error", "json")
	a := httpx.NewApp(httpx.AppConfig{AllowedMethods: []string{"GET"}, BodyLimitBytes: 1024}, logger)
	system.NewHandler(system.NewService(repo, "9.9.9", "test")).Register(a.Group("/api/v1"))
	return a
}

func call(t *testing.T, a *fiber.App) (int, []byte) {
	t.Helper()
	resp, err := a.Test(httptest.NewRequest("GET", "/api/v1/system/info", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, b
}

func TestServiceInfo(t *testing.T) {
	ts := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	svc := system.NewService(fakeRepo{
		state:   system.MigrationState{Version: 1},
		entries: []system.SchemaEntry{{Key: "schema_initialized_at", Value: "x", CreatedAt: ts}},
	}, "9.9.9", "test")
	info, err := svc.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "znero-backend" || info.Version != "9.9.9" || info.Environment != "test" ||
		info.Migration.Version != 1 || len(info.Schema) != 1 {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestServicePropagatesRepositoryErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := system.NewService(fakeRepo{stateEr: boom}, "v", "e").Info(context.Background()); !errors.Is(err, boom) {
		t.Errorf("migration state error not propagated: %v", err)
	}
	if _, err := system.NewService(fakeRepo{infoEr: boom}, "v", "e").Info(context.Background()); !errors.Is(err, boom) {
		t.Errorf("schema info error not propagated: %v", err)
	}
}

func TestInfoEndpoint(t *testing.T) {
	status, body := call(t, app(t, fakeRepo{
		state:   system.MigrationState{Version: 1},
		entries: []system.SchemaEntry{{Key: "schema_initialized_at", Value: "2026-11-02T12:00:00Z"}},
	}))
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Migration struct {
				Version int64 `json:"version"`
				Dirty   bool  `json:"dirty"`
			} `json:"migration"`
			Schema []struct {
				Key string `json:"key"`
			} `json:"schema"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Success || got.Data.Migration.Version != 1 || len(got.Data.Schema) != 1 || got.Data.Schema[0].Key != "schema_initialized_at" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestInfoEndpointNotMigrated(t *testing.T) {
	status, body := call(t, app(t, fakeRepo{stateEr: system.ErrNotMigrated}))
	if status != 503 || !strings.Contains(string(body), `"code":"database_not_migrated"`) {
		t.Errorf("want 503 database_not_migrated, got %d %s", status, body)
	}
}

func TestInfoEndpointHidesDatabaseErrors(t *testing.T) {
	status, body := call(t, app(t, fakeRepo{infoEr: errors.New(`ERROR: relation "schema_info" column "x" (SQLSTATE 42703)`)}))
	if status != 500 {
		t.Fatalf("status = %d", status)
	}
	if strings.Contains(string(body), "SQLSTATE") || strings.Contains(string(body), "relation") {
		t.Errorf("SQL error leaked: %s", body)
	}
}
