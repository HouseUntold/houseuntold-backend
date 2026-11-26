package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/db"
)

type staffQuerier struct {
	db.Querier
	staff db.StaffAccount
	err   error
}

func (q staffQuerier) GetStaffByID(context.Context, uuid.UUID) (db.GetStaffByIDRow, error) {
	return db.GetStaffByIDRow{
		ID:           q.staff.ID,
		Username:     q.staff.Username,
		PasswordHash: q.staff.PasswordHash,
		Role:         q.staff.Role,
		IsActive:     q.staff.IsActive,
		TokenVersion: q.staff.TokenVersion,
		CreatedAt:    q.staff.CreatedAt,
	}, q.err
}

func newTestTokens() *auth.TokenManager {
	return auth.NewTokenManager("access-secret-32-bytes-minimum!!", "refresh-secret-32-bytes-minimum!", "venu-staff", "venu-api", time.Minute, time.Hour)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestContextHelpers(t *testing.T) {
	staff := db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleSCANNER}
	ctx := ContextWithStaff(context.Background(), staff)

	got, ok := StaffFromContext(ctx)
	if !ok || got.ID != staff.ID {
		t.Fatalf("StaffFromContext failed: %+v %v", got, ok)
	}
	if id, ok := StaffIDFromContext(ctx); !ok || id != staff.ID {
		t.Fatalf("StaffIDFromContext failed: %s %v", id, ok)
	}
	if role, ok := RoleFromContext(ctx); !ok || role != db.StaffRoleSCANNER {
		t.Fatalf("RoleFromContext failed: %s %v", role, ok)
	}
	if _, ok := StaffFromContext(context.Background()); ok {
		t.Fatal("empty context must not contain staff")
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for key, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cache-Control":                "no-store, no-cache, must-revalidate, private",
		"Pragma":                       "no-cache",
	} {
		if got := rec.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestRealIPTrustsOnlyConfiguredProxy(t *testing.T) {
	var got string
	handler := RealIP([]string{"127.0.0.1"})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got != "203.0.113.9" {
		t.Fatalf("trusted proxy forwarded-for got %q", got)
	}

	got = ""
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.4:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got != "198.51.100.4:1234" {
		t.Fatalf("untrusted forwarded-for must be ignored, got %q", got)
	}

	got = ""
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Real-IP", "192.0.2.10")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got != "192.0.2.10" {
		t.Fatalf("trusted proxy real-ip got %q", got)
	}
}

func TestCORS(t *testing.T) {
	handler := CORS([]string{"https://venu.test", "*", " "})(okHandler())

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://venu.test")
	handler.ServeHTTP(rec, r)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://venu.test" || rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("allowed origin headers missing: %v", rec.Header())
	}

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://evil.test")
	handler.ServeHTTP(rec, r)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin must not get CORS headers: %v", rec.Header())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight got %d", rec.Code)
	}
}

func TestRBACAuthenticate(t *testing.T) {
	staff := db.StaffAccount{ID: uuid.New(), Username: "scanner", Role: db.StaffRoleSCANNER, IsActive: true}
	pair, err := newTestTokens().IssueStaffTokens(staff)
	if err != nil {
		t.Fatalf("issue tokens: %v", err)
	}

	t.Run("missing token", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{staff: staff}, cache.NewStaffRoleCache(time.Minute))
		rec := httptest.NewRecorder()
		rbac.Authenticate(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", rec.Code)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{staff: staff}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer not-a-jwt")
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", rec.Code)
		}
	})

	t.Run("valid bearer with db lookup", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{staff: staff}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("valid cookie requires origin on mutations", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{staff: staff}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: pair.AccessToken})
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d", rec.Code)
		}
	})

	t.Run("valid cookie mutation with allowed origin", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{staff: staff}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: pair.AccessToken})
		r.Header.Set("Origin", "https://venu.test")
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("inactive staff rejected", func(t *testing.T) {
		inactive := staff
		inactive.IsActive = false
		rbac := newRBAC(staffQuerier{staff: inactive}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", rec.Code)
		}
	})

	t.Run("token version mismatch rejected", func(t *testing.T) {
		revoked := staff
		revoked.TokenVersion = staff.TokenVersion + 1
		rbac := newRBAC(staffQuerier{staff: revoked}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "session revoked") {
			t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("role mismatch rejected and cache invalidated", func(t *testing.T) {
		c := cache.NewStaffRoleCache(time.Minute)
		stale := staff
		stale.Role = db.StaffRoleADMIN
		c.Set(stale)
		rbac := newRBAC(staffQuerier{staff: staff}, c)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", rec.Code)
		}
		if _, hit := c.Get(staff.ID); hit {
			t.Fatal("stale role must be evicted")
		}
	})

	t.Run("db error rejected", func(t *testing.T) {
		rbac := newRBAC(staffQuerier{err: errLookup}, nil)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", rec.Code)
		}
	})

	t.Run("cached role avoids db", func(t *testing.T) {
		c := cache.NewStaffRoleCache(time.Minute)
		c.Set(staff)
		rbac := newRBAC(staffQuerier{err: errLookup}, c)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rbac.Authenticate(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got %d", rec.Code)
		}
	})
}

func TestRequireRole(t *testing.T) {
	guard := newRBAC(staffQuerier{}, nil).RequireRole("SCANNER", "ADMIN")

	rec := httptest.NewRecorder()
	guard(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing role got %d", rec.Code)
	}

	ctx := ContextWithStaff(context.Background(), db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleBARTENDER})
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	guard(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong role got %d", rec.Code)
	}

	ctx = ContextWithStaff(context.Background(), db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleSCANNER})
	r = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	guard(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("allowed role got %d", rec.Code)
	}
}

func TestExtractAccessTokenAndMutatingMethod(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "bearer abc")
	token, fromCookie := extractAccessToken(r)
	if token != "abc" || fromCookie {
		t.Fatalf("bearer extraction failed: %q %v", token, fromCookie)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "cookie-token"})
	token, fromCookie = extractAccessToken(r)
	if token != "cookie-token" || !fromCookie {
		t.Fatalf("cookie extraction failed: %q %v", token, fromCookie)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !isMutatingMethod(method) {
			t.Fatalf("%s must be mutating", method)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if isMutatingMethod(method) {
			t.Fatalf("%s must not be mutating", method)
		}
	}
}

func TestVerifyCSRFOrigin(t *testing.T) {
	rbac := newRBAC(staffQuerier{}, nil)

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Requested-With", "fetch")
	if !rbac.verifyCSRFOrigin(r) {
		t.Fatal("X-Requested-With must be accepted")
	}

	r = httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if !rbac.verifyCSRFOrigin(r) {
		t.Fatal("same-origin must be accepted")
	}

	r = httptest.NewRequest(http.MethodPost, "/", nil)
	if rbac.verifyCSRFOrigin(r) {
		t.Fatal("missing origin must be rejected")
	}
}

func newRBAC(q db.Querier, c *cache.StaffRoleCache) *RBAC {
	return NewRBAC(newTestTokens(), q, c, time.Second, []string{"https://venu.test"})
}

var errLookup = context.Canceled
