package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/middleware"
)

type limitQuerier struct {
	db.Querier
	hits      int32
	err       error
	deletes   atomic.Int32
	upserts   atomic.Int32
	adds      atomic.Int32
	addedHits atomic.Int32
}

func (q *limitQuerier) UpsertRateLimitHit(context.Context, db.UpsertRateLimitHitParams) (int32, error) {
	q.upserts.Add(1)
	return q.hits, q.err
}

func (q *limitQuerier) AddRateLimitHits(_ context.Context, arg db.AddRateLimitHitsParams) (int32, error) {
	q.adds.Add(1)
	q.addedHits.Add(arg.Hits)
	return q.hits + arg.Hits, q.err
}

func (q *limitQuerier) DeleteExpiredRateLimits(context.Context, time.Time) error {
	q.deletes.Add(1)
	return nil
}

func TestMiddlewareAllowsThenBlocks(t *testing.T) {
	q := &limitQuerier{hits: 1}
	l := NewPostgresRateLimiter(q, time.Second, nil)
	handler := l.Middleware(1, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("first request got %d %v", rec.Code, rec.Header())
	}

	q.hits = 2
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second request got %d %v", rec.Code, rec.Header())
	}
}

func TestMiddlewareFailClosedAndDefaults(t *testing.T) {
	q := &limitQuerier{err: errors.New("db down")}
	l := NewPostgresRateLimiter(q, 0, nil)
	called := false
	handler := l.Middleware(0, 0)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("fail-closed expected, got %d called=%v", rec.Code, called)
	}
}

func TestRateLimitFlushesInBatches(t *testing.T) {
	q := &limitQuerier{hits: 1}
	l := NewPostgresRateLimiter(q, time.Second, nil, WithFlushEvery(10))
	handler := l.Middleware(1000, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < 25; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d got %d", i, rec.Code)
		}
	}

	if got := q.upserts.Load(); got != 1 {
		t.Fatalf("expected 1 seed upsert, got %d", got)
	}
	if got := q.adds.Load(); got != 2 {
		t.Fatalf("expected 2 batched reconciliation writes, got %d", got)
	}
	if got := q.addedHits.Load(); got != 20 {
		t.Fatalf("expected 20 reconciled hits, got %d", got)
	}
}

func TestStartCleanup(t *testing.T) {
	q := &limitQuerier{}
	l := NewPostgresRateLimiter(q, time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	l.StartCleanup(ctx, time.Millisecond, time.Hour)
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)
	if q.deletes.Load() == 0 {
		t.Fatal("cleanup never pruned expired rows")
	}
}

func TestExtractIdentifier(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if got := extractIdentifier(r); got != "ip:10.0.0.1" {
		t.Fatalf("unexpected ip identifier: %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "unix"
	if got := extractIdentifier(r); got != "ip:unix" {
		t.Fatalf("unexpected fallback identifier: %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ""
	if got := extractIdentifier(r); got != "ip:unknown" {
		t.Fatalf("unexpected unknown identifier: %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.77, 10.0.0.1")
	if got := extractIdentifier(r); got != "ip:10.0.0.1" {
		t.Fatalf("forwarded header must be ignored, got %q", got)
	}

	id := uuid.New()
	ctx := middleware.ContextWithStaff(context.Background(), db.StaffAccount{ID: id})
	r = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	if got := extractIdentifier(r); got != "staff:"+id.String() {
		t.Fatalf("unexpected staff identifier: %q", got)
	}
}

func TestExtractRoutePatternFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/events/123", nil)
	if got := extractRoutePattern(r); got != "/api/events/123" {
		t.Fatalf("unexpected route pattern: %q", got)
	}
}

func TestUpsertUsesExplicitIdentifierAndEndpoint(t *testing.T) {
	var got db.UpsertRateLimitHitParams
	q := &querierCapture{UpsertFn: func(_ context.Context, arg db.UpsertRateLimitHitParams) (int32, error) {
		got = arg
		return 1, nil
	}}
	l := NewPostgresRateLimiter(q, time.Second, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/gate/check-in", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	l.Middleware(5, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, r)
	if got.Identifier != "ip:10.0.0.1" || got.Endpoint != "POST /api/gate/check-in" || got.WindowStart.IsZero() {
		t.Fatalf("unexpected upsert params: %+v", got)
	}
}

type querierCapture struct {
	db.Querier
	UpsertFn func(context.Context, db.UpsertRateLimitHitParams) (int32, error)
}

func (q *querierCapture) UpsertRateLimitHit(ctx context.Context, arg db.UpsertRateLimitHitParams) (int32, error) {
	return q.UpsertFn(ctx, arg)
}
