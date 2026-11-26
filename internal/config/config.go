package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings loaded strictly from environment variables.
type Config struct {
	Env                 string
	Port                string
	AppBaseURL          string
	PublicAPIURL        string
	DatabaseURL         string
	MetricsToken        string
	DBMaxConns          int32
	DBMinConns          int32
	DBMaxConnLifetime   time.Duration
	DBMaxConnIdleTime   time.Duration
	DBHealthCheckPeriod time.Duration
	DBQueryTimeout      time.Duration
	AllowedOrigins      []string
	TrustedProxies      []string
	CookieDomain        string
	CookieSecure        bool
	StaffCacheTTL       time.Duration

	// Staff JWT & Gate QR HMAC
	JWTAccessSecret  string
	JWTRefreshSecret string
	JWTIssuer        string
	JWTAudience      string
	QRSigningSecret  string
	AccessTokenTTL   time.Duration
	RefreshTokenTTL  time.Duration
	TicketQRTTL      time.Duration

	// Direct UPI QR Payment (Manual Admin Verification — No Razorpay / No PhonePe)
	UPIVPA          string
	UPIMerchantName string
	UPIQRImageURL   string

	// Official Meta WhatsApp Cloud API
	WhatsAppAccessToken   string
	WhatsAppPhoneNumberID string
	WhatsAppAPIVersion    string
	WhatsAppWorkers       int
	WhatsAppHTTPTimeout   time.Duration

	// Business Rules
	First99FemaleLimit int64

	// PostgreSQL Rate Limiting
	AuthRateLimitMax        int
	AuthRateLimitWindow     time.Duration
	CheckoutRateLimitMax    int
	CheckoutRateLimitWindow time.Duration
	ScanRateLimitMax        int
	ScanRateLimitWindow     time.Duration
	BarRateLimitMax         int
	BarRateLimitWindow      time.Duration
	RateLimitCacheSize      int
	RateLimitFlushEvery     int
	RunMigrationsOnStart    bool
	MigrationsDir           string

	// HTTP Server & Graceful Shutdown Timeouts
	ShutdownTimeout       time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPReadHeaderTimeout time.Duration
	RequestTimeout        time.Duration
}

