package handlers

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/services"
)

type TicketingHandler struct {
	cfg    *config.Config
	svc    *services.TicketingService
	tokens *auth.TokenManager
	logger *slog.Logger
}

func NewTicketingHandler(cfg *config.Config, svc *services.TicketingService, tokens *auth.TokenManager, logger *slog.Logger) *TicketingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TicketingHandler{
		cfg:    cfg,
		svc:    svc,
		tokens: tokens,
		logger: logger,
	}
}

// @Summary      Health check
// @Description  Reports service health and PostgreSQL pool statistics.
// @Tags         System
// @Produce      json
// @Success      200 {object} map[string]interface{} "healthy"
// @Failure      503 {object} httpx.ErrorPayload "database unavailable"
// @Router       /health [get]
func (h *TicketingHandler) Health(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Health(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "health check failed", "error", err)
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, httpx.ErrorPayload{
			Error: "database unavailable",
		})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"status": "ok",
		"pool":   h.svc.PoolStats(),
	})
}

// @Summary      List public events
// @Description  Public event listing with tier price breakdown and group seating inventory.
// @Tags         Events
// @Produce      json
// @Param        status query string false "ALL, ON_SALE, SOLD_OUT, or ENDED" default(ON_SALE)
// @Param        limit query int false "Page size" default(50)
// @Param        offset query int false "Page offset" default(0)
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /events [get]
func (h *TicketingHandler) ListPublicEvents(w http.ResponseWriter, r *http.Request) {
	limit := int32(50)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid limit", err))
			return
		}
		limit = int32(parsed)
	}
	offset := int32(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid offset", err))
			return
		}
		offset = int32(parsed)
	}
	events, err := h.svc.ListPublicEvents(r.Context(), r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"events": events, "count": len(events)})
}

// @Summary      Get event details
// @Description  Returns event metadata, dynamic first-99 pricing tiers, and group seating inventory.
// @Tags         Events
// @Produce      json
// @Param        id path string true "Event UUID"
// @Success      200 {object} services.EventDetailsView
// @Failure      400 {object} httpx.ErrorPayload "invalid event id"
// @Failure      404 {object} httpx.ErrorPayload "event not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /events/{id} [get]
func (h *TicketingHandler) GetEventDetails(w http.ResponseWriter, r *http.Request) {
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid event id", err))
		return
	}

	view, err := h.svc.GetEventDetails(r.Context(), eventID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

// @Summary      Get public ticket
// @Description  Returns ticket, booking, and masked guest details; barcodes are omitted until payment is confirmed.
// @Tags         Tickets
// @Produce      json
// @Param        uuid path string true "Ticket UUID"
// @Success      200 {object} services.PublicTicketView
// @Failure      400 {object} httpx.ErrorPayload "invalid ticket uuid"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /tickets/{uuid} [get]
func (h *TicketingHandler) GetTicketByUUID(w http.ResponseWriter, r *http.Request) {
	ticketID, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid ticket uuid", err))
		return
	}

	view, err := h.svc.GetTicketByUUID(r.Context(), ticketID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *TicketingHandler) LookupTicket(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.LookupPublicTicket(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

// @Summary      Search confirmed tickets
// @Description  Finds confirmed ticket passes by contact phone, guest name, or UTR so admins can resend or redownload them.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        q query string false "Phone number, guest name, or UTR"
// @Param        limit query int false "Page size (default 20, max 50)"
// @Param        cursor query string false "Opaque cursor returned by the previous page"
// @Param        wa_pending query boolean false "Only tickets not yet marked WhatsApp sent"
// @Success      200 {object} httpx.Envelope
// @Failure      400 {object} httpx.ErrorPayload "invalid limit or cursor"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/tickets/search [get]
func (h *TicketingHandler) SearchTickets(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 50 {
			httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid limit", err))
			return
		}
		limit = value
	}

	cursor, err := decodeTicketCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid cursor", err))
		return
	}

	waPending := strings.EqualFold(r.URL.Query().Get("wa_pending"), "true")
	page, err := h.svc.SearchTickets(r.Context(), r.URL.Query().Get("q"), waPending, int32(limit), cursor)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"tickets":     page.Tickets,
		"count":       len(page.Tickets),
		"next_cursor": encodeTicketCursor(page.NextCursor),
		"has_more":    page.HasMore,
	})
}

