package ratelimit

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/middleware"
)

const (
	defaultCacheSize  = 16384
	defaultFlushEvery = 5
)

type windowCounter struct {
	mu      sync.Mutex
	hits    int
	pending int
}

type options struct {
	cacheSize  int
	flushEvery int
}

type Option func(*options)

func WithCacheSize(size int) Option {
	return func(o *options) {
		if size > 0 {
			o.cacheSize = size
		}
	}
}

func WithFlushEvery(every int) Option {
	return func(o *options) {
		if every > 0 {
			o.flushEvery = every
		}
	}
}

// PostgresRateLimiter enforces request quotas with a bounded in-memory counter layer and PostgreSQL-backed reconciliation.
// Keys authenticated staff by staff ID (preventing club Wi-Fi NAT false positives) and normalizes route patterns.
type PostgresRateLimiter struct {
	queries    db.Querier
	dbTimeout  time.Duration
	logger     *slog.Logger
	local      *lru.Cache[string, *windowCounter]
	fillMu     sync.Mutex
	flushEvery int
}

func NewPostgresRateLimiter(queries db.Querier, dbTimeout time.Duration, logger *slog.Logger, opts ...Option) *PostgresRateLimiter {
	if dbTimeout <= 0 {
		dbTimeout = 3 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	settings := options{cacheSize: defaultCacheSize, flushEvery: defaultFlushEvery}
	for _, opt := range opts {
		opt(&settings)
	}
	local, err := lru.New[string, *windowCounter](settings.cacheSize)
	if err != nil {
		local, _ = lru.New[string, *windowCounter](defaultCacheSize)
	}
	return &PostgresRateLimiter{
		queries:    queries,
		dbTimeout:  dbTimeout,
		logger:     logger,
		local:      local,
		flushEvery: settings.flushEvery,
	}
}

func (l *PostgresRateLimiter) Middleware(maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	if maxRequests <= 0 {
		maxRequests = 60
	}
	if window <= 0 {
		window = time.Minute
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identifier := extractIdentifier(r)
			endpoint := r.Method + " " + extractRoutePattern(r)
			now := time.Now().UTC()
			windowStart := now.Truncate(window)

			ctx, cancel := context.WithTimeout(r.Context(), l.dbTimeout)
			defer cancel()

			currentHits, err := l.allow(ctx, identifier, endpoint, windowStart)
			if err != nil {
				l.logger.ErrorContext(r.Context(), "postgres rate limiter check failed", "endpoint", endpoint, "error", err)
				w.Header().Set("Retry-After", "1")
				_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, httpx.ErrorPayload{
					Error: "rate limiter unavailable",
				})
				return
			}

			remaining := maxRequests - currentHits
			if remaining < 0 {
				remaining = 0
			}
			resetAt := windowStart.Add(window)

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(maxRequests))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if currentHits > maxRequests {
				retryAfter := int(time.Until(resetAt).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				_ = httpx.WriteJSON(w, http.StatusTooManyRequests, httpx.ErrorPayload{
					Error: "too many requests; please retry shortly",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func (l *PostgresRateLimiter) allow(ctx context.Context, identifier, endpoint string, windowStart time.Time) (int, error) {
	key := identifier + "\x00" + endpoint + "\x00" + windowStart.Format(time.RFC3339Nano)
	if entry, ok := l.local.Get(key); ok {
		return l.increment(ctx, identifier, endpoint, windowStart, entry), nil
	}

	l.fillMu.Lock()
	defer l.fillMu.Unlock()
	if entry, ok := l.local.Get(key); ok {
		return l.increment(ctx, identifier, endpoint, windowStart, entry), nil
	}

	hits, err := l.queries.UpsertRateLimitHit(ctx, db.UpsertRateLimitHitParams{
		Identifier:  identifier,
		Endpoint:    endpoint,
		WindowStart: windowStart,
	})
	if err != nil {
		return 0, err
	}
	entry := &windowCounter{hits: int(hits), pending: 0}
	l.local.Add(key, entry)
	return int(hits), nil
}

func (l *PostgresRateLimiter) increment(ctx context.Context, identifier, endpoint string, windowStart time.Time, entry *windowCounter) int {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.hits++
	entry.pending++
	if entry.pending < l.flushEvery {
		return entry.hits
	}
	total, err := l.queries.AddRateLimitHits(ctx, db.AddRateLimitHitsParams{
		Identifier:  identifier,
		Endpoint:    endpoint,
		WindowStart: windowStart,
		Hits:        int32(entry.pending),
	})
	if err != nil {
		l.logger.WarnContext(ctx, "postgres rate limiter reconciliation failed", "endpoint", endpoint, "error", err)
		return entry.hits
	}
	entry.hits = int(total)
	entry.pending = 0
	return entry.hits
}

// StartCleanup periodically removes expired rate_limits rows in the background until ctx is canceled.
func (l *PostgresRateLimiter) StartCleanup(ctx context.Context, interval, retention time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if retention <= 0 {
		retention = time.Hour
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneCtx, cancel := context.WithTimeout(context.Background(), l.dbTimeout)
				cutoff := time.Now().UTC().Add(-retention)
				if err := l.queries.DeleteExpiredRateLimits(pruneCtx, cutoff); err != nil {
					l.logger.Warn("failed to prune expired rate_limits rows", "error", err)
				}
				cancel()
			}
		}
	}()
}

func extractIdentifier(r *http.Request) string {
	if staffID, ok := middleware.StaffIDFromContext(r.Context()); ok {
		return "staff:" + staffID.String()
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return "ip:" + host
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		return "ip:" + addr
	}
	return "ip:unknown"
}

func extractRoutePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return r.URL.Path
}
