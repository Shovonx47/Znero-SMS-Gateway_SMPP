package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/logx"
)

// RequestIDHeader is read from the request (when well-formed) and always set on the response.
const RequestIDHeader = "X-Request-ID"

// An inbound ID is only trusted when it matches this allow-list; anything else is replaced.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the OS entropy source is broken; fail loudly.
		panic(fmt.Errorf("generate request id: %w", err))
	}
	return hex.EncodeToString(b[:])
}

// RequestIDMiddleware assigns every request an ID, returns it in the X-Request-ID response header and stores
// it in the request context so every log line written with that context includes it.
func RequestIDMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		c.Set(RequestIDHeader, id)
		c.SetUserContext(logx.WithRequestID(c.UserContext(), id))
		return c.Next()
	}
}

// AccessLog writes one structured line per request. It runs the error handler itself so the final
// status code is known when the line is written. Paths in quiet are logged at debug level so that
// health probes do not flood the log.
func AccessLog(logger *slog.Logger, quiet ...string) fiber.Handler {
	quietSet := make(map[string]struct{}, len(quiet))
	for _, p := range quiet {
		quietSet[p] = struct{}{}
	}
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if err != nil {
			if hErr := c.App().ErrorHandler(c, err); hErr != nil {
				_ = c.SendStatus(http.StatusInternalServerError)
			}
			err = nil
		}
		status := c.Response().StatusCode()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		path := c.Path() // never the query string: it may carry sensitive values
		if _, ok := quietSet[path]; ok {
			level = slog.LevelDebug
		}
		logger.LogAttrs(c.UserContext(), level, "http request",
			slog.String("method", c.Method()),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("ip", c.IP()),
			slog.Int("bytes", len(c.Response().Body())),
		)
		return err
	}
}

// Recover converts a panic in a handler into a logged 500 in the standard error format.
func Recover(logger *slog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(c.UserContext(), "panic recovered",
					slog.String("panic", fmt.Sprint(r)),
					slog.String("stack", string(debug.Stack())),
				)
				err = Internal(fmt.Errorf("panic: %v", r))
			}
		}()
		return c.Next()
	}
}

// SecurityHeaders sets conservative response headers suitable for a JSON-only API.
func SecurityHeaders() fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		c.Set("Cross-Origin-Resource-Policy", "same-origin")
		c.Set("Cache-Control", "no-store")
		return c.Next()
	}
}

// AllowMethods is a default-deny HTTP method allow-list.
func AllowMethods(methods ...string) fiber.Handler {
	allowed := make(map[string]struct{}, len(methods))
	for _, m := range methods {
		allowed[strings.ToUpper(m)] = struct{}{}
	}
	allowHeader := strings.Join(methods, ", ")
	return func(c *fiber.Ctx) error {
		if _, ok := allowed[c.Method()]; !ok {
			c.Set(fiber.HeaderAllow, allowHeader)
			return NewError(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "This method is not allowed.")
		}
		return c.Next()
	}
}
