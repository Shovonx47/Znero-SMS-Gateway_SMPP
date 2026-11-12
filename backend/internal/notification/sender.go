package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// EmailSender interface defines email dispatch.
type EmailSender interface {
	SendEmail(ctx context.Context, to string, subject string, htmlBody string, textBody string) error
}

// SentEmail records an email dispatched by MockEmailSender.
type SentEmail struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
	SentAt   time.Time
}

// MockEmailSender is a thread-safe in-memory email sink for testing.
type MockEmailSender struct {
	mu      sync.Mutex
	emails  []SentEmail
	failErr error
}

func NewMockEmailSender() *MockEmailSender {
	return &MockEmailSender{emails: make([]SentEmail, 0)}
}

func (m *MockEmailSender) SendEmail(ctx context.Context, to string, subject string, htmlBody string, textBody string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failErr != nil {
		return m.failErr
	}
	m.emails = append(m.emails, SentEmail{
		To:       to,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
		SentAt:   time.Now().UTC(),
	})
	return nil
}

func (m *MockEmailSender) SentEmails() []SentEmail {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]SentEmail, len(m.emails))
	copy(res, m.emails)
	return res
}

func (m *MockEmailSender) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emails = nil
	m.failErr = nil
}

func (m *MockEmailSender) SetFailErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failErr = err
}

// SMTPEmailSender sends emails via standard SMTP.
type SMTPEmailSender struct {
	addr     string
	auth     smtp.Auth
	fromAddr string
}

func NewSMTPEmailSender(host string, port int, username, password, fromAddr string) *SMTPEmailSender {
	addr := fmt.Sprintf("%s:%d", host, port)
	var auth smtp.Auth
	if username != "" || password != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	return &SMTPEmailSender{
		addr:     addr,
		auth:     auth,
		fromAddr: fromAddr,
	}
}

func (s *SMTPEmailSender) SendEmail(ctx context.Context, to string, subject string, htmlBody string, textBody string) error {
	boundary := "b_znero_boundary_" + hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.fromAddr))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary))

	if textBody != "" {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		msg.WriteString(textBody)
		msg.WriteString("\r\n\r\n")
	}

	if htmlBody != "" {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		msg.WriteString(htmlBody)
		msg.WriteString("\r\n\r\n")
	}
	msg.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	return smtp.SendMail(s.addr, s.auth, s.fromAddr, []string{to}, msg.Bytes())
}

// WebhookSender interface defines webhook dispatch.
type WebhookSender interface {
	SendWebhook(ctx context.Context, targetURL string, secret string, eventType string, payload []byte) error
}

// SentWebhook records a webhook dispatched by MockWebhookSender.
type SentWebhook struct {
	TargetURL string
	Secret    string
	EventType string
	Payload   []byte
	Signature string
	SentAt    time.Time
}

// MockWebhookSender is a thread-safe in-memory webhook sink for testing.
type MockWebhookSender struct {
	mu       sync.Mutex
	webhooks []SentWebhook
	failErr  error
}

func NewMockWebhookSender() *MockWebhookSender {
	return &MockWebhookSender{webhooks: make([]SentWebhook, 0)}
}

func (m *MockWebhookSender) SendWebhook(ctx context.Context, targetURL string, secret string, eventType string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failErr != nil {
		return m.failErr
	}

	sig := ComputeWebhookSignature(secret, payload)
	m.webhooks = append(m.webhooks, SentWebhook{
		TargetURL: targetURL,
		Secret:    secret,
		EventType: eventType,
		Payload:   payload,
		Signature: sig,
		SentAt:    time.Now().UTC(),
	})
	return nil
}

func (m *MockWebhookSender) SentWebhooks() []SentWebhook {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]SentWebhook, len(m.webhooks))
	copy(res, m.webhooks)
	return res
}

func (m *MockWebhookSender) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webhooks = nil
	m.failErr = nil
}

func (m *MockWebhookSender) SetFailErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failErr = err
}

// HTTPWebhookSender dispatches signed webhooks over HTTP.
type HTTPWebhookSender struct {
	client *http.Client
}

func NewHTTPWebhookSender(timeout time.Duration) *HTTPWebhookSender {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HTTPWebhookSender{
		client: &http.Client{Timeout: timeout},
	}
}

func ComputeWebhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (h *HTTPWebhookSender) SendWebhook(ctx context.Context, targetURL string, secret string, eventType string, payload []byte) error {
	if targetURL == "" {
		return fmt.Errorf("empty webhook target URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Znero-Notification-Dispatcher/1.0")
	req.Header.Set("X-Znero-Event", eventType)
	if secret != "" {
		req.Header.Set("X-Znero-Signature", ComputeWebhookSignature(secret, payload))
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook responded with HTTP %d", resp.StatusCode)
	}
	return nil
}

// VerifyWebhookSignature verifies HMAC-SHA256 signature against received payload.
func VerifyWebhookSignature(secret, headerSig string, payload []byte) bool {
	expected := ComputeWebhookSignature(secret, payload)
	return hmac.Equal([]byte(strings.TrimSpace(headerSig)), []byte(expected))
}

