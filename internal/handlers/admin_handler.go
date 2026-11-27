package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/realtime"
	"github.com/venu-live/venu-api/internal/services"
)

type AdminHandler struct {
	svc    *services.AdminService
	hub    *realtime.AdminHub
	logger *slog.Logger
}

func NewAdminHandler(svc *services.AdminService, logger *slog.Logger) *AdminHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminHandler{svc: svc, logger: logger}
}

func (h *AdminHandler) SetHub(hub *realtime.AdminHub) {
	h.hub = hub
}

// @Summary      Create staff account
// @Description  Creates an ADMIN, SCANNER, or BARTENDER account.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        payload body services.CreateStaffRequest true "Staff account"
// @Success      201 {object} services.StaffAccountView
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      409 {object} httpx.ErrorPayload "username already exists"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/staff [post]
func (h *AdminHandler) CreateStaff(w http.ResponseWriter, r *http.Request) {
	var req services.CreateStaffRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	staff, err := h.svc.CreateStaff(r.Context(), req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, staff)
}

type updatePasswordRequest struct {
	Password string `json:"password"`
}

// @Summary      Update staff password
// @Description  Updates a staff password and revokes all previously issued sessions.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Staff UUID"
// @Param        payload body updatePasswordRequest true "New password"
// @Success      200 {object} services.StaffAccountView
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "staff account not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/staff/{id}/password [patch]
func (h *AdminHandler) UpdateStaffPassword(w http.ResponseWriter, r *http.Request) {
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid staff id", err))
		return
	}

	var req updatePasswordRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	staff, err := h.svc.UpdateStaffPassword(r.Context(), staffID, req.Password)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, staff)
}

// @Summary      List all staff
// @Description  Lists all staff accounts with their roles and status.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Success      200 {array} services.StaffAccountView
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/staff [get]
func (h *AdminHandler) ListStaff(w http.ResponseWriter, r *http.Request) {
	staff, err := h.svc.ListStaff(r.Context())
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, staff)
}

// @Summary      Deactivate staff account
// @Description  Deactivates a staff account and revokes all previously issued sessions.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Staff UUID"
// @Success      200 {object} services.StaffAccountView
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role or self-deactivation"
// @Failure      404 {object} httpx.ErrorPayload "staff account not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/staff/{id} [delete]
func (h *AdminHandler) DeactivateStaff(w http.ResponseWriter, r *http.Request) {
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid staff id", err))
		return
	}

	actorID, ok := middleware.StaffIDFromContext(r.Context())
	if !ok {
		httpx.HandleError(w, r, h.logger, httpx.NewUnauthorized("authentication required", nil))
		return
	}

	staff, err := h.svc.DeactivateStaff(r.Context(), staffID, actorID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, staff)
}

// @Summary      List bookings for verification
// @Description  Returns the manual UPI verification queue with UTRs and expected amounts.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        status query string false "Booking status (default PENDING)"
// @Param        event_id query string false "Optional event UUID filter"
// @Param        limit query int32 false "Page size (default 50)"
// @Param        offset query int32 false "Page offset"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/bookings [get]
func (h *AdminHandler) ListBookingsForVerification(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := db.BookingStatus(strings.ToUpper(strings.TrimSpace(q.Get("status"))))

	var eventID uuid.UUID
	if rawEv := strings.TrimSpace(q.Get("event_id")); rawEv != "" {
		parsed, err := uuid.Parse(rawEv)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event_id query parameter", err))
			return
		}
		eventID = parsed
	}

	limit := int32(50)
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil {
			limit = int32(n)
		}
	}
	offset := int32(0)
	if rawOffset := strings.TrimSpace(q.Get("offset")); rawOffset != "" {
		if n, err := strconv.Atoi(rawOffset); err == nil {
			offset = int32(n)
		}
	}

	items, err := h.svc.ListBookingsForVerification(r.Context(), status, eventID, limit, offset)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"bookings": items,
		"count":    len(items),
	})
}

// @Summary      Verify booking payment
// @Description  Atomically approves or rejects a pending booking and queues WhatsApp delivery on approval.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Booking UUID"
// @Param        payload body services.VerifyBookingRequest true "APPROVE or REJECT decision"
// @Success      200 {object} services.VerifyBookingResponse
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "booking not found"
// @Failure      409 {object} httpx.ErrorPayload "booking is no longer pending"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/bookings/{id}/verify [post]
func (h *AdminHandler) VerifyBookingPayment(w http.ResponseWriter, r *http.Request) {
	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid booking id", err))
		return
	}

	adminID, ok := middleware.StaffIDFromContext(r.Context())
	if !ok {
		httpx.HandleError(w, r, h.logger, httpx.NewUnauthorized("authentication required", nil))
		return
	}

	var req services.VerifyBookingRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	resp, err := h.svc.VerifyBookingPayment(r.Context(), bookingID, adminID, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

// @Summary      Create event
// @Description  Creates a ticketing event with status DRAFT, ONSALE, SOLDOUT, or ENDED.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        payload body services.CreateEventRequest true "Event details"
// @Success      201 {object} db.Event
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events [post]
func (h *AdminHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var req services.CreateEventRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	ev, err := h.svc.CreateEvent(r.Context(), req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, ev)
}

// @Summary      List admin events
// @Description  Returns all events with confirmed booking, revenue, and check-in totals.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Success      200 {object} map[string]interface{}
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events [get]
func (h *AdminHandler) ListAdminEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.svc.ListAdminEvents(r.Context())
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"events": events, "count": len(events)})
}

