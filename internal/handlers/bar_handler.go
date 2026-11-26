package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/services"
)

type BarHandler struct {
	svc    *services.BarService
	logger *slog.Logger
}

func NewBarHandler(svc *services.BarService, logger *slog.Logger) *BarHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &BarHandler{svc: svc, logger: logger}
}

// @Summary      List bar menu
// @Description  Returns all active food and drink menu items.
// @Tags         Bar
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Success      200 {object} map[string]interface{}
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/menu [get]
func (h *BarHandler) GetMenu(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.GetMenu(r.Context())
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"items": items})
}

// @Summary      Create bar menu item
// @Description  Adds a new active food or drink item to the bar menu.
// @Tags         Bar
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        payload body services.CreateMenuItemRequest true "Menu item"
// @Success      201 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/menu [post]
func (h *BarHandler) CreateMenuItem(w http.ResponseWriter, r *http.Request) {
	var req services.CreateMenuItemRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	item, err := h.svc.CreateMenuItem(r.Context(), req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, item)
}

// @Summary      Update bar menu item
// @Description  Updates the name, category, price and serving label of an active menu item.
// @Tags         Bar
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Menu item UUID"
// @Param        payload body services.UpdateMenuItemRequest true "Menu item"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "menu item not found"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/menu/{id} [put]
func (h *BarHandler) UpdateMenuItem(w http.ResponseWriter, r *http.Request) {
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid menu item id", err))
		return
	}

	var req services.UpdateMenuItemRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	item, err := h.svc.UpdateMenuItem(r.Context(), itemID, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, item)
}

// @Summary      Delete bar menu item
// @Description  Soft-deletes a menu item while preserving historical transaction audit records.
// @Tags         Bar
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Menu item UUID"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload "invalid menu item id"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "menu item not found"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/menu/{id} [delete]
func (h *BarHandler) DeleteMenuItem(w http.ResponseWriter, r *http.Request) {
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid menu item id", err))
		return
	}

	item, err := h.svc.DeleteMenuItem(r.Context(), itemID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"deleted": true,
		"item":    item,
	})
}

// @Summary      Search ticket by bartender barcode
// @Description  Looks up a confirmed ticket, its guests, wallet balance, and recent bar history.
// @Tags         Bar
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        bar_code query string false "5-character barcode (bar_code takes precedence over code)"
// @Param        code query string false "Alias for bar_code"
// @Success      200 {object} services.BarTicketSearchView
// @Failure      400 {object} httpx.ErrorPayload "invalid barcode"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/search [get]
func (h *BarHandler) SearchTicketByBarCode(w http.ResponseWriter, r *http.Request) {
	barCode := strings.TrimSpace(r.URL.Query().Get("bar_code"))
	if barCode == "" {
		barCode = strings.TrimSpace(r.URL.Query().Get("code"))
	}

	view, err := h.svc.SearchTicketByBarCode(r.Context(), barCode)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

// @Summary      Get ticket bar history
// @Description  Returns recent audited bar transactions for a ticket barcode.
// @Tags         Bar
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        bar_code path string true "5-character barcode"
// @Param        limit query int32 false "Maximum transactions to return (default 50)"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload "invalid barcode"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/tickets/{bar_code}/history [get]
func (h *BarHandler) GetTicketBarHistory(w http.ResponseWriter, r *http.Request) {
	barCode := strings.TrimSpace(chi.URLParam(r, "bar_code"))
	limit := int32(50)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = int32(n)
		}
	}

	history, err := h.svc.GetTicketBarHistory(r.Context(), barCode, limit)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"bar_code":     strings.ToUpper(barCode),
		"transactions": history,
	})
}

// @Summary      Redeem bar items
// @Description  Atomically prices items, optionally redeems a free shot, deducts wallet balance, and records the audited transaction.
// @Tags         Bar
// @Accept       json
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        Idempotency-Key header string false "Optional retry-safe key bound to barcode, items, and quantities"
// @Param        payload body services.RedeemItemsRequest true "Barcode, items, and redemption options"
// @Success      200 {object} services.RedeemItemsResponse
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      409 {object} httpx.ErrorPayload "unconfirmed ticket, expired wallet, concurrent update, or ineligible free shot"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /counter/redeem [post]
func (h *BarHandler) RedeemItems(w http.ResponseWriter, r *http.Request) {
	bartenderID, ok := middleware.StaffIDFromContext(r.Context())
	if !ok {
		httpx.HandleError(w, r, h.logger, httpx.NewUnauthorized("authentication required", nil))
		return
	}

	var req services.RedeemItemsRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	resp, err := h.svc.RedeemItems(r.Context(), bartenderID, idemKey, req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}
