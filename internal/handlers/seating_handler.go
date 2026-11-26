package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/realtime"
	seating "github.com/venu-live/venu-api/internal/services/seating"
)

type configureSeatingRequest struct {
	Rows []seating.SeatConfigInput `json:"rows"`
}

type SeatingHandler struct {
	svc            *seating.Service
	hub            *realtime.SeatHub
	allowedOrigins []string
	logger         *slog.Logger
}

func NewSeatingHandler(svc *seating.Service, hub *realtime.SeatHub, allowedOrigins []string, logger *slog.Logger) *SeatingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SeatingHandler{svc: svc, hub: hub, allowedOrigins: allowedOrigins, logger: logger}
}

// @Summary      List event seats
// @Description  Public snapshot of section A/B/C table inventory, prices, and live booked state.
// @Tags         Events
// @Produce      json
// @Param        id path string true "Event UUID"
// @Success      200 {object} seating.SeatSnapshot
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /events/{id}/seats [get]
func (h *SeatingHandler) GetSeats(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	snapshot, err := h.svc.Snapshot(r.Context(), eventID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snapshot)
}

// @Summary      Subscribe to seat updates
// @Description  Public websocket stream; emits a full SeatSnapshot immediately and after every reservation/config change.
// @Tags         Events
// @Produce      json
// @Param        id path string true "Event UUID"
// @Success      101 {string} string "websocket upgrade"
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Router       /events/{id}/seats/ws [get]
func (h *SeatingHandler) SeatSocket(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	snapshot, err := h.svc.Snapshot(r.Context(), eventID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	upgrader := websocket.Upgrader{CheckOrigin: h.checkOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("seat_websocket_upgrade", "event_id", eventID, "error", err)
		return
	}
	client := h.hub.Subscribe(eventID, conn)
	if err := conn.WriteJSON(snapshot); err != nil {
		client.Unsubscribe()
		return
	}
	go client.WritePump()
	client.ReadPump()
}

// @Summary      Configure event seating
// @Description  Sets table counts and table totals for sections A/B/C and table sizes 5/8/10/15.
// @Tags         Admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Event UUID"
// @Param        payload body configureSeatingRequest true "Seating configuration"
// @Success      200 {object} seating.SeatSnapshot
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload
// @Failure      409 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/events/{id}/seating [put]
func (h *SeatingHandler) ConfigureSeating(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}
	var req configureSeatingRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	snapshot, err := h.svc.Configure(r.Context(), eventID, req.Rows)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snapshot)
}

func (h *SeatingHandler) checkOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	for _, allowed := range h.allowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}
