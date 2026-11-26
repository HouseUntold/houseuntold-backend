package config

import (
	"testing"
	"time"
)

func TestLoad_RequiredVariables(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=disable")
	t.Setenv("JWT_ACCESS_SECRET", "access-secret")
	t.Setenv("JWT_REFRESH_SECRET", "refresh-secret")
	t.Setenv("QR_SIGNING_SECRET", "qr-secret")
	t.Setenv("UPI_VPA", "merchant@upi")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.DatabaseURL == "" || cfg.QRSigningSecret != "qr-secret" || cfg.UPIVPA != "merchant@upi" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.DBQueryTimeout != 3*time.Second || cfg.AccessTokenTTL != 8*time.Hour || cfg.RefreshTokenTTL != 30*24*time.Hour {
		t.Fatalf("unexpected defaults: timeout=%s ttl=%s", cfg.DBQueryTimeout, cfg.AccessTokenTTL)
	}
	if cfg.CookieSecure {
		t.Fatalf("cookie secure must default false in development")
	}
}

func TestLoad_MissingRequiredVariable(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "JWT_ACCESS_SECRET", "JWT_REFRESH_SECRET", "QR_SIGNING_SECRET", "UPI_VPA"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=disable")
			t.Setenv("JWT_ACCESS_SECRET", "access-secret")
			t.Setenv("JWT_REFRESH_SECRET", "refresh-secret")
			t.Setenv("QR_SIGNING_SECRET", "qr-secret")
			t.Setenv("UPI_VPA", "merchant@upi")
			t.Setenv(key, " ")
			if _, err := Load(); err == nil {
				t.Fatalf("expected missing %s to fail", key)
			}
		})
	}
}

func TestLoad_OverridesAndInvalidFallbacks(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=require")
	t.Setenv("JWT_ACCESS_SECRET", "access-secret-32-chars-long-minimum-prod")
	t.Setenv("JWT_REFRESH_SECRET", "refresh-secret-32-chars-long-minimum-prod")
	t.Setenv("QR_SIGNING_SECRET", "qr-secret-32-chars-long-minimum-prod")
	t.Setenv("UPI_VPA", "merchant@upi")
	t.Setenv("APP_ENV", "production")
	t.Setenv("ALLOWED_ORIGINS", " https://a.example , ,https://b.example ")
	t.Setenv("DB_MAX_CONNS", "12")
	t.Setenv("DB_MIN_CONNS", "oops")
	t.Setenv("DB_QUERY_TIMEOUT", "5s")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("FIRST_99_FEMALE_LIMIT", "25")
	t.Setenv("MIN_GUEST_AGE", "25")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "wa-token")
	t.Setenv("WHATSAPP_WORKERS", "3")
	t.Setenv("HTTP_READ_TIMEOUT", "bad")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://a.example" {
		t.Fatalf("unexpected origins: %v", cfg.AllowedOrigins)
	}
	if cfg.DBMaxConns != 12 || cfg.DBMinConns != 3 {
		t.Fatalf("unexpected pool config: %d/%d", cfg.DBMaxConns, cfg.DBMinConns)
	}
	if cfg.DBQueryTimeout != 5*time.Second || cfg.HTTPReadTimeout != 10*time.Second {
		t.Fatalf("unexpected timeouts: %s %s", cfg.DBQueryTimeout, cfg.HTTPReadTimeout)
	}
	if !cfg.CookieSecure || cfg.First99FemaleLimit != 25 {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
	if cfg.WhatsAppAccessToken != "wa-token" || cfg.WhatsAppWorkers != 3 {
		t.Fatalf("unexpected whatsapp config: %+v", cfg)
	}
}

func TestLoad_ProductionSecurityGuards(t *testing.T) {
	setRequired := func() {
		t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=require")
		t.Setenv("JWT_ACCESS_SECRET", "access-secret-32-chars-long-minimum-prod")
		t.Setenv("JWT_REFRESH_SECRET", "refresh-secret-32-chars-long-minimum-prod")
		t.Setenv("QR_SIGNING_SECRET", "qr-secret-32-chars-long-minimum-prod")
		t.Setenv("UPI_VPA", "merchant@upi")
		t.Setenv("APP_ENV", "production")
		t.Setenv("ALLOWED_ORIGINS", "https://venu.example")
	}

	t.Run("rejects disabled ssl", func(t *testing.T) {
		setRequired()
		t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=disable")
		if _, err := Load(); err == nil {
			t.Fatal("expected production sslmode=disable to fail")
		}
	})

	t.Run("requires explicit origins", func(t *testing.T) {
		setRequired()
		t.Setenv("ALLOWED_ORIGINS", " ")
		if _, err := Load(); err == nil {
			t.Fatal("expected missing production ALLOWED_ORIGINS to fail")
		}
	})

	t.Run("rejects wildcard origins", func(t *testing.T) {
		setRequired()
		t.Setenv("ALLOWED_ORIGINS", "*")
		if _, err := Load(); err == nil {
			t.Fatal("expected wildcard ALLOWED_ORIGINS to fail")
		}
	})

	t.Run("rejects weak jwt access secret in production", func(t *testing.T) {
		setRequired()
		t.Setenv("JWT_ACCESS_SECRET", "short-secret-under-32-chars")
		if _, err := Load(); err == nil {
			t.Fatal("expected production short JWT_ACCESS_SECRET (<32 chars) to fail")
		}
	})

	t.Run("rejects weak qr signing secret in production", func(t *testing.T) {
		setRequired()
		t.Setenv("QR_SIGNING_SECRET", "short-qr-secret-under-32-chars")
		if _, err := Load(); err == nil {
			t.Fatal("expected production short QR_SIGNING_SECRET (<32 chars) to fail")
		}
	})
}

func TestPoolSizingFormula(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/venu?sslmode=require")
	t.Setenv("JWT_ACCESS_SECRET", "access-secret")
	t.Setenv("JWT_REFRESH_SECRET", "refresh-secret")
	t.Setenv("QR_SIGNING_SECRET", "qr-secret")
	t.Setenv("UPI_VPA", "merchant@upi")
	t.Setenv("DB_MAX_CONNS", "")
	t.Setenv("DB_MIN_CONNS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.DBMaxConns != 20 || cfg.DBMinConns != 5 {
		t.Fatalf("unexpected free-tier pool sizing: %d/%d", cfg.DBMaxConns, cfg.DBMinConns)
	}

	t.Setenv("POSTGRES_MAX_CONNECTIONS", "200")
	t.Setenv("API_INSTANCES", "4")
	t.Setenv("DB_CONNECTION_RESERVE", "40")
	t.Setenv("DB_MAX_CONNS_CAP", "50")

	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.DBMaxConns != 40 || cfg.DBMinConns != 10 {
		t.Fatalf("unexpected scaled pool sizing: %d/%d", cfg.DBMaxConns, cfg.DBMinConns)
	}
}

func TestHelpers(t *testing.T) {
	if getEnv("MISSING_KEY_TEST", "fallback") != "fallback" {
		t.Fatal("getEnv must use fallback")
	}
	if getEnvInt("MISSING_INT_TEST", 7) != 7 || getEnvBool("MISSING_BOOL_TEST", true) != true {
		t.Fatal("numeric/bool helpers must use fallback")
	}
	if getEnvDuration("MISSING_DUR_TEST", time.Second) != time.Second {
		t.Fatal("duration helper must use fallback")
	}
	if got := splitCSV(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected splitCSV result: %v", got)
	}
}