// @Summary      Soft delete event
// @Description  Hides an event from public and admin event reads without removing its bookings.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Event UUID"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "event not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id} [delete]
func (h *AdminHandler) SoftDeleteEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	if err := h.svc.SoftDeleteEvent(r.Context(), id); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"message": "event deleted"})
}

type setFirst99ActiveRequest struct {
	IsFirst99Active bool `json:"is_first_99_active"`
}

// @Summary      Toggle first-99 female pricing
// @Description  Enables or disables the first-99 female pricing rule for an event.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Event UUID"
// @Param        payload body setFirst99ActiveRequest true "Pricing override"
// @Success      200 {object} db.SetEventFirst99ActiveRow
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "event not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id}/first-99 [patch]
func (h *AdminHandler) SetEventFirst99Active(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}

	var req setFirst99ActiveRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	row, err := h.svc.SetEventFirst99Active(r.Context(), id, req.IsFirst99Active)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, row)
}

// @Summary      List API audit logs
// @Description  Returns structured API call audit records; request bodies and secrets are never stored.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        actor_id query string false "Optional staff UUID filter"
// @Param        path query string false "Optional request path substring filter"
// @Param        limit query int false "Page size (default 50)"
// @Param        offset query int false "Page offset"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/audit-logs [get]
func (h *AdminHandler) ListAPIAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var filter services.AuditLogFilter
	if raw := strings.TrimSpace(q.Get("actor_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid actor_id query parameter", err))
			return
		}
		filter.ActorID = parsed
	}
	filter.Path = q.Get("path")
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid limit", err))
			return
		}
		filter.Limit = int32(parsed)
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid offset", err))
			return
		}
		filter.Offset = int32(parsed)
	}

	logs, err := h.svc.ListAPIAuditLogs(r.Context(), filter)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"logs": logs, "count": len(logs)})
}

// @Summary      Create ticket tier
// @Description  Adds a category/tier to an existing event.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Event UUID"
// @Param        payload body services.CreateTicketTierRequest true "Ticket tier"
// @Success      201 {object} db.TicketCategory
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "event not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id}/tiers [post]
func (h *AdminHandler) CreateTicketTier(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}

	var req services.CreateTicketTierRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	tier, err := h.svc.CreateTicketTier(r.Context(), eventID, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, tier)
}

// @Summary      Get event stats
// @Description  Returns ticketing, gate, F&B wallet, gender, top-item, bartender, and recent-entry analytics for an event.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        event_id path string true "Event UUID"
// @Success      200 {object} services.EventStatsView
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "event not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/stats/{event_id} [get]
func (h *AdminHandler) GetEventStats(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event_id", err))
		return
	}

	stats, err := h.svc.GetEventStats(r.Context(), eventID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stats)
}

// @Summary      Get aggregate admin stats
// @Description  Returns landing-page KPIs, per-event performance, top bar items, and recent gate entries across active events.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Success      200 {object} services.AdminStatsView
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/stats [get]
func (h *AdminHandler) GetAdminStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.GetAdminStats(r.Context())
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stats)
}

// @Summary      List customer help queries
// @Description  Returns customer support inquiries with optional resolved filter and pagination.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        resolved query boolean false "Filter by resolved status"
// @Param        limit query integer false "Page limit (default 50)"
// @Param        offset query integer false "Page offset (default 0)"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/help [get]
func (h *AdminHandler) ListHelpMessages(w http.ResponseWriter, r *http.Request) {
	var resolved *bool
	if raw := r.URL.Query().Get("resolved"); raw != "" {
		b := raw == "true" || raw == "1"
		resolved = &b
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = int32(n)
		}
	}
	offset := int32(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	msgs, err := h.svc.ListHelpMessages(r.Context(), resolved, limit, offset)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"messages": msgs, "count": len(msgs)})
}

// @Summary      Resolve customer help query
// @Description  Marks a customer support inquiry as resolved by the authenticated admin.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Help Message UUID"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "help message not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/help/{id}/resolve [post]
func (h *AdminHandler) ResolveHelpMessage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid help message id", err))
		return
	}
	adminID, ok := middleware.StaffIDFromContext(r.Context())
	if !ok {
		httpx.HandleError(w, r, h.logger, httpx.NewUnauthorized("admin identity required", nil))
		return
	}
	msg, err := h.svc.ResolveHelpMessage(r.Context(), id, adminID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, msg)
}

