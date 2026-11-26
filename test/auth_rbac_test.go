package test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/venu-live/venu-api/internal/middleware"
)

// Covers Routes 7–9 + Security Headers, CORS, CSRF Origin Enforcement, RBAC & Postgres Rate Limiting:
//  7. POST /api/auth/login
//  8. POST /api/auth/refresh
//  9. POST /api/auth/logout
func TestStaffAuthCookiesCSRFAndRoleIsolation(t *testing.T) {
	env := newTestEnv(t)

	// Invalid credentials -> 401
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "admin_user",
		"password": "WrongPassword!99",
	}, nil), http.StatusUnauthorized)

	// 7. POST /api/auth/login -> 200 + strict HttpOnly SameSite=Strict cookies + SecurityHeaders
	rec := env.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "bar_user",
		"password": "BarPass!123",
	}, map[string]string{"Origin": "https://venu.test"})
	assertStatus(t, rec, http.StatusOK)

	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing X-Frame-Options: DENY")
	}
	if rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("missing Cross-Origin-Resource-Policy: same-origin")
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("expected Cache-Control: no-store, got %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://venu.test" {
		t.Fatalf("expected CORS allow origin https://venu.test, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	setCookies := rec.Header().Values("Set-Cookie")
	if len(setCookies) != 2 {
		t.Fatalf("expected 2 Set-Cookie headers (access + refresh), got %d", len(setCookies))
	}
	for _, sc := range setCookies {
		if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") {
			t.Fatalf("cookie lacks HttpOnly or SameSite=Strict: %s", sc)
		}
	}
	cookies := rec.Result().Cookies()

	// Role cache must now hold bar_user
	if _, hit := env.roleCache.Get(env.bartenderID); !hit {
		t.Fatal("expected StaffRoleCache to be populated after login")
	}

	// Protected route without auth -> 401
	assertStatus(t, env.doRequest(http.MethodGet, "/api/counter/menu", nil, nil), http.StatusUnauthorized)

	// Bartender accessing ADMIN route -> 403 Forbidden
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/bookings", nil, nil, cookies...), http.StatusForbidden)

	// Cross-site CSRF mutation using session cookie -> 403 Forbidden
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/menu", map[string]any{
		"name":       "CrossSite Shot",
		"category":   "DRINK",
		"unit_price": 500,
	}, map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
	}, cookies...), http.StatusForbidden)

	// 8. POST /api/auth/refresh -> rejects cross-site cookie refresh (403), accepts same-origin (200)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/refresh", nil, map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
	}, cookies...), http.StatusForbidden)

	recRefresh := env.doRequest(http.MethodPost, "/api/auth/refresh", nil, map[string]string{
		"Origin":         "https://venu.test",
		"Sec-Fetch-Site": "same-origin",
	}, cookies...)
	assertStatus(t, recRefresh, http.StatusOK)

	// 9. POST /api/auth/logout -> rejects cross-site cookie logout (403), accepts same-origin (200) and evicts StaffRoleCache
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/logout", nil, map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
	}, cookies...), http.StatusForbidden)

	recLogout := env.doRequest(http.MethodPost, "/api/auth/logout", nil, map[string]string{
		"Origin":         "https://venu.test",
		"Sec-Fetch-Site": "same-origin",
	}, cookies...)
	assertStatus(t, recLogout, http.StatusOK)

	if _, hit := env.roleCache.Get(env.bartenderID); hit {
		t.Fatal("expected StaffRoleCache to be evicted immediately on logout")
	}

	var clearedAccess bool
	for _, c := range recLogout.Result().Cookies() {
		if c.Name == middleware.AccessCookieName && c.MaxAge < 0 {
			clearedAccess = true
		}
	}
	if !clearedAccess {
		t.Fatal("expected access cookie to be cleared on logout")
	}
}

func TestPostgresRateLimitingEnforced(t *testing.T) {
	env := newTestEnv(t)

	for i := 0; i < env.cfg.AuthRateLimitMax; i++ {
		rec := env.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
			"username": "nobody",
			"password": "badpassword",
		}, nil)
		assertStatus(t, rec, http.StatusUnauthorized)
		if i == 0 && rec.Header().Get("X-RateLimit-Limit") == "" {
			t.Fatal("missing X-RateLimit-Limit header")
		}
	}

	rec429 := env.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "nobody",
		"password": "badpassword",
	}, nil)
	assertStatus(t, rec429, http.StatusTooManyRequests)
	if rec429.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 Too Many Requests")
	}
}
