package auth

import (
	"context"
	"fmt"
	"log/slog"
)

// Mailer abstracts outbound email delivery.
type Mailer interface {
	SendPasswordReset(ctx context.Context, email, token string) error
	SendUserInvitation(ctx context.Context, email, token, orgName string) error
}

// DevConsoleMailer logs links directly to the console in local development.
// It fails fast if APP_ENV is not development.
type DevConsoleMailer struct {
	env    string
	logger *slog.Logger
}

func NewDevConsoleMailer(env string, logger *slog.Logger) *DevConsoleMailer {
	if logger == nil {
		logger = slog.Default()
	}
	return &DevConsoleMailer{env: env, logger: logger}
}

func (m *DevConsoleMailer) SendPasswordReset(ctx context.Context, email, token string) error {
	if m.env != "development" {
		return fmt.Errorf("DevConsoleMailer disabled in environment: %s", m.env)
	}
	link := fmt.Sprintf("http://127.0.0.1:5173/reset-password?token=%s", token)
	m.logger.InfoContext(ctx, "=== [DEV MAILER] PASSWORD RESET LINK ===",
		slog.String("email", email),
		slog.String("reset_link", link),
	)
	fmt.Printf("\n--------------------------------------------------------------------------------\n")
	fmt.Printf("[DEV MAILER] Password Reset for: %s\nLink: %s\n", email, link)
	fmt.Printf("--------------------------------------------------------------------------------\n\n")
	return nil
}

func (m *DevConsoleMailer) SendUserInvitation(ctx context.Context, email, token, orgName string) error {
	if m.env != "development" {
		return fmt.Errorf("DevConsoleMailer disabled in environment: %s", m.env)
	}
	link := fmt.Sprintf("http://127.0.0.1:5173/accept-invitation?token=%s", token)
	m.logger.InfoContext(ctx, "=== [DEV MAILER] USER INVITATION LINK ===",
		slog.String("email", email),
		slog.String("organization", orgName),
		slog.String("invitation_link", link),
	)
	fmt.Printf("\n--------------------------------------------------------------------------------\n")
	fmt.Printf("[DEV MAILER] User Invitation for %s to join %s\nLink: %s\n", email, orgName, link)
	fmt.Printf("--------------------------------------------------------------------------------\n\n")
	return nil
}
