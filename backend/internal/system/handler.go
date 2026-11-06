package system

import (
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/httpx"
)

const codeNotMigrated = "database_not_migrated"

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts the routes on the /api/v1 router.
func (h *Handler) Register(v1 fiber.Router, middleware ...fiber.Handler) {
	if len(middleware) > 0 {
		handlers := make([]fiber.Handler, len(middleware)+1)
		copy(handlers, middleware)
		handlers[len(middleware)] = h.info
		v1.Get("/system/info", handlers...)
	} else {
		v1.Get("/system/info", h.info)
	}
}

// info returns build and schema information.
// TODO(security): this endpoint is unauthenticated because Phase 1 has no authentication yet.
// Once auth exists it must require a signed-in user with an explicit permission (default deny).
func (h *Handler) info(c *fiber.Ctx) error {
	info, err := h.svc.Info(c.UserContext())
	if err != nil {
		if errors.Is(err, ErrNotMigrated) {
			return httpx.NewError(http.StatusServiceUnavailable, codeNotMigrated,
				"Database migrations have not been applied. Run scripts/migrate-up.ps1.").WithCause(err)
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, info)
}
