package health

import (
	"net/http"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/httpx"
)

// Handler exposes /healthz and /readyz (unversioned by convention: they are infrastructure endpoints).
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts the routes on r.
func (h *Handler) Register(r fiber.Router) {
	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)
}

// healthz: the process is alive. Always 200 while the server can answer.
func (h *Handler) healthz(c *fiber.Ctx) error {
	return httpx.OK(c, h.svc.Live())
}

// readyz: 200 only if every dependency is up; otherwise 503 in the standard error format with the
// per-dependency report in error.details, so clients can see which one is down.
func (h *Handler) readyz(c *fiber.Ctx) error {
	report := h.svc.Ready(c.UserContext())
	if report.Ready() {
		return httpx.OK(c, report)
	}
	return httpx.NewError(http.StatusServiceUnavailable, httpx.CodeNotReady,
		"One or more dependencies are unavailable.").WithDetails(report)
}
