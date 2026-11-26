package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/handlers"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/services"
)

type passLimiter struct{}

func (passLimiter) Middleware(_ int, _ time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

func (passLimiter) StartCleanup(context.Context, time.Duration, time.Duration) {}

type routerQuerier struct{ db.Querier }

func testRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{
		RequestTimeout:          5 * time.Second,
		AllowedOrigins:          []string{"https://venu.test"},
		AuthRateLimitMax:        10,
		AuthRateLimitWindow:     time.Minute,
		CheckoutRateLimitMax:    10,
		CheckoutRateLimitWindow: time.Minute,
		ScanRateLimitMax:        10,
		ScanRateLimitWindow:     time.Minute,
		BarRateLimitMax:         10,
		BarRateLimitWindow:      time.Minute,
		AppBaseURL:              "https://venu.test",
		First99FemaleLimit:      99,
	}
	repo := repositories.NewTicketingRepositoryWithQuerier(routerQuerier{}, func(context.Context) error { return nil }, nil, time.Second)
	tokens := auth.NewTokenManager("access-secret", "refresh-secret", "aud", "iss", time.Minute, time.Hour)
	signer := auth.NewQRSigner("qr-secret")
	roles := cache.NewStaffRoleCache(time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ticketingSvc := services.NewTicketingService(cfg, repo, tokens, signer, roles, nil, logger)
	rbac := middleware.NewRBAC(tokens, routerQuerier{}, roles, time.Second, cfg.AllowedOrigins)

	return NewRouter(Deps{
		Cfg:       cfg,
		Handler:   handlers.NewTicketingHandler(cfg, ticketingSvc, tokens, logger),
		AdminHdlr: handlers.NewAdminHandler(services.NewAdminService(repo, roles, nil, cfg.AppBaseURL, logger), logger),
		GateHdlr:  handlers.NewGateHandler(services.NewGateService(repo, signer, logger), logger),
		BarHdlr:   handlers.NewBarHandler(services.NewBarService(repo, logger), logger),
		RBAC:      rbac,
		Limiter:   passLimiter{},
	})
}

func TestNewRouterHealthAndSecurity(t *testing.T) {
	router := testRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("health got %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("security header missing: %q", got)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	req.Header.Set("Origin", "https://venu.test")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://venu.test" {
		t.Fatalf("preflight got %d headers=%v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route got %d", rec.Code)
	}
}

func TestNewRouterServesOpenAPIAndGatesSwaggerUI(t *testing.T) {
	newRouter := func(env string) http.Handler {
		cfg := &config.Config{Env: env, RequestTimeout: time.Second}
		return NewRouter(Deps{
			Cfg:       cfg,
			Handler:   &handlers.TicketingHandler{},
			AdminHdlr: &handlers.AdminHandler{},
			GateHdlr:  &handlers.GateHandler{},
			BarHdlr:   &handlers.BarHandler{},
			RBAC:      &middleware.RBAC{},
			Limiter:   passLimiter{},
		})
	}

	for path, contentType := range map[string]string{
		"/openapi.yaml":     "application/yaml",
		"/swagger/doc.json": "application/json",
	} {
		rec := httptest.NewRecorder()
		newRouter("production").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), contentType) || !strings.Contains(rec.Body.String(), "Idempotency-Key") {
			t.Fatalf("%s got %d headers=%v body=%s", path, rec.Code, rec.Header(), rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	newRouter("production").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("production swagger UI got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	newRouter("development").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "SwaggerUIBundle") {
		t.Fatalf("development swagger UI got %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	rec = httptest.NewRecorder()
	newRouter("development").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger/swagger-ui.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("swagger UI asset got %d", rec.Code)
	}
}

func TestNewRouterProtectsStaffRoutes(t *testing.T) {
	router := testRouter(t)
	for _, path := range []string{
		"/api/gate/validate",
		"/api/counter/menu",
		"/api/admin/bookings",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s without auth got %d", path, rec.Code)
		}
	}
}

func TestNewRouterMountsExpectedRoutes(t *testing.T) {
	cfg := &config.Config{RequestTimeout: time.Second}
	router := NewRouter(Deps{
		Cfg:       cfg,
		Handler:   &handlers.TicketingHandler{},
		AdminHdlr: &handlers.AdminHandler{},
		GateHdlr:  &handlers.GateHandler{},
		BarHdlr:   &handlers.BarHandler{},
		RBAC:      &middleware.RBAC{},
		Limiter:   passLimiter{},
	})
	r, ok := router.(chi.Router)
	if !ok {
		t.Fatal("router is not chi.Router")
	}
	routes := 0
	err := chi.Walk(r, func(_ string, _ string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes++
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if routes < 25 {
		t.Fatalf("expected at least 25 mounted routes, got %d", routes)
	}
}
