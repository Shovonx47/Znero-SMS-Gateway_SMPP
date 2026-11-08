package httpx

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// AppConfig configures the shared Fiber app (middleware stack, timeouts, limits).
type AppConfig struct {
	Name           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	BodyLimitBytes int

	// AllowedMethods is the default-deny method allow-list.
	AllowedMethods []string
	// CORSOrigins is an exact-match allow-list. Empty means no cross-origin access at all.
	CORSOrigins []string

	// RateLimitMax requests per RateLimitWindow per client IP. RateLimitMax <= 0 disables the limiter.
	RateLimitMax    int
	RateLimitWindow time.Duration

	// QuietPaths are logged at debug level and exempt from rate limiting (health probes).
	QuietPaths []string
}

// NewApp builds a Fiber app with the standard middleware chain:
// request ID -> access log -> panic recovery -> security headers -> method allow-list -> CORS -> rate limit.
func NewApp(cfg AppConfig, logger *slog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               cfg.Name,
		ServerHeader:          "",
		DisableStartupMessage: true,
		Immutable:             true, // handlers may keep strings from the request safely
		ReadTimeout:           cfg.ReadTimeout,
		WriteTimeout:          cfg.WriteTimeout,
		IdleTimeout:           cfg.IdleTimeout,
		ReadBufferSize:        32 * 1024,
		WriteBufferSize:       32 * 1024,
		BodyLimit:             cfg.BodyLimitBytes,
		ErrorHandler:          ErrorHandler(logger),
	})

	app.Use(RequestIDMiddleware())
	app.Use(AccessLog(logger, cfg.QuietPaths...))
	app.Use(Recover(logger))
	app.Use(SecurityHeaders())
	app.Use(AllowMethods(cfg.AllowedMethods...))

	// fiber's CORS middleware treats an empty origin list as "*", so only install it when
	// an explicit allow-list exists.
	if len(cfg.CORSOrigins) > 0 {
		app.Use(cors.New(cors.Config{
			AllowOrigins:     strings.Join(cfg.CORSOrigins, ","),
			AllowMethods:     strings.Join(cfg.AllowedMethods, ","),
			AllowHeaders:     "Content-Type," + RequestIDHeader,
			ExposeHeaders:    RequestIDHeader,
			AllowCredentials: false,
			MaxAge:           600,
		}))
	}

	if cfg.RateLimitMax > 0 {
		quiet := make(map[string]struct{}, len(cfg.QuietPaths))
		for _, p := range cfg.QuietPaths {
			quiet[p] = struct{}{}
		}
		app.Use(limiter.New(limiter.Config{
			Max:        cfg.RateLimitMax,
			Expiration: cfg.RateLimitWindow,
			Next: func(c *fiber.Ctx) bool {
				_, skip := quiet[c.Path()]
				return skip
			},
			KeyGenerator: func(c *fiber.Ctx) string { return c.IP() },
			LimitReached: func(c *fiber.Ctx) error {
				return NewError(fiber.StatusTooManyRequests, CodeRateLimited, "Too many requests. Please retry later.")
			},
		}))
	}

	// Unmatched routes make Fiber return a 404 *fiber.Error from c.Next(); ErrorHandler renders it
	// in the standard error format (see toAppError).
	return app
}

// CredentialRateLimiter returns a Fiber middleware configured to rate-limit sensitive credential endpoints.
func CredentialRateLimiter(max int, window time.Duration) fiber.Handler {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: window,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP() + ":" + c.Path()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return NewError(fiber.StatusTooManyRequests, CodeRateLimited, "Too many credential operations. Please retry later.")
		},
	})
}

// MoneyRateLimiter returns a Fiber middleware configured to rate-limit financial/money-moving endpoints.
func MoneyRateLimiter(max int, window time.Duration) fiber.Handler {
	if max <= 0 {
		max = 30
	}
	if window <= 0 {
		window = time.Minute
	}
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: window,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP() + ":" + c.Method() + ":" + c.Path()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return NewError(fiber.StatusTooManyRequests, CodeRateLimited, "Too many financial operations. Rate limit exceeded, please retry later.")
		},
	})
}

// RateLimiter returns a generic configurable rate-limiting Fiber middleware.
func RateLimiter(max int, window time.Duration, msg string) fiber.Handler {
	if max <= 0 {
		max = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	if msg == "" {
		msg = "Rate limit exceeded. Please retry later."
	}
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: window,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP() + ":" + c.Method() + ":" + c.Path()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return NewError(fiber.StatusTooManyRequests, CodeRateLimited, msg)
		},
	})
}
