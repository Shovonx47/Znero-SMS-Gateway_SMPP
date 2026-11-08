package audit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/database"
)

func TestRedactValue(t *testing.T) {
	input := map[string]any{
		"email":              "admin@example.com",
		"password":           "SuperSecret1234",
		"password_hash":      "hash_val",
		"token":              "raw_token",
		"refresh_token_hash": "secret_hash",
		"mfa_secret":         "JBSWY3DPEHPK3PXP",
		"recovery_codes":     []string{"code1", "code2"},
		"normal_field":       "visible_value",
		"nested": map[string]any{
			"jwt_secret": "nested_secret",
			"active":     true,
		},
	}

	raw := RedactValue(input)
	var redacted map[string]any
	if err := json.Unmarshal(raw, &redacted); err != nil {
		t.Fatalf("unmarshal redacted: %v", err)
	}

	if redacted["password"] != "[REDACTED]" {
		t.Errorf("expected password to be redacted, got %v", redacted["password"])
	}
	if redacted["password_hash"] != "[REDACTED]" {
		t.Errorf("expected password_hash to be redacted, got %v", redacted["password_hash"])
	}
	if redacted["token"] != "[REDACTED]" {
		t.Errorf("expected token to be redacted, got %v", redacted["token"])
	}
	if redacted["refresh_token_hash"] != "[REDACTED]" {
		t.Errorf("expected refresh_token_hash to be redacted, got %v", redacted["refresh_token_hash"])
	}
	if redacted["mfa_secret"] != "[REDACTED]" {
		t.Errorf("expected mfa_secret to be redacted, got %v", redacted["mfa_secret"])
	}
	if redacted["recovery_codes"] != "[REDACTED]" {
		t.Errorf("expected recovery_codes to be redacted, got %v", redacted["recovery_codes"])
	}
	if redacted["normal_field"] != "visible_value" {
		t.Errorf("expected normal_field to be preserved, got %v", redacted["normal_field"])
	}
	nested := redacted["nested"].(map[string]any)
	if nested["jwt_secret"] != "[REDACTED]" {
		t.Errorf("expected nested jwt_secret to be redacted, got %v", nested["jwt_secret"])
	}
	if nested["active"] != true {
		t.Errorf("expected nested active to be true, got %v", nested["active"])
	}
}

func TestAuditLogImmutability(t *testing.T) {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("skipping integration test without ZNERO_INTEGRATION=1")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://znero_app:s3cretPASS@localhost:5432/znero_dev?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            dbURL,
		MaxConns:       2,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	// Insert an audit log record
	var logID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO audit_logs (action, module, entity_type, entity_id, reason)
		VALUES ('test.insert', 'audit', 'test', '123', 'Testing immutability trigger')
		RETURNING id
	`).Scan(&logID)
	if err != nil {
		t.Fatalf("insert audit log failed: %v", err)
	}

	// Attempt UPDATE - MUST fail due to immutability trigger
	_, updateErr := pool.Exec(ctx, `UPDATE audit_logs SET reason = 'Modified' WHERE id = $1`, logID)
	if updateErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: UPDATE on audit_logs succeeded! Expected trigger rejection.")
	}

	// Attempt DELETE - MUST fail due to immutability trigger
	_, deleteErr := pool.Exec(ctx, `DELETE FROM audit_logs WHERE id = $1`, logID)
	if deleteErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: DELETE on audit_logs succeeded! Expected trigger rejection.")
	}
}
