package notification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/notification"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestNotification_StormSuppressionAndRateLimiting(t *testing.T) {
	suppressor := notification.NewStormSuppressor(5*time.Minute, 1*time.Minute, 50)

	userID := uuid.New()
	dedupKey := "alert:critical:high_failure_rate"

	// 1. First event should succeed
	allowed, reason := suppressor.CheckAndRecord(&userID, dedupKey)
	if !allowed {
		t.Fatalf("expected first event to be allowed, got suppressed: %s", reason)
	}

	// 2. Duplicate event with same dedupKey within 5m must be suppressed
	allowed2, reason2 := suppressor.CheckAndRecord(&userID, dedupKey)
	if allowed2 {
		t.Fatalf("expected duplicate event to be suppressed, but was allowed")
	}
	if reason2 == "" {
		t.Errorf("expected non-empty reason for suppression")
	}

	// 3. Different dedupKey should be allowed
	allowed3, _ := suppressor.CheckAndRecord(&userID, "alert:critical:carrier_down")
	if !allowed3 {
		t.Fatalf("expected different event to be allowed")
	}

	// 4. Rate Limiting Test: Fire up to 50 notifications for this user
	suppressor.Reset()
	user2 := uuid.New()

	for i := 0; i < 50; i++ {
		uniqueKey := fmt.Sprintf("event:%d", i)
		ok, r := suppressor.CheckAndRecord(&user2, uniqueKey)
		if !ok {
			t.Fatalf("event %d should be allowed under rate limit, got suppressed: %s", i, r)
		}
	}

	// 51st event for user2 must be blocked by rate limit
	ok51, r51 := suppressor.CheckAndRecord(&user2, "event:51")
	if ok51 {
		t.Fatalf("expected 51st event to be rate limited, but was allowed")
	}
	if r51 == "" {
		t.Errorf("expected rate limit reason")
	}

	// Another user should NOT be rate limited
	user3 := uuid.New()
	okOther, _ := suppressor.CheckAndRecord(&user3, "event:user3")
	if !okOther {
		t.Fatalf("unrelated user should not be affected by user2's rate limit")
	}
}