func Load() (*Config, error) {
	env := getEnv("APP_ENV", "development")

	dbURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return nil, err
	}
	accessSecret, err := requireEnv("JWT_ACCESS_SECRET")
	if err != nil {
		return nil, err
	}
	refreshSecret, err := requireEnv("JWT_REFRESH_SECRET")
	if err != nil {
		return nil, err
	}
	qrSecret, err := requireEnv("QR_SIGNING_SECRET")
	if err != nil {
		return nil, err
	}
	upiVPA, err := requireEnv("UPI_VPA")
	if err != nil {
		return nil, err
	}

	maxConns, minConns := poolSizing()

	rawOrigins := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	origins := splitCSV(getEnv("ALLOWED_ORIGINS", "http://localhost:3000"))
	if env == "production" {
		if strings.Contains(strings.ToLower(dbURL), "sslmode=disable") {
			return nil, fmt.Errorf("config: sslmode=disable is forbidden when APP_ENV=production")
		}
		if rawOrigins == "" {
			return nil, fmt.Errorf("config: ALLOWED_ORIGINS must be set explicitly in production")
		}
		for _, origin := range origins {
			if origin == "*" {
				return nil, fmt.Errorf("config: wildcard ALLOWED_ORIGINS is forbidden in production")
			}
		}
		if len(accessSecret) < 32 {
			return nil, fmt.Errorf("config: JWT_ACCESS_SECRET must be at least 32 characters in production")
		}
		if len(refreshSecret) < 32 {
			return nil, fmt.Errorf("config: JWT_REFRESH_SECRET must be at least 32 characters in production")
		}
		if len(qrSecret) < 32 {
			return nil, fmt.Errorf("config: QR_SIGNING_SECRET must be at least 32 characters in production")
		}
		if accessSecret == refreshSecret {
			return nil, fmt.Errorf("config: JWT_ACCESS_SECRET and JWT_REFRESH_SECRET must be distinct")
		}
	}

	appBaseURL := strings.TrimRight(getEnv("APP_URL", "http://localhost:3000"), "/")
	publicAPIURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_API_URL")), "/")
	if publicAPIURL == "" {
		publicAPIURL = appBaseURL
	}

	return &Config{
		Env:                 env,
		Port:                getEnv("PORT", "8080"),
		AppBaseURL:          appBaseURL,
		PublicAPIURL:        publicAPIURL,
		DatabaseURL:         dbURL,
		MetricsToken:        strings.TrimSpace(os.Getenv("METRICS_TOKEN")),
		DBMaxConns:          maxConns,
		DBMinConns:          minConns,
		DBMaxConnLifetime:   getEnvDuration("DB_MAX_CONN_LIFETIME", time.Hour),
		DBMaxConnIdleTime:   getEnvDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
		DBHealthCheckPeriod: getEnvDuration("DB_HEALTH_CHECK_PERIOD", time.Minute),
		DBQueryTimeout:      getEnvDuration("DB_QUERY_TIMEOUT", 3*time.Second),
		AllowedOrigins:      origins,
		TrustedProxies:      splitCSV(os.Getenv("TRUSTED_PROXIES")),
		CookieDomain:        strings.TrimSpace(os.Getenv("COOKIE_DOMAIN")),
		CookieSecure:        getEnvBool("COOKIE_SECURE", env == "production"),
		StaffCacheTTL:       getEnvDuration("STAFF_CACHE_TTL", 10*time.Second),

		JWTAccessSecret:  accessSecret,
		JWTRefreshSecret: refreshSecret,
		JWTIssuer:        getEnv("JWT_ISSUER", "venu-api"),
		JWTAudience:      getEnv("JWT_AUDIENCE", "venu-staff"),
		QRSigningSecret:  qrSecret,
		AccessTokenTTL:   getEnvDuration("ACCESS_TOKEN_TTL", 8*time.Hour),
		RefreshTokenTTL:  getEnvDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		TicketQRTTL:      getEnvDuration("TICKET_QR_TTL", 24*time.Hour),

		UPIVPA:          upiVPA,
		UPIMerchantName: getEnv("UPI_MERCHANT_NAME", "House Untold Live Events"),
		UPIQRImageURL:   strings.TrimSpace(os.Getenv("UPI_QR_IMAGE_URL")),

		WhatsAppAccessToken:   strings.TrimSpace(os.Getenv("WHATSAPP_ACCESS_TOKEN")),
		WhatsAppPhoneNumberID: strings.TrimSpace(os.Getenv("WHATSAPP_PHONE_NUMBER_ID")),
		WhatsAppAPIVersion:    getEnv("WHATSAPP_API_VERSION", "v21.0"),
		WhatsAppWorkers:       getEnvInt("WHATSAPP_WORKERS", 6),
		WhatsAppHTTPTimeout:   getEnvDuration("WHATSAPP_HTTP_TIMEOUT", 10*time.Second),

		First99FemaleLimit: int64(getEnvInt("FIRST_99_FEMALE_LIMIT", 99)),

		AuthRateLimitMax:        getEnvInt("RATE_LIMIT_AUTH_MAX", 15),
		AuthRateLimitWindow:     getEnvDuration("RATE_LIMIT_AUTH_WINDOW", time.Minute),
		CheckoutRateLimitMax:    getEnvInt("RATE_LIMIT_CHECKOUT_MAX", 20),
		CheckoutRateLimitWindow: getEnvDuration("RATE_LIMIT_CHECKOUT_WINDOW", time.Minute),
		ScanRateLimitMax:        getEnvInt("RATE_LIMIT_SCAN_MAX", 120),
		ScanRateLimitWindow:     getEnvDuration("RATE_LIMIT_SCAN_WINDOW", time.Minute),
		BarRateLimitMax:         getEnvInt("RATE_LIMIT_BAR_MAX", 180),
		BarRateLimitWindow:      getEnvDuration("RATE_LIMIT_BAR_WINDOW", time.Minute),
		RateLimitCacheSize:      getEnvInt("RATE_LIMIT_CACHE_SIZE", 16384),
		RateLimitFlushEvery:     getEnvInt("RATE_LIMIT_FLUSH_EVERY", 100),
		RunMigrationsOnStart:    getEnvBool("RUN_MIGRATIONS_ON_START", true),
		MigrationsDir:           getEnv("MIGRATIONS_DIR", "db/migrations"),

		ShutdownTimeout:       getEnvDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		HTTPReadTimeout:       getEnvDuration("HTTP_READ_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout:      getEnvDuration("HTTP_WRITE_TIMEOUT", 20*time.Second),
		HTTPIdleTimeout:       getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPReadHeaderTimeout: getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		RequestTimeout:        getEnvDuration("HTTP_REQUEST_TIMEOUT", 15*time.Second),
	}, nil
}

func poolSizing() (int32, int32) {
	pgMax := getEnvInt("POSTGRES_MAX_CONNECTIONS", 100)
	instances := getEnvInt("API_INSTANCES", 2)
	reserve := getEnvInt("DB_CONNECTION_RESERVE", 60)
	cap := getEnvInt("DB_MAX_CONNS_CAP", 20)

	if pgMax < 10 {
		pgMax = 100
	}
	if instances < 1 {
		instances = 2
	}
	if reserve < 0 {
		reserve = 60
	}
	if cap < 5 {
		cap = 20
	}

	available := pgMax - reserve
	if available < 5 {
		available = 5
	}
	maxPerInstance := available / instances
	if maxPerInstance < 5 {
		maxPerInstance = 5
	}
	if maxPerInstance > cap {
		maxPerInstance = cap
	}

	maxOverride := getEnvInt("DB_MAX_CONNS", 0)
	if maxOverride > 0 {
		maxPerInstance = maxOverride
	}
	if maxPerInstance > pgMax {
		maxPerInstance = pgMax
	}

	minPerInstance := maxPerInstance / 4
	if minPerInstance < 2 {
		minPerInstance = 2
	}
	if minPerInstance > 10 {
		minPerInstance = 10
	}
	if minOverride := getEnvInt("DB_MIN_CONNS", 0); minOverride > 0 {
		minPerInstance = minOverride
	}
	if minPerInstance > maxPerInstance {
		minPerInstance = maxPerInstance
	}

	return int32(maxPerInstance), int32(minPerInstance)
}

func requireEnv(key string) (string, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return "", fmt.Errorf("config: missing required environment variable %s", key)
	}
	return val, nil
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
