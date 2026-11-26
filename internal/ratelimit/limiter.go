package ratelimit

import (
	"context"
	"net/http"
	"time"
)

// Limiter defines the HTTP rate-limiting middleware contract.
type Limiter interface {
	Middleware(maxRequests int, window time.Duration) func(http.Handler) http.Handler
	StartCleanup(ctx context.Context, interval, retention time.Duration)
}
