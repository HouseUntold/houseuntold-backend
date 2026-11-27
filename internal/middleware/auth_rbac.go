package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
)

type ctxKey string

const (
	AccessCookieName  = "venu_access"
	RefreshCookieName = "venu_refresh"
	staffContextKey   = ctxKey("venu_auth_staff")
	staffIDContextKey = ctxKey("venu_auth_staff_id")
	staffRoleCtxKey   = ctxKey("venu_auth_staff_role")
)

func ContextWithStaff(ctx context.Context, staff db.StaffAccount) context.Context {
	ctx = context.WithValue(ctx, staffContextKey, staff)
	ctx = context.WithValue(ctx, staffIDContextKey, staff.ID)
	ctx = context.WithValue(ctx, staffRoleCtxKey, staff.Role)
	if identity, ok := ctx.Value(auditIdentityCtxKey{}).(*auditIdentity); ok {
		identity.id = staff.ID
		identity.role = string(staff.Role)
	}
	return ctx
}

func StaffFromContext(ctx context.Context) (db.StaffAccount, bool) {
	s, ok := ctx.Value(staffContextKey).(db.StaffAccount)
	return s, ok
}

func StaffIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(staffIDContextKey).(uuid.UUID)
	return id, ok
}

func RoleFromContext(ctx context.Context) (db.StaffRole, bool) {
	r, ok := ctx.Value(staffRoleCtxKey).(db.StaffRole)
	return r, ok
}

type RBAC struct {
	tokens         *auth.TokenManager
	queries        db.Querier
	roleCache      *cache.StaffRoleCache
	dbTimeout      time.Duration
	allowedOrigins map[string]struct{}
}

func NewRBAC(
	tokens *auth.TokenManager,
	queries db.Querier,
	roleCache *cache.StaffRoleCache,
	dbTimeout time.Duration,
	allowedOrigins []string,
) *RBAC {
	if dbTimeout <= 0 {
		dbTimeout = 3 * time.Second
	}
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if trimmed := strings.TrimSpace(o); trimmed != "" && trimmed != "*" {
			originSet[trimmed] = struct{}{}
		}
	}
	return &RBAC{
		tokens:         tokens,
		queries:        queries,
		roleCache:      roleCache,
		dbTimeout:      dbTimeout,
		allowedOrigins: originSet,
	}
}

// RealIP trusts forwarded headers only when the direct peer is an explicitly configured proxy.
func RealIP(trusted []string) func(http.Handler) http.Handler {
	trustedSet := make(map[string]struct{}, len(trusted))
	for _, proxy := range trusted {
		if addr := strings.TrimSpace(proxy); addr != "" {
			trustedSet[addr] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
			if err != nil {
				peer = strings.TrimSpace(r.RemoteAddr)
			}
			if _, ok := trustedSet[peer]; ok {
				if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
					r.RemoteAddr = forwarded
				} else if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
					r.RemoteAddr = realIP
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders prevents cross-origin token/role leakage, clickjacking, MIME sniffing, and proxy caching.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		corp := "same-origin"
		if strings.HasSuffix(r.URL.Path, "/image") {
			corp = "cross-origin"
		}
		w.Header().Set("Cross-Origin-Resource-Policy", corp)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// Authenticate validates the staff access JWT on every request, enforces CSRF origin checks on
// cookie-authenticated mutations, and verifies the staff account & role against the server-side
// StaffRoleCache (backed by PostgreSQL with 3s timeout on cache miss).
func (rb *RBAC) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken, fromCookie := extractAccessToken(r)
		if rawToken == "" {
			_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
				Error: "authentication required",
			})
			return
		}

		if fromCookie && isMutatingMethod(r.Method) {
			if !rb.verifyCSRFOrigin(r) {
				_ = httpx.WriteJSON(w, http.StatusForbidden, httpx.ErrorPayload{
					Error: "cross-site request origin rejected",
				})
				return
			}
		}

		claims, err := rb.tokens.VerifyAccessToken(rawToken)
		if err != nil {
			_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
				Error: "access token invalid or expired",
			})
			return
		}

		staff, ok := rb.lookupVerifiedStaff(r.Context(), claims.StaffID)
		if !ok || !staff.IsActive {
			_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
				Error: "staff account inactive or unavailable",
			})
			return
		}

		if staff.TokenVersion != claims.TokenVersion {
			_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
				Error: "session revoked; please sign in again",
			})
			return
		}

		// Defense-in-depth: ensure the token role matches the authoritative server-cached DB role.
		if staff.Role != claims.Role {
			if rb.roleCache != nil {
				rb.roleCache.Invalidate(staff.ID)
			}
			_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
				Error: "role mismatch; please sign in again",
			})
			return
		}

		next.ServeHTTP(w, r.WithContext(ContextWithStaff(r.Context(), staff)))
	})
}