func encodeTicketCursor(cursor services.TicketCursor) string {
	if cursor.ID == uuid.Nil {
		return ""
	}
	raw := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeTicketCursor(raw string) (services.TicketCursor, error) {
	if raw == "" {
		return services.TicketCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return services.TicketCursor{}, err
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return services.TicketCursor{}, fmt.Errorf("unexpected cursor format")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return services.TicketCursor{}, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return services.TicketCursor{}, err
	}
	return services.TicketCursor{CreatedAt: createdAt, ID: id}, nil
}

// @Summary      Mark a ticket's WhatsApp pass as sent
// @Description  Records that the admin has sent the ticket pass on WhatsApp so it drops out of WA Pending.
// @Tags         Admin
// @Produce      json
// @Security     BearerAuth, CookieAuth
// @Param        id path string true "Ticket UUID"
// @Success      200 {object} services.PublicTicketView
// @Failure      400 {object} httpx.ErrorPayload "invalid ticket uuid"
// @Failure      401 {object} httpx.ErrorPayload
// @Failure      403 {object} httpx.ErrorPayload "insufficient role"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /admin/bookings/{id}/wa-sent [patch]
func (h *TicketingHandler) MarkBookingWhatsAppSent(w http.ResponseWriter, r *http.Request) {
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid ticket uuid", err))
		return
	}

	view, err := h.svc.MarkBookingWhatsAppSent(r.Context(), ticketID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, view)
}

