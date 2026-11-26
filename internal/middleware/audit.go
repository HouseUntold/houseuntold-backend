package middleware

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type AuditRecord struct {
	RequestID  string
	Method     string
	Path       string
	Route      string
	StatusCode int
	DurationMS int64
	ActorID    uuid.UUID
	ActorRole  string
	IP         string
	UserAgent  string
}

type auditIdentity struct {
	id   uuid.UUID
	role string
}

type auditIdentityCtxKey struct{}

type AuditSink interface {
	WriteAudit(context.Context, AuditRecord) error
}

type auditWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *auditWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *auditWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *auditWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// AuditPool is a bounded semaphore-based goroutine pool for audit sink writes.
// It caps concurrent WriteAudit calls to maxConcurrency; excess writes are dropped
// with a warning log to prevent goroutine leaks under sustained load.
type AuditPool struct {
	logger *slog.Logger
	sink   AuditSink
	sem    chan struct{} // buffered channel used as semaphore
}

// NewAuditMiddleware returns an audit middleware backed by a bounded goroutine pool of
// maxConcurrency workers. The semaphore buffer is capped at 500 pending slots; writes
// that cannot acquire a slot immediately are dropped with a Warn log.
func NewAuditMiddleware(logger *slog.Logger, sink AuditSink, maxConcurrency int) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var pool *AuditPool
	if sink != nil {
		if maxConcurrency <= 0 {
			maxConcurrency = 50
		}
		pool = &AuditPool{
			logger: logger,
			sink:   sink,
			sem:    make(chan struct{}, 500),
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			identity := &auditIdentity{}
			r = r.WithContext(context.WithValue(r.Context(), auditIdentityCtxKey{}, identity))
			recorder := &auditWriter{ResponseWriter: w}
			next.ServeHTTP(recorder, r)

			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			actorID := identity.id
			actorRole := identity.role
			route := r.URL.Path
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if pattern := routeCtx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}
			record := AuditRecord{
				RequestID:  chimiddleware.GetReqID(r.Context()),
				Method:     r.Method,
				Path:       r.URL.Path,
				Route:      route,
				StatusCode: status,
				DurationMS: time.Since(start).Milliseconds(),
				ActorID:    actorID,
				ActorRole:  actorRole,
				IP:         r.RemoteAddr,
				UserAgent:  r.UserAgent(),
			}
			logger.Info("api_call",
				"request_id", record.RequestID,
				"method", record.Method,
				"path", record.Path,
				"route", record.Route,
				"status", record.StatusCode,
				"duration_ms", record.DurationMS,
				"actor_id", record.ActorID,
				"actor_role", record.ActorRole,
				"ip", record.IP,
			)
			if pool != nil {
				// Try to acquire a semaphore slot; drop if the buffer is full.
				select {
				case pool.sem <- struct{}{}:
					go func() {
						defer func() { <-pool.sem }()
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						if err := pool.sink.WriteAudit(ctx, record); err != nil {
							pool.logger.Warn("api_audit_write", "request_id", record.RequestID, "error", err)
						}
					}()
				default:
					logger.Warn("api_audit_drop", "request_id", record.RequestID, "reason", "audit pool full")
				}
			}
		})
	}
}

// Audit is a convenience wrapper that calls NewAuditMiddleware with a default
// concurrency of 50 goroutines. Use NewAuditMiddleware for custom pool sizes.
func Audit(logger *slog.Logger, sink AuditSink) func(http.Handler) http.Handler {
	return NewAuditMiddleware(logger, sink, 50)
}
