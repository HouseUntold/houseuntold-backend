package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpSwagger "github.com/swaggo/http-swagger/v2"

	"github.com/venu-live/venu-api/docs"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/handlers"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/ratelimit"
)

// Deps groups the wired HTTP dependencies mounted on the House Untold Chi router.
type Deps struct {
	Cfg       *config.Config
	Logger    *slog.Logger
	Handler   *handlers.TicketingHandler
	AdminHdlr *handlers.AdminHandler
	GateHdlr  *handlers.GateHandler
	BarHdlr   *handlers.BarHandler
	SeatHdlr  *handlers.SeatingHandler
	RBAC      *middleware.RBAC
	Limiter   ratelimit.Limiter
	AuditSink middleware.AuditSink
}

func swaggerCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/swagger/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func timeoutUnlessWS(timeout time.Duration) func(http.Handler) http.Handler {
	t := chimiddleware.Timeout(timeout)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			t(next).ServeHTTP(w, r)
		})
	}
}

// NewRouter mounts all House Untold API routes with security headers, CORS, audit logging, Postgres rate limiting, and per-request RBAC.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	r.Use(middleware.RealIP(d.Cfg.TrustedProxies))
	r.Use(chimiddleware.Recoverer)
	r.Use(timeoutUnlessWS(d.Cfg.RequestTimeout))
	r.Use(middleware.SecurityHeaders)
	r.Use(swaggerCSP)
	r.Use(middleware.CORS(d.Cfg.AllowedOrigins))
	r.Use(middleware.Metrics)
	r.Use(middleware.Audit(d.Logger, d.AuditSink))

	serveOpenAPI := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(docs.OpenAPIYAML())
	}
	serveSwaggerJSON := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(docs.SwaggerJSON())
	}
	serveSwaggerIndex := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(docs.SwaggerIndexHTML())
	}
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/swagger/doc.json", serveSwaggerJSON)
	if d.Cfg.Env != "production" {
		r.Get("/swagger/index.html", serveSwaggerIndex)
		r.Get("/swagger/*", httpSwagger.Handler(
			httpSwagger.URL("/swagger/doc.json"),
		))
	}
	metricsHandler := promhttp.Handler()
	r.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.Cfg.Env == "production" {
			token := d.Cfg.MetricsToken
			if token == "" {
				http.Error(w, "metrics disabled", http.StatusNotFound)
				return
			}
			provided := strings.TrimSpace(r.Header.Get("X-Metrics-Token"))
			if provided == "" {
				auth := strings.TrimSpace(r.Header.Get("Authorization"))
				if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
					provided = strings.TrimSpace(auth[7:])
				}
			}
			if provided != token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		metricsHandler.ServeHTTP(w, r)
	}))

	r.Route("/api", func(api chi.Router) {
		// 1. PUBLIC / GUEST ROUTES (Direct UPI QR + UTR Submission)
		api.Get("/health", d.Handler.Health)
		api.Get("/events", d.Handler.ListPublicEvents)
		api.Get("/events/{id}", d.Handler.GetEventDetails)
		api.Get("/events/{id}/seats", d.SeatHdlr.GetSeats)
		api.Get("/events/{id}/seats/ws", d.SeatHdlr.SeatSocket)
		api.Get("/menu", d.BarHdlr.GetMenu)

		// Public Ticket Viewing (Rate limited)
		api.Group(func(ticketPub chi.Router) {
			ticketPub.Use(d.Limiter.Middleware(d.Cfg.CheckoutRateLimitMax, d.Cfg.CheckoutRateLimitWindow))
			ticketPub.Get("/tickets/lookup", d.Handler.LookupTicket)
			ticketPub.Get("/tickets/{uuid}", d.Handler.GetTicketByUUID)
			ticketPub.Get("/tickets/{uuid}/image", d.Handler.GetTicketImagePNG)
		})

		// Guest Help & Support (Rate limited)
		api.Group(func(helpPub chi.Router) {
			helpPub.Use(d.Limiter.Middleware(d.Cfg.CheckoutRateLimitMax, d.Cfg.CheckoutRateLimitWindow))
			helpPub.Post("/help", d.Handler.SubmitHelpMessage)
		})

		// Guest Checkout & UPI UTR Submission (PostgreSQL rate limited)
		api.Group(func(checkoutPub chi.Router) {
			checkoutPub.Use(d.Limiter.Middleware(d.Cfg.CheckoutRateLimitMax, d.Cfg.CheckoutRateLimitWindow))
			checkoutPub.Post("/checkout/init", d.Handler.InitGuestCheckout)
			checkoutPub.Post("/checkout/{booking_id}/upi-reference", d.Handler.SubmitUPITransactionID)
		})

		// Staff Auth (ADMIN, SCANNER, BARTENDER — username/password, HttpOnly SameSite=Strict cookies)
		api.Group(func(authPub chi.Router) {
			authPub.Use(d.Limiter.Middleware(d.Cfg.AuthRateLimitMax, d.Cfg.AuthRateLimitWindow))
			authPub.Post("/auth/login", d.Handler.StaffLogin)
			authPub.Post("/auth/refresh", d.Handler.RefreshSession)
			authPub.Post("/auth/logout", d.Handler.Logout)
		})

		// 2. PROTECTED STAFF ROUTES (JWT + Server-Side StaffRoleCache on every request)
		api.Group(func(authed chi.Router) {
			authed.Use(d.RBAC.Authenticate)

			// --- GATE SCANNER (SCANNER or ADMIN) ---
			authed.Group(func(scanGrp chi.Router) {
				scanGrp.Use(d.RBAC.RequireRole("SCANNER", "ADMIN"))
				scanGrp.Use(d.Limiter.Middleware(d.Cfg.ScanRateLimitMax, d.Cfg.ScanRateLimitWindow))

				scanGrp.Post("/gate/validate", d.GateHdlr.ValidateQR)
				scanGrp.Post("/gate/check-in", d.GateHdlr.CheckInTicket)
			})

			// --- BARTENDER POS (BARTENDER or ADMIN) ---
			authed.Group(func(barGrp chi.Router) {
				barGrp.Use(d.RBAC.RequireRole("BARTENDER", "ADMIN"))
				barGrp.Use(d.Limiter.Middleware(d.Cfg.BarRateLimitMax, d.Cfg.BarRateLimitWindow))

				barGrp.Get("/counter/menu", d.BarHdlr.GetMenu)
				barGrp.Get("/counter/search", d.BarHdlr.SearchTicketByBarCode)
				barGrp.Get("/counter/tickets/{bar_code}/history", d.BarHdlr.GetTicketBarHistory)
				barGrp.Post("/counter/redeem", d.BarHdlr.RedeemItems)
			})

			// --- ADMIN OPERATIONS (ADMIN only) ---
			authed.Group(func(adminGrp chi.Router) {
				adminGrp.Use(d.RBAC.RequireRole("ADMIN"))

				adminGrp.Post("/counter/menu", d.BarHdlr.CreateMenuItem)
				adminGrp.Put("/counter/menu/{id}", d.BarHdlr.UpdateMenuItem)
				adminGrp.Delete("/counter/menu/{id}", d.BarHdlr.DeleteMenuItem)
				adminGrp.Post("/admin/staff", d.AdminHdlr.CreateStaff)
				adminGrp.Get("/admin/staff", d.AdminHdlr.ListStaff)
				adminGrp.Patch("/admin/staff/{id}/password", d.AdminHdlr.UpdateStaffPassword)
				adminGrp.Delete("/admin/staff/{id}", d.AdminHdlr.DeactivateStaff)

				adminGrp.Get("/admin/bookings", d.AdminHdlr.ListBookingsForVerification)
				adminGrp.Post("/admin/bookings/{id}/verify", d.AdminHdlr.VerifyBookingPayment)
				adminGrp.Patch("/admin/bookings/{id}/wa-sent", d.Handler.MarkBookingWhatsAppSent)
				adminGrp.Get("/admin/tickets/search", d.Handler.SearchTickets)
				adminGrp.Get("/admin/leads", d.AdminHdlr.ListCheckoutLeads)

				adminGrp.Post("/admin/events", d.AdminHdlr.CreateEvent)
				adminGrp.Get("/admin/events", d.AdminHdlr.ListAdminEvents)
				adminGrp.Put("/admin/events/{id}", d.AdminHdlr.UpdateEvent)
				adminGrp.Delete("/admin/events/{id}", d.AdminHdlr.SoftDeleteEvent)
				adminGrp.Patch("/admin/events/{id}/first-99", d.AdminHdlr.SetEventFirst99Active)
				adminGrp.Post("/admin/events/{id}/tiers", d.AdminHdlr.CreateTicketTier)
				adminGrp.Patch("/admin/tiers/{id}", d.AdminHdlr.UpdateTicketTier)
				adminGrp.Patch("/admin/tiers/{id}/price", d.AdminHdlr.UpdateTierPrice)
				adminGrp.Put("/admin/events/{id}/seating", d.SeatHdlr.ConfigureSeating)
				adminGrp.Post("/admin/events/{id}/artists", d.AdminHdlr.CreateArtist)
				adminGrp.Get("/admin/events/{id}/artists", d.AdminHdlr.ListArtists)
				adminGrp.Delete("/admin/artists/{id}", d.AdminHdlr.DeleteArtist)

				adminGrp.Get("/admin/audit-logs", d.AdminHdlr.ListAPIAuditLogs)
				adminGrp.Get("/admin/stats", d.AdminHdlr.GetAdminStats)
				adminGrp.Get("/admin/stats/{event_id}", d.AdminHdlr.GetEventStats)

				adminGrp.Get("/admin/help", d.AdminHdlr.ListHelpMessages)
				adminGrp.Post("/admin/help/{id}/resolve", d.AdminHdlr.ResolveHelpMessage)
				adminGrp.Get("/admin/ws", d.AdminHdlr.AdminSocket)
			})
		})
	})

	return r
}