func (rb *RBAC) lookupVerifiedStaff(ctx context.Context, staffID uuid.UUID) (db.StaffAccount, bool) {
	if rb.roleCache != nil {
		if cached, hit := rb.roleCache.Get(staffID); hit {
			return cached, true
		}
	}

	qctx, cancel := context.WithTimeout(ctx, rb.dbTimeout)
	defer cancel()

	row, err := rb.queries.GetStaffByID(qctx, staffID)
	if err != nil {
		return db.StaffAccount{}, false
	}
	staff := db.StaffAccountFromGetStaffByID(row)

	if rb.roleCache != nil && staff.IsActive {
		rb.roleCache.Set(staff)
	}
	return staff, true
}

// RequireRole enforces strict RBAC against the authoritative server-verified role on every request.
func (rb *RBAC) RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[db.StaffRole]struct{}, len(allowed))
	for _, role := range allowed {
		allowedSet[db.StaffRole(role)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, ok := RoleFromContext(r.Context())
			if !ok {
				_ = httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorPayload{
					Error: "authentication required",
				})
				return
			}

			if _, permitted := allowedSet[role]; !permitted {
				_ = httpx.WriteJSON(w, http.StatusForbidden, httpx.ErrorPayload{
					Error: "forbidden: insufficient role permissions",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	originSet := make(map[string]struct{}, len(allowedOrigins))
	hasLocalhost := false
	for _, o := range allowedOrigins {
		trimmed := strings.TrimRight(strings.TrimSpace(o), "/")
		if trimmed != "" && trimmed != "*" {
			originSet[trimmed] = struct{}{}
			if strings.HasPrefix(trimmed, "http://localhost") || strings.HasPrefix(trimmed, "http://127.0.0.1") {
				hasLocalhost = true
			}
		}
	}

	isAllowed := func(origin string) bool {
		if origin == "" {
			return false
		}
		clean := strings.TrimRight(origin, "/")
		if _, ok := originSet[clean]; ok {
			return true
		}
		if hasLocalhost && (strings.HasPrefix(clean, "http://localhost") || strings.HasPrefix(clean, "http://127.0.0.1")) {
			return true
		}
		return false
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if isAllowed(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				reqHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
				if reqHeaders != "" {
					w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
				} else {
					w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, Idempotency-Key, X-Requested-With, Origin, Host, User-Agent, X-Metrics-Token")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractAccessToken(r *http.Request) (token string, fromCookie bool) {
	if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			return strings.TrimSpace(authHeader[7:]), false
		}
	}
	if cookie, err := r.Cookie(AccessCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, true
	}
	return "", false
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (rb *RBAC) verifyCSRFOrigin(r *http.Request) bool {
	secFetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if secFetchSite == "cross-site" {
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-Requested-With")) != "" {
		return true
	}
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return secFetchSite == "same-origin"
	}
	if _, ok := rb.allowedOrigins[origin]; ok {
		return true
	}
	if strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1") {
		for allowed := range rb.allowedOrigins {
			if strings.HasPrefix(allowed, "http://localhost") || strings.HasPrefix(allowed, "http://127.0.0.1") {
				return true
			}
		}
	}
	return false
}
