// Package app is the composition root for the HTTP layer: it wires module handlers onto the
// shared Fiber app and owns the API versioning prefix.
package app

import (
	"bytes"
	"log/slog"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/metrics"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/system"
	"github.com/znero-international/znero/backend/pkg/httpx"
)

// Version is the build version; override with -ldflags "-X .../internal/app.Version=1.2.3".
var Version = "dev"

// APIPrefix is the versioned base path of every business endpoint.
const APIPrefix = "/api/v1"

// Paths that are probed frequently: logged at debug level and exempt from rate limiting.
var quietPaths = []string{"/healthz", "/readyz"}

// allowedMethods is the default-deny method allow-list. PUT, PATCH and DELETE are allowed
// for resource management routes, while TRACE, CONNECT and unlisted methods remain denied.
var allowedMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// Modules are the handlers to mount. Diagnostics must be nil outside development.
type Modules struct {
	Health               *health.Handler
	System               *system.Handler
	Diagnostics          *diagnostics.Handler
	Auth                 *auth.Handler
	Org                  *org.Handler
	RBAC                 *rbac.Handler
	Audit                *audit.Handler
	Country              *country.Handler
	Operator             *operator.Handler
	Prefix               *prefix.Handler
	Approval             *approval.Handler
	ImportJob            *importjob.Handler
	Number               *number.Handler
	Provider             *provider.Handler
	Customer             *customer.Handler
	SenderID             *senderid.Handler
	Rating               *rating.Handler
	Ledger               *ledger.Handler
	Billing              *billing.Handler
	Routing              *routing.Handler
	Messaging            *messaging.Handler
	CustomerEdge         *customeredge.EdgeHandler
	HTTPProviderCallback *httpprovider.CallbackHandler
	AuthMiddleware       fiber.Handler
}

// NewHTTP builds the Fiber app with the standard middleware and all module routes.
func NewHTTP(cfg *config.Config, logger *slog.Logger, m Modules) *fiber.App {
	app := httpx.NewApp(httpx.AppConfig{
		Name:            "znero-backend",
		ReadTimeout:     cfg.Server.ReadTimeout,
		WriteTimeout:    cfg.Server.WriteTimeout,
		IdleTimeout:     cfg.Server.IdleTimeout,
		BodyLimitBytes:  cfg.Server.BodyLimitBytes,
		AllowedMethods:  allowedMethods,
		CORSOrigins:     cfg.Server.CORSOrigins,
		RateLimitMax:    cfg.Server.RateLimitMax,
		RateLimitWindow: cfg.Server.RateLimitWindow,
		QuietPaths:      quietPaths,
	}, logger)

	if m.Health != nil {
		m.Health.Register(app)
	}
	app.Get("/metrics", func(c *fiber.Ctx) error {
		ip := c.IP()
		isLoopback := ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
		expectedToken := strings.TrimSpace(os.Getenv("METRICS_TOKEN"))
		if !isLoopback {
			if expectedToken == "" {
				return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: metrics token required for non-loopback access")
			}
			authHdr := c.Get("Authorization")
			if !strings.HasPrefix(authHdr, "Bearer ") || strings.TrimPrefix(authHdr, "Bearer ") != expectedToken {
				return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: invalid metrics token")
			}
		}
		var buf bytes.Buffer
		_ = metrics.DefaultRegistry.WritePrometheus(&buf)
		c.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		return c.Send(buf.Bytes())
	})
	v1 := app.Group(APIPrefix)
	if m.System != nil {
		if m.AuthMiddleware != nil {
			m.System.Register(v1, m.AuthMiddleware, rbac.RequirePermission("system.info.view"))
		} else {
			m.System.Register(v1)
		}
	}
	if m.Diagnostics != nil {
		m.Diagnostics.Register(v1)
	}
	if m.Auth != nil {
		m.Auth.Register(v1, m.AuthMiddleware)
	}
	if m.Org != nil {
		m.Org.Register(v1, m.AuthMiddleware)
	}
	if m.RBAC != nil {
		m.RBAC.Register(v1, m.AuthMiddleware)
	}
	if m.Audit != nil {
		m.Audit.Register(v1, m.AuthMiddleware, rbac.RequirePermission("audit.log.view"))
	}
	md := v1.Group("/master-data")
	if m.Country != nil {
		m.Country.Register(v1, m.AuthMiddleware)
		m.Country.Register(md, m.AuthMiddleware)
	}
	if m.Operator != nil {
		m.Operator.Register(v1, m.AuthMiddleware)
		m.Operator.Register(md, m.AuthMiddleware)
	}
	if m.Prefix != nil {
		m.Prefix.Register(v1, m.AuthMiddleware)
		m.Prefix.Register(md, m.AuthMiddleware)
	}
	if m.Approval != nil {
		m.Approval.Register(v1, m.AuthMiddleware)
		m.Approval.Register(md, m.AuthMiddleware)
	}
	if m.ImportJob != nil {
		m.ImportJob.Register(v1, m.AuthMiddleware)
		m.ImportJob.Register(md, m.AuthMiddleware)
	}
	if m.Number != nil {
		m.Number.Register(v1, m.AuthMiddleware)
		m.Number.Register(md, m.AuthMiddleware)
	}
	if m.Provider != nil {
		m.Provider.Register(v1, m.AuthMiddleware)
	}
	if m.Customer != nil {
		m.Customer.Register(v1, m.AuthMiddleware)
	}
	if m.SenderID != nil {
		m.SenderID.Register(v1, m.AuthMiddleware)
	}
	if m.Rating != nil {
		m.Rating.Register(v1, m.AuthMiddleware)
	}
	if m.Ledger != nil {
		m.Ledger.Register(v1, m.AuthMiddleware)
	}
	if m.Billing != nil {
		m.Billing.Register(v1, m.AuthMiddleware)
	}
	if m.Routing != nil {
		m.Routing.Register(v1, m.AuthMiddleware)
	}
	if m.Messaging != nil {
		m.Messaging.RegisterRoutes(v1, m.AuthMiddleware)
	}
	if m.CustomerEdge != nil {
		m.CustomerEdge.RegisterRoutes(v1)
	}
	if m.HTTPProviderCallback != nil {
		m.HTTPProviderCallback.Register(v1)
	}
	return app
}