// @Summary      Get ticket PNG
// @Description  Renders the confirmed ticket pass as a PNG image.
// @Tags         Tickets
// @Produce      image/png
// @Param        uuid path string true "Ticket UUID"
// @Success      200 {string} string "PNG image bytes"
// @Failure      400 {object} httpx.ErrorPayload "invalid ticket uuid"
// @Failure      404 {object} httpx.ErrorPayload "ticket not found"
// @Failure      409 {object} httpx.ErrorPayload "ticket not confirmed"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /tickets/{uuid}/image [get]
func (h *TicketingHandler) GetTicketImagePNG(w http.ResponseWriter, r *http.Request) {
	ticketID, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid ticket uuid", err))
		return
	}

	pngBytes, err := h.svc.RenderTicketImagePNG(r.Context(), ticketID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="venu-pass-%s.png"`, ticketID.String()[:8]))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}

// @Summary      Initialize guest checkout
// @Description  Create a confirmed UPI checkout booking with guest details and payment instructions
// @Tags         Checkout
// @Accept       json
// @Produce      json
// @Param        payload body services.InitCheckoutRequest true "Checkout request"
// @Param        Idempotency-Key header string true "Client-generated key preventing duplicate checkout submissions"
// @Success      201 {object} services.InitCheckoutResponse
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      404 {object} httpx.ErrorPayload "category not found"
// @Failure      409 {object} httpx.ErrorPayload "duplicate idempotency key or UTR"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /checkout/init [post]
func (h *TicketingHandler) InitGuestCheckout(w http.ResponseWriter, r *http.Request) {
	var req services.InitCheckoutRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 255 {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("Idempotency-Key header is required (8-255 characters)", nil))
		return
	}

	resp, err := h.svc.InitGuestCheckout(r.Context(), req, idempotencyKey)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, resp)
}

type submitUPIReferenceRequest struct {
	UPITransactionID string `json:"upi_transaction_id"`
	ContactPhone     string `json:"contact_phone"`
}

// @Summary      Submit UPI transaction reference
// @Description  Binds a UTR to a pending booking using the original contact_phone as a shared secret.
// @Tags         Checkout
// @Accept       json
// @Produce      json
// @Param        booking_id path string true "Booking UUID"
// @Param        payload body submitUPIReferenceRequest true "UTR and booking contact phone"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload "invalid booking id or UTR"
// @Failure      404 {object} httpx.ErrorPayload "booking not found"
// @Failure      409 {object} httpx.ErrorPayload "duplicate UTR"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /checkout/{booking_id}/upi-reference [post]
func (h *TicketingHandler) SubmitUPITransactionID(w http.ResponseWriter, r *http.Request) {
	bookingID, err := uuid.Parse(chi.URLParam(r, "booking_id"))
	if err != nil {
		httpx.HandleError(w, r, h.logger, httpx.NewBadRequest("invalid booking_id", err))
		return
	}

	var req submitUPIReferenceRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	booking, err := h.svc.SubmitUPITransactionID(r.Context(), bookingID, req.ContactPhone, req.UPITransactionID)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"booking_id":         booking.ID,
		"status":             booking.Status,
		"total_amount":       booking.TotalAmount,
		"upi_transaction_id": booking.UpiTransactionID.String,
		"message":            "UPI transaction reference submitted; awaiting admin verification",
	})
}

type StaffLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// @Summary      Staff Login
// @Description  Authenticate Admin, Scanner, or Bartender
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        payload body StaffLoginRequest true "Login credentials"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /auth/login [post]
func (h *TicketingHandler) StaffLogin(w http.ResponseWriter, r *http.Request) {
	var req StaffLoginRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	session, err := h.svc.StaffLogin(r.Context(), req.Username, req.Password)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	h.setSessionCookies(w, session.Tokens)
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"staff":        session.Staff,
		"access_token": session.Tokens.AccessToken,
		"expires_at":   session.Tokens.ExpiresAt,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// @Summary      Refresh staff session
// @Description  Rotates the staff token version and issues a new access token from a refresh cookie or JSON body.
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        Origin header string false "Allowed browser origin for cookie-based refresh"
// @Param        Sec-Fetch-Site header string false "Browser fetch site header"
// @Param        X-Requested-With header string false "Set by non-cross-site browser clients"
// @Param        payload body refreshRequest false "Refresh token when cookie is unavailable"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      401 {object} httpx.ErrorPayload "refresh token invalid, expired, or reused"
// @Failure      403 {object} httpx.ErrorPayload "cross-site refresh origin rejected"
// @Failure      429 {object} httpx.ErrorPayload "rate limited"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /auth/refresh [post]
func (h *TicketingHandler) RefreshSession(w http.ResponseWriter, r *http.Request) {
	var rawRefresh string
	fromCookie := false
	if cookie, err := r.Cookie(middleware.RefreshCookieName); err == nil && cookie.Value != "" {
		rawRefresh = cookie.Value
		fromCookie = true
	} else {
		var req refreshRequest
		if err := httpx.ReadJSON(w, r, &req); err == nil {
			rawRefresh = strings.TrimSpace(req.RefreshToken)
		}
	}

	if fromCookie && !h.isAllowedRefreshOrigin(r) {
		httpx.HandleError(w, r, h.logger, httpx.NewForbidden("cross-site refresh origin rejected"))
		return
	}

	session, err := h.svc.RefreshSession(r.Context(), rawRefresh)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}

	h.setSessionCookies(w, session.Tokens)
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{
		"staff":        session.Staff,
		"access_token": session.Tokens.AccessToken,
		"expires_at":   session.Tokens.ExpiresAt,
	})
}

// @Summary      Logout staff session
// @Description  Clears session cookies and increments the staff token version to revoke all issued tokens.
// @Tags         Auth
// @Produce      json
// @Param        Authorization header string false "Bearer access token when cookie is unavailable"
// @Param        Origin header string false "Allowed browser origin for cookie-based logout"
// @Param        Sec-Fetch-Site header string false "Browser fetch site header"
// @Param        X-Requested-With header string false "Set by non-cross-site browser clients"
// @Success      200 {object} map[string]interface{}
// @Failure      403 {object} httpx.ErrorPayload "cross-site logout origin rejected"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /auth/logout [post]
func (h *TicketingHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, cookieErr := r.Cookie(middleware.AccessCookieName)
	hasCookie := cookieErr == nil && cookie.Value != ""
	if hasCookie && !h.isAllowedRefreshOrigin(r) {
		httpx.HandleError(w, r, h.logger, httpx.NewForbidden("cross-site logout origin rejected"))
		return
	}

	var revokeErr error
	if h.tokens != nil {
		var staffID uuid.UUID
		if hasCookie {
			if claims, err := h.tokens.VerifyAccessToken(cookie.Value); err == nil {
				staffID = claims.StaffID
			}
		} else if authHdr := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
			if claims, err := h.tokens.VerifyAccessToken(strings.TrimSpace(authHdr[7:])); err == nil {
				staffID = claims.StaffID
			}
		}

		if staffID == uuid.Nil {
			if refCookie, err := r.Cookie(middleware.RefreshCookieName); err == nil && refCookie.Value != "" {
				if rclaims, err := h.tokens.VerifyRefreshToken(refCookie.Value); err == nil {
					staffID = rclaims.StaffID
				}
			}
		}

		if staffID != uuid.Nil {
			revokeErr = h.svc.LogoutStaff(r.Context(), staffID)
		}
	}

	h.clearSessionCookies(w)
	if revokeErr != nil {
		httpx.HandleError(w, r, h.logger, revokeErr)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.Envelope{"status": "signed_out"})
}

func (h *TicketingHandler) isAllowedRefreshOrigin(r *http.Request) bool {
	secFetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if secFetchSite == "cross-site" {
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-Requested-With")) != "" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return secFetchSite == "same-origin"
	}
	for _, allowed := range h.cfg.AllowedOrigins {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}
	return false
}

func (h *TicketingHandler) setSessionCookies(w http.ResponseWriter, pair auth.TokenPair) {
	h.writeCookie(w, middleware.AccessCookieName, pair.AccessToken, int(h.cfg.AccessTokenTTL.Seconds()))
	h.writeCookie(w, middleware.RefreshCookieName, pair.RefreshToken, int(h.cfg.RefreshTokenTTL.Seconds()))
}

func (h *TicketingHandler) clearSessionCookies(w http.ResponseWriter) {
	h.writeCookie(w, middleware.AccessCookieName, "", -1)
	h.writeCookie(w, middleware.RefreshCookieName, "", -1)
}

// writeCookie issues strictly isolated HttpOnly + SameSite=Strict cookies so no third-party site or JS can read tokens/roles.
func (h *TicketingHandler) writeCookie(w http.ResponseWriter, name, value string, maxAge int) {
	securePart := ""
	if h.cfg.CookieSecure {
		securePart = "; Secure"
	}
	domainPart := ""
	if h.cfg.CookieDomain != "" {
		domainPart = "; Domain=" + h.cfg.CookieDomain
	}
	cookieHeader := fmt.Sprintf("%s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Strict%s%s",
		name, value, maxAge, domainPart, securePart)
	w.Header().Add("Set-Cookie", cookieHeader)
}

// @Summary      Submit customer help request
// @Description  Allows patrons/guests to submit help inquiries or payment issues to venue admins.
// @Tags         Support
// @Accept       json
// @Produce      json
// @Param        payload body services.CreateHelpRequest true "Help query details"
// @Success      201 {object} services.CreateHelpResponse
// @Failure      400 {object} httpx.ErrorPayload
// @Failure      429 {object} httpx.ErrorPayload "rate limit exceeded"
// @Failure      500 {object} httpx.ErrorPayload
// @Router       /help [post]
func (h *TicketingHandler) SubmitHelpMessage(w http.ResponseWriter, r *http.Request) {
	var req services.CreateHelpRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	resp, err := h.svc.SubmitHelpMessage(r.Context(), req)
	if err != nil {
		httpx.HandleError(w, r, h.logger, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, resp)
}
