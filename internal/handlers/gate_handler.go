package handlers

import (
	"log/slog"
	"net/http"

	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/services"
)

type GateHandler struct {
	svc    *services.GateService
	logger *slog.Logger
}

func NewGateHandler(svc *services.GateService, logger *slog.Logger) *GateHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &GateHandler{svc: svc, logger: logger}
}

type qrScanRequest struct {
	QRToken string `json:"qr_token"`
}

// @Summary      Validate gate QR
// @Description  Verifies a signed gate QR token and returns admission details without mutating state.
// @Tags         Gate
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        payload body qrScanRequest true "Gate QR token"
// @Success      200 {object} services.GateTicketView
// @Failure      400 {object} httpx.ErrorPayload "invalid or expired QR token"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /gate/validate [post]
func (h *GateHandler) ValidateQR(w http.ResponseWriter, r *http.Request) {
	var req qrScanRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	view, err := h.svc.ValidateQR(r.Context(), req.QRToken)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

// @Summary      Check in ticket
// @Description  Atomically checks in a confirmed ticket and immediately expires its single-use gate QR.
// @Tags         Gate
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        payload body qrScanRequest true "Gate QR token"
// @Success      200 {object} services.GateTicketView
// @Failure      400 {object} httpx.ErrorPayload "invalid or expired QR token"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      409 {object} httpx.ErrorPayload "payment unconfirmed, event ended, or QR already used"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /gate/check-in [post]
func (h *GateHandler) CheckInTicket(w http.ResponseWriter, r *http.Request) {
	var req qrScanRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	gatekeeperID, _ := middleware.StaffIDFromContext(r.Context())
	view, err := h.svc.CheckInTicket(r.Context(), req.QRToken, gatekeeperID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}