func TestNotification_CRUDAndTenantIsolation(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	// Seed test organization & users
	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	orgAID := uuid.New()
	userAID := uuid.New()
	orgBID := uuid.New()
	userBID := uuid.New()

	pathA := "/" + platID.String() + "/" + orgAID.String() + "/"
	pathB := "/" + platID.String() + "/" + orgBID.String() + "/"

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Customer Org A', 'customer', $2, $3, 'active'),
		       ($4, 'Customer Org B', 'customer', $2, $5, 'active')
		ON CONFLICT DO NOTHING
	`, orgAID, platID, pathA, orgBID, pathB)
	if err != nil {
		t.Fatalf("seed orgs: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, organization_id, email, password_hash, status)
		VALUES ($1, $2, 'usera_' || substr(md5(random()::text), 1, 6) || '@example.com', 'hash', 'active'),
		       ($3, $4, 'userb_' || substr(md5(random()::text), 1, 6) || '@example.com', 'hash', 'active')
		ON CONFLICT DO NOTHING
	`, userAID, orgAID, userBID, orgBID)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	repo := notification.NewRepository(pool, nil)
	emailMock := notification.NewMockEmailSender()
	webhookMock := notification.NewMockWebhookSender()
	suppressor := notification.NewStormSuppressor(1*time.Second, 1*time.Minute, 50)
	svc := notification.NewService(repo, emailMock, webhookMock, suppressor, nil)

	tcA := &tenant.Context{
		ActorUserID:  userAID,
		ActorOrgID:   orgAID,
		ActorOrgType: "customer",
	}
	tcB := &tenant.Context{
		ActorUserID:  userBID,
		ActorOrgID:   orgBID,
		ActorOrgType: "customer",
	}

	// 1. User A has initially 0 notifications
	countA, err := svc.GetUnreadCount(ctx, tcA)
	if err != nil {
		t.Fatalf("unread count A: %v", err)
	}
	if countA != 0 {
		t.Fatalf("expected 0 unread for A, got %d", countA)
	}

	// 2. Dispatch 2 notifications to User A
	notif1ID := uuid.New()
	notif2ID := uuid.New()

	err = repo.Create(ctx, &notification.Notification{
		ID:             notif1ID,
		OrganizationID: &orgAID,
		UserID:         &userAID,
		Category:       notification.CategoryBilling,
		Severity:       notification.SeverityWarning,
		Title:          "Low Balance Alert",
		Body:           "Your prepaid wallet balance is below $50.",
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create notif1: %v", err)
	}

	err = repo.Create(ctx, &notification.Notification{
		ID:             notif2ID,
		OrganizationID: &orgAID,
		UserID:         &userAID,
		Category:       notification.CategorySecurity,
		Severity:       notification.SeverityCritical,
		Title:          "New Login from Berlin",
		Body:           "Suspicious IP detected.",
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create notif2: %v", err)
	}

	// 3. User A unread count must be 2; User B must be 0
	countA, _ = svc.GetUnreadCount(ctx, tcA)
	if countA != 2 {
		t.Errorf("expected 2 unread for A, got %d", countA)
	}

	countB, _ := svc.GetUnreadCount(ctx, tcB)
	if countB != 0 {
		t.Errorf("TENANT LEAK: User B sees %d unread notifications belonging to User A", countB)
	}

	// 4. User B cannot see User A's notifications in list
	itemsB, totalB, err := svc.List(ctx, tcB, notification.ListFilter{})
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if totalB != 0 || len(itemsB) != 0 {
		t.Errorf("TENANT LEAK: User B listed User A's notifications: total=%d, items=%d", totalB, len(itemsB))
	}

	// 5. User B cannot mark User A's notification as read
	err = svc.MarkAsRead(ctx, tcB, notif1ID)
	if err == nil {
		t.Errorf("expected error when User B marks User A notification, got nil")
	}

	// 6. User A marks notif1 as read
	err = svc.MarkAsRead(ctx, tcA, notif1ID)
	if err != nil {
		t.Fatalf("mark read A: %v", err)
	}
	countA, _ = svc.GetUnreadCount(ctx, tcA)
	if countA != 1 {
		t.Errorf("expected 1 unread after marking notif1 read, got %d", countA)
	}

	// 7. User A marks all as read
	err = svc.MarkAllAsRead(ctx, tcA)
	if err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	countA, _ = svc.GetUnreadCount(ctx, tcA)
	if countA != 0 {
		t.Errorf("expected 0 unread after mark all read, got %d", countA)
	}

	// 8. User A deletes notif2
	err = svc.Delete(ctx, tcA, notif2ID)
	if err != nil {
		t.Fatalf("delete notif2: %v", err)
	}
	itemsA, totalA, _ := svc.List(ctx, tcA, notification.ListFilter{})
	if totalA != 1 {
		t.Errorf("expected 1 remaining notification, got %d (items=%d)", totalA, len(itemsA))
	}
}

func TestNotification_MultiChannelDispatch(t *testing.T) {
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

	orgID := uuid.New()
	userID := uuid.New()
	customerPath := "/" + platID.String() + "/" + orgID.String() + "/"

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Channel Test Org', 'customer', $2, $3, 'active')
		ON CONFLICT DO NOTHING
	`, orgID, platID, customerPath)
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}

	testEmail := fmt.Sprintf("notify_%s@example.com", testkit.RandHex(4))
	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, organization_id, email, password_hash, status)
		VALUES ($1, $2, $3, 'hash', 'active')
		ON CONFLICT DO NOTHING
	`, userID, orgID, testEmail)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	repo := notification.NewRepository(pool, nil)
	emailMock := notification.NewMockEmailSender()
	webhookMock := notification.NewMockWebhookSender()
	suppressor := notification.NewStormSuppressor(1*time.Second, 1*time.Minute, 50)
	svc := notification.NewService(repo, emailMock, webhookMock, suppressor, nil)

	// Configure preferences: email enabled, webhook enabled
	webhookURL := "https://example.com/webhook/alerts"
	pref := &notification.NotificationPreferences{
		ID:             uuid.New(),
		UserID:         userID,
		EmailEnabled:   true,
		WebhookEnabled: true,
		WebhookURL:     &webhookURL,
		Categories: map[string]notification.CategoryPreference{
			notification.CategoryBilling: {Email: true, InApp: true, Webhook: true},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.UpsertPreferences(ctx, pref); err != nil {
		t.Fatalf("upsert preferences: %v", err)
	}

	// 1. Dispatch billing alert
	ev := notification.DispatchEvent{
		OrganizationID: &orgID,
		UserID:         &userID,
		Category:       notification.CategoryBilling,
		Severity:       notification.SeverityWarning,
		Title:          "Invoice Ready",
		Body:           "Your monthly invoice #1002 has been generated.",
		EventType:      notification.EventInvoiceGenerated,
		DedupKey:       "billing:invoice:1002",
	}

	if err := svc.Dispatch(ctx, ev); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}

	// Check Email Sender received the email
	emails := emailMock.SentEmails()
	if len(emails) != 1 {
		t.Fatalf("expected 1 sent email, got %d", len(emails))
	}
	if emails[0].To != testEmail {
		t.Errorf("expected email to %s, got %s", testEmail, emails[0].To)
	}

	// Check Webhook Sender received the webhook
	webhooks := webhookMock.SentWebhooks()
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 sent webhook, got %d", len(webhooks))
	}
	if webhooks[0].TargetURL != webhookURL {
		t.Errorf("expected webhook url %s, got %s", webhookURL, webhooks[0].TargetURL)
	}
	if webhooks[0].EventType != notification.EventInvoiceGenerated {
		t.Errorf("expected event type %s, got %s", notification.EventInvoiceGenerated, webhooks[0].EventType)
	}

	// Verify HMAC-SHA256 signature verification
	if !notification.VerifyWebhookSignature(webhooks[0].Secret, webhooks[0].Signature, webhooks[0].Payload) {
		t.Errorf("webhook signature verification failed!")
	}

	// 2. Disable email in preferences and dispatch another event
	pref.EmailEnabled = false
	if err := repo.UpsertPreferences(ctx, pref); err != nil {
		t.Fatalf("disable email pref: %v", err)
	}

	emailMock.Reset()
	webhookMock.Reset()

	ev2 := notification.DispatchEvent{
		OrganizationID: &orgID,
		UserID:         &userID,
		Category:       notification.CategoryBilling,
		Severity:       notification.SeverityInfo,
		Title:          "Payment Received",
		Body:           "Payment of $200 has been credited.",
		EventType:      notification.EventPaymentReceived,
		DedupKey:       "billing:payment:9999",
	}

	if err := svc.Dispatch(ctx, ev2); err != nil {
		t.Fatalf("dispatch event 2: %v", err)
	}

	// Email should NOT be sent
	if len(emailMock.SentEmails()) != 0 {
		t.Errorf("expected 0 emails when email disabled, got %d", len(emailMock.SentEmails()))
	}
	// Webhook should still be sent
	if len(webhookMock.SentWebhooks()) != 1 {
		t.Errorf("expected 1 webhook, got %d", len(webhookMock.SentWebhooks()))
	}
}

