package approval

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	group := router.Group("/change-requests", authMiddleware)
	group.Get("", rbac.RequireAnyPermission("country.country.view", "operator.operator.view", "number.prefix.view"), h.list)
	group.Get("/:id", rbac.RequireAnyPermission("country.country.view", "operator.operator.view", "number.prefix.view"), h.get)
	group.Post("/:id/approve", rbac.RequireAnyPermission("country.country.approve", "operator.operator.approve", "number.prefix.approve", "number.rule.approve"), h.approve)
	group.Post("/:id/reject", rbac.RequireAnyPermission("country.country.approve", "operator.operator.approve", "number.prefix.approve", "number.rule.approve"), h.reject)
	group.Post("/:id/cancel", h.cancel)
}

func (h *Handler) list(c *fiber.Ctx) error {
	if tc, err := tenant.FromContext(c.UserContext()); err != nil {
		return httpx.Unauthorized("Authentication required")
	} else if !tc.IsPlatformOrg() {
		return httpx.Forbidden("Change requests are visible to the platform organization only")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	filter := ChangeRequestFilter{
		EntityType: c.Query("entity_type"),
		Status:     c.Query("status"),
		Page:       page,
		PageSize:   pageSize,
	}

	res, err := h.svc.List(c.UserContext(), filter)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) get(c *fiber.Ctx) error {
	if tc, err := tenant.FromContext(c.UserContext()); err != nil {
		return httpx.Unauthorized("Authentication required")
	} else if !tc.IsPlatformOrg() {
		return httpx.Forbidden("Change requests are visible to the platform organization only")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid change request ID")
	}

	cr, err := h.svc.Get(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, ErrChangeRequestNotFound) {
			return httpx.NotFound("Change request not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, cr)
}

func (h *Handler) approve(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid change request ID")
	}

	var req ApproveRequest
	_ = c.BodyParser(&req)

	if err := h.svc.Approve(c.UserContext(), tc, id, req.Reason); err != nil {
		if errors.Is(err, ErrMakerCannotApproveOwnChange) {
			return httpx.Forbidden("You cannot approve your own change request (Maker-Checker violation)")
		}
		if errors.Is(err, ErrNotAuthorizedToApprove) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrChangeRequestNotFound) {
			return httpx.NotFound("Change request not found")
		}
		if errors.Is(err, ErrInvalidStatus) {
			return httpx.BadRequest("Change request is not in pending status")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"message": "Change request approved and applied successfully"})
}

func (h *Handler) reject(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid change request ID")
	}

	var req RejectRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	if err := h.svc.Reject(c.UserContext(), tc, id, req.Reason); err != nil {
		if errors.Is(err, ErrMakerCannotApproveOwnChange) {
			return httpx.Forbidden("You cannot reject your own change request; cancel it instead")
		}
		if errors.Is(err, ErrNotAuthorizedToApprove) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrEmptyRejectReason) {
			return httpx.BadRequest("Rejection reason is required")
		}
		if errors.Is(err, ErrChangeRequestNotFound) {
			return httpx.NotFound("Change request not found")
		}
		if errors.Is(err, ErrInvalidStatus) {
			return httpx.BadRequest("Change request is not in pending status")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"message": "Change request rejected"})
}

func (h *Handler) cancel(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid change request ID")
	}

	if err := h.svc.Cancel(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrChangeRequestNotFound) {
			return httpx.NotFound("Change request not found")
		}
		if errors.Is(err, ErrInvalidStatus) {
			return httpx.BadRequest("Change request is not in pending status")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"message": "Change request cancelled"})
}