// @Summary      Update event details
// @Description  Updates event title, description, timings, status, venue, area, transit, and advisory.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id   path string true "Event UUID"
// @Param        payload body services.UpdateEventRequest true "Event update payload"
// @Success      200 {object} db.Event
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id} [put]
func (h *AdminHandler) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	var req services.UpdateEventRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	ev, err := h.svc.UpdateEvent(r.Context(), id, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, ev)
}

// @Summary      Update ticket tier
// @Description  Updates name, category, prices, entry count, and gender-strict flag for an existing ticket tier.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Tier UUID"
// @Param        payload body services.UpdateTicketTierRequest true "Tier payload"
// @Success      200 {object} db.TicketCategory
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/tiers/{id} [patch]
func (h *AdminHandler) UpdateTicketTier(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid tier id", err))
		return
	}
	var req services.UpdateTicketTierRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	tier, err := h.svc.UpdateTicketTier(r.Context(), id, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, tier)
}

// @Summary      Update tier price
// @Description  Updates base price and fnb credit for an existing ticket category.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id   path string true "Tier UUID"
// @Param        payload body services.UpdateTierPriceRequest true "Price payload"
// @Success      200 {object} db.TicketCategory
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/tiers/{id}/price [patch]
func (h *AdminHandler) UpdateTierPrice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid tier id", err))
		return
	}
	var req services.UpdateTierPriceRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	tier, err := h.svc.UpdateTierPrice(r.Context(), id, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, tier)
}

// @Summary      Create event artist
// @Description  Adds a performing artist, headliner, or DJ to an event.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id   path string true "Event UUID"
// @Param        payload body services.CreateArtistRequest true "Artist payload"
// @Success      201 {object} db.EventArtist
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id}/artists [post]
func (h *AdminHandler) CreateArtist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	var req services.CreateArtistRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	artist, err := h.svc.CreateArtist(r.Context(), id, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, artist)
}

// @Summary      List event artists
// @Description  Lists all artists linked to an event.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id   path string true "Event UUID"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id}/artists [get]
func (h *AdminHandler) ListArtists(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	artists, err := h.svc.ListArtists(r.Context(), id)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"artists": artists, "count": len(artists)})
}

// @Summary      Delete event artist
// @Description  Removes an artist from an event.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id   path string true "Artist UUID"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/artists/{id} [delete]
func (h *AdminHandler) DeleteArtist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid artist id", err))
		return
	}
	if err := h.svc.DeleteArtist(r.Context(), id); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"message": "artist deleted"})
}

// @Summary      Admin real-time WebSocket
// @Description  Opens a WebSocket stream for live notifications on new bookings and submitted UTRs.
// @Tags         Admin
// @Security     BearerAuth, CookieAuth
// @Router       /admin/ws [get]
func (h *AdminHandler) AdminSocket(w http.ResponseWriter, r *http.Request) {
	checkOrigin := func(req *http.Request) bool {
		if h.hub != nil {
			return h.hub.CheckOrigin(req)
		}
		return true
	}
	upgrader := websocket.Upgrader{CheckOrigin: checkOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("admin_websocket_upgrade_failed", "error", err)
		return
	}
	if h.hub == nil {
		_ = conn.WriteJSON(map[string]any{"type": "CONNECTED", "message": "admin live notification stream connected"})
		_ = conn.Close()
		return
	}
	client := h.hub.Subscribe(conn)
	defer client.Unsubscribe()

	go client.WritePump()
	client.Send([]byte(`{"type":"CONNECTED","message":"admin live notification stream connected"}`))

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *AdminHandler) ListCheckoutLeads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	includeCompleted := strings.EqualFold(q.Get("include_completed"), "true")
	var eventID uuid.UUID
	if raw := strings.TrimSpace(q.Get("event_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil { httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event_id", err)); return }
		eventID = parsed
	}
	limit := int32(10)
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" { if n, err := strconv.Atoi(raw); err == nil { limit = int32(n) } }
	var cursorUpdatedAt *time.Time
	var cursorID uuid.UUID
	if raw := strings.TrimSpace(q.Get("cursor")); raw != "" {
		parts := strings.SplitN(raw, "|", 2)
		if len(parts) == 2 {
			if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
				if id, err := uuid.Parse(parts[1]); err == nil {
					cursorUpdatedAt = &t
					cursorID = id
				}
			}
		}
	}
	page, err := h.svc.ListCheckoutLeads(r.Context(), includeCompleted, eventID, q.Get("q"), limit, cursorUpdatedAt, cursorID)
	if err != nil { httpx.HandleError(w, r, h.logger, err); return }
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"leads": page.Leads, "count": page.Count, "next_cursor": page.NextCursor, "has_more": page.HasMore})
}
