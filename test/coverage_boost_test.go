package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/ratelimit"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/services"
)

// TestHandlerMalformedInputs verifies robust 400 Bad Request responses on invalid path params and bodies.
func TestHandlerMalformedInputs(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	badUUID := "not-a-uuid"

	// Admin bad UUID paths
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/staff/"+badUUID+"/password", map[string]string{"password": "pass"}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/admin/staff/"+badUUID, nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/events/"+badUUID+"/tiers", map[string]any{"name": "VIP"}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPut, "/api/admin/events/"+badUUID+"/seating", map[string]any{}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/stats/"+badUUID, nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/help/"+badUUID+"/resolve", nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/bookings/"+badUUID+"/verify", map[string]string{"action": "APPROVE"}, bearerHeader(adminToken)), http.StatusBadRequest)

	// Admin invalid json bodies
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/staff", "invalid json", bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/events", "invalid json", bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/events/"+env.eventID.String()+"/tiers", "invalid json", bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/staff/"+env.adminID.String()+"/password", "invalid json", bearerHeader(adminToken)), http.StatusBadRequest)

	// Bar & Menu bad inputs
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/counter/menu/"+badUUID, nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/menu", "invalid json", bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", "invalid json", bearerHeader(barToken)), http.StatusBadRequest)

	// Gate bad json
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", "invalid json", bearerHeader(scannerToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/check-in", "invalid json", bearerHeader(scannerToken)), http.StatusBadRequest)

	// Public bad UUID paths
	assertStatus(t, env.doRequest(http.MethodGet, "/api/tickets/"+badUUID, nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/tickets/"+badUUID+"/image", nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events/"+badUUID+"/seats", nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/checkout/"+badUUID+"/upi-reference", map[string]string{"upi_transaction_id": "12345678"}, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/checkout/init", "invalid json", nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/help", "invalid json", nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/login", "invalid json", nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/refresh", "invalid json", nil), http.StatusUnauthorized)
}

// TestPublicEventsAndTiersPagination tests query parameter clamping and filters.
func TestPublicEventsAndTiersPagination(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// Pagination with negative or high limits
	rec := env.doRequest(http.MethodGet, "/api/events?limit=200&offset=-5", nil, nil)
	assertStatus(t, rec, http.StatusOK)

	// Invalid pagination params return 400
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events?limit=abc", nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events?offset=xyz", nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events?status=INVALID_STATUS", nil, nil), http.StatusBadRequest)

	// Admin bookings with bad event_id query or bad status
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/bookings?event_id=not-a-uuid", nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/bookings?status=INVALID", nil, bearerHeader(adminToken)), http.StatusBadRequest)

	// Admin events listing
	rec = env.doRequest(http.MethodGet, "/api/admin/events", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	// Admin audit logs with filters
	rec = env.doRequest(http.MethodGet, "/api/admin/audit-logs?path=/api&limit=10&offset=0", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/audit-logs?actor_id=bad", nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/audit-logs?limit=bad", nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/audit-logs?offset=bad", nil, bearerHeader(adminToken)), http.StatusBadRequest)
}

// TestHelpValidationEdgeCases tests edge validation branches of the help service.
func TestHelpValidationEdgeCases(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// Empty name -> 400
	assertStatus(t, env.doRequest(http.MethodPost, "/api/help", services.CreateHelpRequest{
		Name:    "",
		Phone:   "+919876543211",
		Message: "Hello",
	}, nil), http.StatusBadRequest)

	// Empty phone -> 400
	assertStatus(t, env.doRequest(http.MethodPost, "/api/help", services.CreateHelpRequest{
		Name:    "John",
		Phone:   "",
		Message: "Hello",
	}, nil), http.StatusBadRequest)

	// Empty message -> 400
	assertStatus(t, env.doRequest(http.MethodPost, "/api/help", services.CreateHelpRequest{
		Name:    "John",
		Phone:   "+919876543211",
		Message: "",
	}, nil), http.StatusBadRequest)

	// Non-existent help message resolution -> 404
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/help/"+uuid.NewString()+"/resolve", nil, bearerHeader(adminToken)), http.StatusNotFound)
}

// TestRateLimiterConfigurationAndHelpers tests options and helpers in ratelimit package.
func TestRateLimiterConfigurationAndHelpers(t *testing.T) {
	ms := newMemStore()
	rl := ratelimit.NewPostgresRateLimiter(ms, 3*time.Second, nil,
		ratelimit.WithCacheSize(2048),
		ratelimit.WithFlushEvery(50),
	)

	mw := rl.Middleware(2, time.Minute)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test-rl", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rl.StartCleanup(ctx, 100*time.Millisecond, time.Hour)
	cancel()
}

// TestMiddlewareAuditRecorderHelpers tests Flush, Hijack, and Unwrap on auditWriter.
func TestMiddlewareAuditRecorderHelpers(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := middleware.Audit(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			_ = u.Unwrap()
		}
		if h, ok := w.(http.Hijacker); ok {
			_, _, _ = h.Hijack()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// TestRepositoryHelpers exercises Ping, PoolStats, and constructor wrappers.
func TestRepositoryHelpers(t *testing.T) {
	ms := newMemStore()
	repo := repositories.NewTicketingRepositoryWithQuerier(ms, func(ctx context.Context) error { return nil }, func(ctx context.Context, fn func(context.Context, db.Querier) error) error {
		return fn(ctx, ms)
	}, 3*time.Second)

	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	stats := repo.PoolStats()
	if stats.MaxConns != 0 {
		t.Fatalf("unexpected pool stats: %+v", stats)
	}

	// WithTx success & rollback
	err := repo.WithTx(context.Background(), func(ctx context.Context, q db.Querier) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx failed: %v", err)
	}
}

// TestSeatingHandlerAndRepoEdgeCases exercises seating handler validation and bar repo queries.
func TestSeatingHandlerAndRepoEdgeCases(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// Bad UUID on SeatSocket
	rec := env.doRequest(http.MethodGet, "/api/events/bad-uuid/seats/ws", nil, nil)
	assertStatus(t, rec, http.StatusBadRequest)

	// Bad UUID on ConfigureSeating
	rec = env.doRequest(http.MethodPut, "/api/admin/events/bad-uuid/seating", map[string]any{"rows": []any{}}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusBadRequest)

	// Bad JSON on ConfigureSeating
	rec = env.doRequest(http.MethodPut, "/api/admin/events/"+uuid.NewString()+"/seating", "invalid-json", bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusBadRequest)

	// repo GetBarTransactionByIdempotencyKey not found
	repo := repositories.NewTicketingRepositoryWithQuerier(env.store, nil, nil, time.Second)
	_, err := repo.GetBarTransactionByIdempotencyKey(context.Background(), "non-existent-key")
	if err == nil {
		t.Fatal("expected error for non-existent key, got nil")
	}
}

// TestAdminEventEditPriceChangeAndArtists tests admin editing, price updates, artist management, and lead booker checkout.
func TestAdminEventEditPriceChangeAndArtists(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// 1. Update event details
	newTitle := "Neon Sky Blue Festival"
	rec := env.doRequest(http.MethodPut, "/api/admin/events/"+env.eventID.String(), map[string]any{
		"title": newTitle,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	// 2. Update category price
	rec = env.doRequest(http.MethodPatch, "/api/admin/tiers/"+env.maleCatID.String()+"/price", map[string]any{
		"base_price": 1999,
		"fnb_credit": 1999,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	// 3. Create artist
	rec = env.doRequest(http.MethodPost, "/api/admin/events/"+env.eventID.String()+"/artists", map[string]any{
		"name":        "DJ Nucleya",
		"category":    "Headliner DJ",
		"description": "Bass and electronic music producer",
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var artist db.EventArtist
	decodeBody(t, rec, &artist)

	// 4. List artists
	rec = env.doRequest(http.MethodGet, "/api/admin/events/"+env.eventID.String()+"/artists", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	// 5. Delete artist
	rec = env.doRequest(http.MethodDelete, "/api/admin/artists/"+artist.ID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	// 6. Test primary booker frictionless checkout (no guest spam)
	rec = env.doRequest(http.MethodPost, "/api/checkout/init", map[string]any{
		"event_id":        env.eventID.String(),
		"category_id":     env.maleCatID.String(),
		"whatsapp_number": "+919876543211",
		"primary_name":    "Vikram Malhotra",
		"primary_age":     27,
		"gender":          "MALE",
	}, map[string]string{
		"Idempotency-Key": "idemp-primary-booker-001",
	})
	assertStatus(t, rec, http.StatusCreated)
}