func TestNotification_HTTPRoutesAndRBAC(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	tokenMgr, err := tokens.NewTokenManager(testkit.RandHex(32), 15*time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	repo := notification.NewRepository(pool, nil)
	svc := notification.NewService(repo, nil, nil, nil, nil)
	handler := notification.NewHandler(svc)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler:          httpx.ErrorHandler(nil),
	})

	authMiddleware := func(c *fiber.Ctx) error {
		authHdr := c.Get("Authorization")
		if authHdr == "" {
			return httpx.Unauthorized("Missing authorization header")
		}
		var tokenStr string
		if len(authHdr) > 7 && authHdr[:7] == "Bearer " {
			tokenStr = authHdr[7:]
		} else {
			tokenStr = authHdr
		}
		claims, err := tokenMgr.ValidateAccessToken(tokenStr)
		if err != nil {
			return httpx.Unauthorized("Invalid token")
		}
		perms := make(map[string]bool, len(claims.Permissions))
		for _, p := range claims.Permissions {
			perms[p] = true
		}
		tc := &tenant.Context{
			ActorUserID:     claims.UserID,
			ActorOrgID:      claims.OrgID,
			ActorOrgType:    claims.OrgType,
			ActorOrgPath:    claims.OrgPath,
			IsPlatformAdmin: claims.IsPlatformAdmin,
			Permissions:     perms,
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	}

	handler.Register(app.Group("/api/v1"), authMiddleware)

	// Seed test user
	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	userID := uuid.New()
	orgID := uuid.New()
	customerPath := "/" + platID.String() + "/" + orgID.String() + "/"

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'HTTP Org', 'customer', $2, $3, 'active')
		ON CONFLICT DO NOTHING
	`, orgID, platID, customerPath)
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, organization_id, email, password_hash, status)
		VALUES ($1, $2, 'httpuser_' || substr(md5(random()::text), 1, 6) || '@example.com', 'hash', 'active')
		ON CONFLICT DO NOTHING
	`, userID, orgID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// 1. Unauthorized request (No Token) -> 401
	reqUnauth := httptest.NewRequest("GET", "/api/v1/notifications", nil)
	respUnauth, _ := app.Test(reqUnauth)
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauth request, got %d", respUnauth.StatusCode)
	}

	// 2. Token with notification.view only
	viewOnlyToken, _ := tokenMgr.GenerateAccessToken(userID, orgID, orgID, "customer", "/c/", false, []string{"notification.view"})

	// GET /unread-count
	reqUnread := httptest.NewRequest("GET", "/api/v1/notifications/unread-count", nil)
	reqUnread.Header.Set("Authorization", "Bearer "+viewOnlyToken)
	respUnread, _ := app.Test(reqUnread)
	if respUnread.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for unread-count, got %d", respUnread.StatusCode)
	}

	// Attempt PUT /preferences with viewOnlyToken -> 403 Forbidden (requires notification.manage)
	bodyPref := []byte(`{"email_enabled": false}`)
	reqPrefForbidden := httptest.NewRequest("PUT", "/api/v1/notifications/preferences", bytes.NewReader(bodyPref))
	reqPrefForbidden.Header.Set("Authorization", "Bearer "+viewOnlyToken)
	reqPrefForbidden.Header.Set("Content-Type", "application/json")
	respPrefForbidden, _ := app.Test(reqPrefForbidden)
	if respPrefForbidden.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for update preferences without notification.manage, got %d", respPrefForbidden.StatusCode)
	}

	// 3. Full permissions token (notification.view + notification.manage)
	fullToken, _ := tokenMgr.GenerateAccessToken(userID, orgID, orgID, "customer", "/c/", false, []string{"notification.view", "notification.manage"})

	// PUT /preferences -> 200 OK
	reqPrefOK := httptest.NewRequest("PUT", "/api/v1/notifications/preferences", bytes.NewReader(bodyPref))
	reqPrefOK.Header.Set("Authorization", "Bearer "+fullToken)
	reqPrefOK.Header.Set("Content-Type", "application/json")
	respPrefOK, _ := app.Test(reqPrefOK)
	if respPrefOK.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for update preferences with full token, got %d", respPrefOK.StatusCode)
	}

	// POST /test -> 200 OK
	reqTest := httptest.NewRequest("POST", "/api/v1/notifications/test", nil)
	reqTest.Header.Set("Authorization", "Bearer "+fullToken)
	respTest, _ := app.Test(reqTest)
	if respTest.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for test notification, got %d", respTest.StatusCode)
	}

	// GET / -> should now have the test notification
	reqList := httptest.NewRequest("GET", "/api/v1/notifications", nil)
	reqList.Header.Set("Authorization", "Bearer "+fullToken)
	respList, _ := app.Test(reqList)
	if respList.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for list notifications, got %d", respList.StatusCode)
	}

	var listData struct {
		Data struct {
			Items []notification.Notification `json:"items"`
			Total int                         `json:"total"`
		} `json:"data"`
	}
	_ = json.NewDecoder(respList.Body).Decode(&listData)
	if listData.Data.Total < 1 {
		t.Errorf("expected at least 1 notification after test, got total=%d", listData.Data.Total)
	}

	// POST /read-all -> 200 OK
	reqReadAll := httptest.NewRequest("POST", "/api/v1/notifications/read-all", nil)
	reqReadAll.Header.Set("Authorization", "Bearer "+fullToken)
	respReadAll, _ := app.Test(reqReadAll)
	if respReadAll.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for read-all, got %d", respReadAll.StatusCode)
	}
}
