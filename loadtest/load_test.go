package loadtest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type seedManifest struct {
	AdminID            string `json:"admin_id"`
	ScannerID          string `json:"scanner_id"`
	BartenderID        string `json:"bartender_id"`
	AdminToken         string `json:"admin_token"`
	AdminRefreshToken  string `json:"admin_refresh_token"`
	ScannerToken       string `json:"scanner_token"`
	BartenderToken     string `json:"bartender_token"`
	EventID            string `json:"event_id"`
	EndedEventID       string `json:"ended_event_id"`
	CoupleCategoryID   string `json:"couple_category_id"`
	FemaleCategoryID   string `json:"female_category_id"`
	PublicTicketID     string `json:"public_ticket_id"`
	GateTicketID       string `json:"gate_ticket_id"`
	GateQRToken        string `json:"gate_qr_token"`
	GateBarCode        string `json:"gate_bar_code"`
	BarPartialTicketID string `json:"bar_partial_ticket_id"`
	BarPartialBarCode  string `json:"bar_partial_bar_code"`
	BarConcurrentID    string `json:"bar_concurrent_ticket_id"`
	BarConcurrentCode  string `json:"bar_concurrent_bar_code"`
	BeerItemID         string `json:"beer_item_id"`
	BurgerItemID       string `json:"burger_item_id"`
	CocktailItemID     string `json:"cocktail_item_id"`
	PremiumItemID      string `json:"premium_item_id"`
	TotalEvents        int    `json:"total_events"`
	TotalMenuItems     int    `json:"total_menu_items"`
	TotalTickets       int    `json:"total_tickets"`
}

type callResult struct {
	Status  int
	Body    []byte
	Headers http.Header
	Elapsed time.Duration
	Err     error
}

type metricSnapshot struct {
	Count  int
	Errors int
	P50    time.Duration
	P95    time.Duration
	P99    time.Duration
	Max    time.Duration
	RPS    float64
}

type metrics struct {
	mu        sync.Mutex
	latencies []time.Duration
	statuses  map[int]int
	started   time.Time
}

func newMetrics() *metrics {
	return &metrics{statuses: map[int]int{}, started: time.Now()}
}

func (m *metrics) record(status int, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, elapsed)
	m.statuses[status]++
}

func (m *metrics) snapshot() metricSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.latencies) == 0 {
		return metricSnapshot{}
	}
	sorted := append([]time.Duration(nil), m.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pick := func(q float64) time.Duration {
		idx := int(math.Ceil(q*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	errors := 0
	for status, count := range m.statuses {
		if status >= 500 {
			errors += count
		}
	}
	seconds := time.Since(m.started).Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	return metricSnapshot{
		Count:  len(sorted),
		Errors: errors,
		P50:    pick(0.50),
		P95:    pick(0.95),
		P99:    pick(0.99),
		Max:    sorted[len(sorted)-1],
		RPS:    float64(len(sorted)) / seconds,
	}
}

type testState struct {
	baseURL string
	client  *http.Client
	db      *sql.DB
	seed    seedManifest
	metrics *metrics
}

var (
	stateOnce sync.Once
	stateRef  *testState
	stateErr  error
)

func getState(t *testing.T) *testState {
	t.Helper()
	if os.Getenv("VENU_LOADTEST") != "1" {
		t.Skip("set VENU_LOADTEST=1 to run the live-server load suite")
	}
	stateOnce.Do(func() {
		baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("VENU_BASE_URL")), "/")
		manifestPath := strings.TrimSpace(os.Getenv("VENU_SEED_MANIFEST"))
		dsn := strings.TrimSpace(os.Getenv("VENU_TEST_DATABASE_URL"))
		if baseURL == "" || manifestPath == "" || dsn == "" {
			stateErr = fmt.Errorf("VENU_BASE_URL, VENU_SEED_MANIFEST and VENU_TEST_DATABASE_URL are required")
			return
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			stateErr = fmt.Errorf("read manifest: %w", err)
			return
		}
		var seed seedManifest
		if err := json.Unmarshal(raw, &seed); err != nil {
			stateErr = fmt.Errorf("decode manifest: %w", err)
			return
		}
		dbConn, err := sql.Open("pgx", dsn)
		if err != nil {
			stateErr = fmt.Errorf("open db: %w", err)
			return
		}
		dbConn.SetMaxOpenConns(10)
		stateRef = &testState{
			baseURL: baseURL,
			client: &http.Client{
				Timeout: 30 * time.Second,
				Transport: &http.Transport{
					MaxIdleConns:        1000,
					MaxIdleConnsPerHost: 500,
					IdleConnTimeout:     90 * time.Second,
				},
			},
			db:      dbConn,
			seed:    seed,
			metrics: newMetrics(),
		}
	})
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	return stateRef
}

func (s *testState) call(method, path, token string, payload any, headers ...map[string]string) callResult {
	return s.callWithIP(method, path, token, payload, "", headers...)
}

func (s *testState) callWithIP(method, path, token string, payload any, clientIP string, headers ...map[string]string) callResult {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return callResult{Err: err}
		}
		body = encoded
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return callResult{Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "loadtest")
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	for _, header := range headers {
		for key, value := range header {
			req.Header.Set(key, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := s.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		s.metrics.record(0, elapsed)
		return callResult{Err: err, Elapsed: elapsed}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		s.metrics.record(resp.StatusCode, elapsed)
		return callResult{Status: resp.StatusCode, Headers: resp.Header, Err: err, Elapsed: elapsed}
	}
	s.metrics.record(resp.StatusCode, elapsed)
	return callResult{Status: resp.StatusCode, Body: respBody, Headers: resp.Header, Elapsed: elapsed}
}

func (s *testState) clearRateLimits(t *testing.T) {
	t.Helper()
	if _, err := s.db.Exec(`DELETE FROM rate_limits`); err != nil {
		t.Fatalf("clear rate_limits: %v", err)
	}
}

func (s *testState) ticketBalance(t *testing.T, barCode string) int32 {
	t.Helper()
	var balance int32
	err := s.db.QueryRow(`SELECT food_balance FROM tickets WHERE bar_code = $1`, barCode).Scan(&balance)
	if err != nil {
		t.Fatalf("read balance for %s: %v", barCode, err)
	}
	return balance
}

func (s *testState) rateLimitHits(t *testing.T, endpointPrefix string) int32 {
	t.Helper()
	var hits int32
	err := s.db.QueryRow(`SELECT COALESCE(MAX(hits), 0) FROM rate_limits WHERE endpoint LIKE $1`, endpointPrefix+"%").Scan(&hits)
	if err != nil {
		t.Fatalf("read rate limit hits: %v", err)
	}
	return hits
}

func runConcurrent(count int, fn func(int) callResult) []callResult {
	results := make([]callResult, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

func decode[T any](t *testing.T, res callResult) T {
	t.Helper()
	var out T
	if res.Err != nil {
		t.Fatalf("request failed: %v", res.Err)
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode response %q: %v", string(res.Body), err)
	}
	return out
}

func assertStatus(t *testing.T, name string, res callResult, want ...int) {
	t.Helper()
	if res.Err != nil {
		t.Fatalf("%s: request error: %v", name, res.Err)
	}
	for _, status := range want {
		if res.Status == status {
			return
		}
	}
	t.Fatalf("%s: got status %d want %v body=%s", name, res.Status, want, string(res.Body))
}

func countStatus(results []callResult, status int) int {
	count := 0
	for _, res := range results {
		if res.Status == status {
			count++
		}
	}
	return count
}

func failOn5xx(t *testing.T, name string, results []callResult) {
	t.Helper()
	for _, res := range results {
		if res.Err != nil {
			t.Fatalf("%s: transport error: %v", name, res.Err)
		}
		if res.Status >= 500 {
			t.Fatalf("%s: unexpected 5xx %d body=%s", name, res.Status, string(res.Body))
		}
	}
}

type checkoutRequest struct {
	EventID          string `json:"event_id"`
	CategoryID       string `json:"category_id"`
	ContactPhone     string `json:"contact_phone"`
	ContactEmail     string `json:"contact_email"`
	UPITransactionID string `json:"upi_transaction_id,omitempty"`
	Guests           []struct {
		Name             string `json:"name"`
		Age              int32  `json:"age"`
		Gender           string `json:"gender"`
		IsPrimaryContact bool   `json:"is_primary_contact"`
	} `json:"guests"`
}

type checkoutResponse struct {
	BookingID string `json:"booking_id"`
	Status    string `json:"status"`
}

func makeCheckout(m seedManifest, i int, withUTR bool) checkoutRequest {
	req := checkoutRequest{
		EventID:      m.EventID,
		CategoryID:   m.CoupleCategoryID,
		ContactPhone: fmt.Sprintf("+9199%08d", i%100000000),
		ContactEmail: fmt.Sprintf("swarm%d@example.test", i),
		Guests: []struct {
			Name             string `json:"name"`
			Age              int32  `json:"age"`
			Gender           string `json:"gender"`
			IsPrimaryContact bool   `json:"is_primary_contact"`
		}{
			{Name: "Swarm Guest M", Age: 28, Gender: "MALE", IsPrimaryContact: true},
			{Name: "Swarm Guest F", Age: 27, Gender: "FEMALE"},
		},
	}
	if withUTR {
		req.UPITransactionID = fmt.Sprintf("SWARM%012d%03d", time.Now().Unix(), i)
	}
	return req
}

func TestThunderingHerdGateCheckIn(t *testing.T) {
	s := getState(t)
	s.clearRateLimits(t)
	results := runConcurrent(250, func(int) callResult {
		return s.call(http.MethodPost, "/api/gate/check-in", s.seed.ScannerToken, map[string]string{
			"qr_token": s.seed.GateQRToken,
		})
	})
	failOn5xx(t, "gate herd", results)
	ok := countStatus(results, http.StatusOK)
	conflicts := countStatus(results, http.StatusConflict)
	if ok != 1 || conflicts != 249 {
		t.Fatalf("gate herd: got 200=%d 409=%d want 200=1 409=249", ok, conflicts)
	}
	t.Logf("thundering herd: 1 check-in, 249 conflicts, p99=%s", s.metrics.snapshot().P99)
}

func TestRealWorldBartenderDeductions(t *testing.T) {
	s := getState(t)
	s.clearRateLimits(t)
	m := s.seed

	steps := []struct {
		name       string
		items      []map[string]any
		strict     bool
		wantStatus int
		wantWallet int32
		wantRemain int32
	}{
		{"two_beers", []map[string]any{{"menu_item_id": m.BeerItemID, "quantity": 2}}, true, 200, 500, 2500},
		{"burger_cocktail", []map[string]any{{"menu_item_id": m.BurgerItemID, "quantity": 1}, {"menu_item_id": m.CocktailItemID, "quantity": 1}}, true, 200, 1000, 1500},
		{"premium_bottle", []map[string]any{{"menu_item_id": m.PremiumItemID, "quantity": 1}}, true, 200, 1500, 0},
	}
	for i, step := range steps {
		res := s.call(http.MethodPost, "/api/counter/redeem", m.BartenderToken, map[string]any{
			"bar_code":                      m.BarPartialBarCode,
			"items":                         step.items,
			"idempotency_key":               fmt.Sprintf("load-partial-%d-%d", time.Now().UnixNano(), i),
			"require_full_wallet_deduction": true,
		})
		assertStatus(t, step.name, res, step.wantStatus)
		parsed := decode[struct {
			WalletDeducted   int32 `json:"wallet_deducted"`
			RemainingBalance int32 `json:"remaining_balance"`
		}](t, res)
		if parsed.WalletDeducted != step.wantWallet || parsed.RemainingBalance != step.wantRemain {
			t.Fatalf("%s: wallet=%d remaining=%d want %d/%d", step.name, parsed.WalletDeducted, parsed.RemainingBalance, step.wantWallet, step.wantRemain)
		}
	}

	reject := s.call(http.MethodPost, "/api/counter/redeem", m.BartenderToken, map[string]any{
		"bar_code":                      m.BarPartialBarCode,
		"items":                         []map[string]any{{"menu_item_id": m.BeerItemID, "quantity": 1}},
		"idempotency_key":               fmt.Sprintf("load-reject-%d", time.Now().UnixNano()),
		"require_full_wallet_deduction": true,
	})
	assertStatus(t, "insufficient_funds", reject, http.StatusConflict)

	stamp := time.Now().UnixNano()
	results := runConcurrent(10, func(i int) callResult {
		return s.call(http.MethodPost, "/api/counter/redeem", m.BartenderToken, map[string]any{
			"bar_code":                      m.BarConcurrentCode,
			"items":                         []map[string]any{{"menu_item_id": m.BurgerItemID, "quantity": 1}},
			"idempotency_key":               fmt.Sprintf("load-race-%d-%d", stamp, i),
			"require_full_wallet_deduction": true,
		})
	})
	failOn5xx(t, "bar concurrency", results)
	ok, conflicts := countStatus(results, http.StatusOK), countStatus(results, http.StatusConflict)
	if ok != 6 || conflicts != 4 {
		t.Fatalf("bar concurrency: got 200=%d 409=%d want 200=6 409=4", ok, conflicts)
	}
	if balance := s.ticketBalance(t, m.BarConcurrentCode); balance != 0 {
		t.Fatalf("bar concurrency: remaining balance %d want 0", balance)
	}
	t.Logf("bartender: 500+1000+1500=3000, strict race 6/10, p99=%s", s.metrics.snapshot().P99)
}

func TestAdminUTRSwarm(t *testing.T) {
	s := getState(t)
	s.clearRateLimits(t)

	baseStamp := time.Now().Unix()
	checkouts := runConcurrent(250, func(i int) callResult {
		req := makeCheckout(s.seed, i, true)
		req.UPITransactionID = fmt.Sprintf("SWARM%012d%03d", baseStamp, i)
		return s.call(http.MethodPost, "/api/checkout/init", "", req, map[string]string{
			"Idempotency-Key": fmt.Sprintf("swarm-%d-%03d", baseStamp, i),
		})
	})
	failOn5xx(t, "checkout swarm", checkouts)
	if got := countStatus(checkouts, http.StatusCreated); got != 250 {
		t.Fatalf("checkout swarm: got 201=%d want 250", got)
	}

	bookingIDs := make([]string, 0, 250)
	for _, res := range checkouts {
		parsed := decode[checkoutResponse](t, res)
		if parsed.BookingID == "" {
			t.Fatal("checkout swarm: empty booking_id")
		}
		bookingIDs = append(bookingIDs, parsed.BookingID)
	}

	verifies := runConcurrent(250, func(i int) callResult {
		return s.call(http.MethodPost, "/api/admin/bookings/"+bookingIDs[i]+"/verify", s.seed.AdminToken, map[string]string{
			"action": "APPROVE",
		})
	})
	for _, res := range verifies {
		if res.Err != nil {
			t.Fatalf("admin swarm: transport error: %v", res.Err)
		}
		if strings.Contains(strings.ToLower(string(res.Body)), "deadlock") {
			t.Fatalf("admin swarm: deadlock detected: %s", string(res.Body))
		}
		if res.Status >= 500 {
			t.Fatalf("admin swarm: 5xx %d body=%s", res.Status, string(res.Body))
		}
	}
	if got := countStatus(verifies, http.StatusOK); got != 250 {
		t.Fatalf("admin swarm: got 200=%d want 250", got)
	}
	t.Logf("admin UTR swarm: 250 checkouts + 250 approvals, no deadlocks, p99=%s", s.metrics.snapshot().P99)
}

func TestRateLimiterBypass(t *testing.T) {
	s := getState(t)
	s.clearRateLimits(t)

	results := runConcurrent(500, func(int) callResult {
		return s.callWithIP(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": "rate_limit_probe",
			"password": "wrong-password",
		}, "198.51.100.77")
	})
	failOn5xx(t, "rate limiter", results)
	rateLimited := countStatus(results, http.StatusTooManyRequests)
	if rateLimited == 0 {
		t.Fatal("rate limiter: expected at least one 429")
	}
	if rateLimited+countStatus(results, http.StatusUnauthorized) != 500 {
		t.Fatalf("rate limiter: 429=%d 401=%d want total 500", rateLimited, countStatus(results, http.StatusUnauthorized))
	}
	hits := s.rateLimitHits(t, "POST /api/auth/login")
	if hits <= 0 {
		t.Fatal("rate limiter: expected reconciled Postgres hits")
	}
	t.Logf("rate limiter: 500 requests, %d blocked with 429, postgres_hits=%d, pool stable", rateLimited, hits)
}

func TestSecurityAndSQLValidation(t *testing.T) {
	s := getState(t)

	root := filepath.Join("..", "db", "queries")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		clean := stripSQLComments(string(raw))
		for _, bad := range []string{"||", "fmt.Sprintf", "EXECUTE IMMEDIATE", "quote_ident(", "format("} {
			if strings.Contains(clean, bad) {
				return fmt.Errorf("%s contains unsafe dynamic SQL token %q", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("static sql check: %v", err)
	}

	injection := s.call(http.MethodGet, "/api/events/"+url.PathEscape("' OR 1=1 --"), "", nil)
	assertStatus(t, "sql_injection_probe", injection, http.StatusBadRequest, http.StatusNotFound)

	missing := s.call(http.MethodGet, "/api/events/00000000-0000-0000-0000-000000000000", "", nil)
	assertStatus(t, "missing_event", missing, http.StatusNotFound)

	wrongRole := s.call(http.MethodGet, "/api/counter/menu", s.seed.ScannerToken, nil)
	assertStatus(t, "scanner_on_bar", wrongRole, http.StatusForbidden)

	wrongAdmin := s.call(http.MethodGet, "/api/admin/bookings?limit=1", s.seed.ScannerToken, nil)
	assertStatus(t, "scanner_on_admin", wrongAdmin, http.StatusForbidden)

	tampered := s.seed.GateQRToken
	if len(tampered) > 0 {
		last := tampered[len(tampered)-1]
		if last == 'A' {
			tampered = tampered[:len(tampered)-1] + "B"
		} else {
			tampered = tampered[:len(tampered)-1] + "A"
		}
	}
	badQR := s.call(http.MethodPost, "/api/gate/validate", s.seed.ScannerToken, map[string]string{"qr_token": tampered})
	assertStatus(t, "tampered_qr", badQR, http.StatusUnauthorized)

	health := s.call(http.MethodGet, "/api/health", "", nil)
	assertStatus(t, "security_headers", health, http.StatusOK)
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if health.Headers.Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
	t.Log("security: parameterized SQL, 404 mapping, RBAC, tampered QR and headers verified")
}

func stripSQLComments(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestAllAPIsSmokeAndLatency(t *testing.T) {
	s := getState(t)
	s.clearRateLimits(t)
	m := s.seed
	covered := map[string]bool{}
	mark := func(route string, res callResult, want ...int) {
		assertStatus(t, route, res, want...)
		covered[route] = true
	}

	mark("GET /api/health", s.call(http.MethodGet, "/api/health", "", nil), 200)
	mark("GET /api/events/{id}", s.call(http.MethodGet, "/api/events/"+m.EventID, "", nil), 200)
	mark("GET /api/tickets/{uuid}", s.call(http.MethodGet, "/api/tickets/"+m.PublicTicketID, "", nil), 200)
	mark("GET /api/tickets/{uuid}/image", s.call(http.MethodGet, "/api/tickets/"+m.PublicTicketID+"/image", "", nil), 200)

	checkoutPayload := makeCheckout(m, 900001, false)
	checkout := s.call(http.MethodPost, "/api/checkout/init", "", checkoutPayload, map[string]string{
		"Idempotency-Key": fmt.Sprintf("smoke-checkout-%d", time.Now().UnixNano()),
	})
	mark("POST /api/checkout/init", checkout, 201)
	bookingID := decode[checkoutResponse](t, checkout).BookingID
	mark("POST /api/checkout/{booking_id}/upi-reference", s.call(
		http.MethodPost, "/api/checkout/"+bookingID+"/upi-reference", "",
		map[string]string{
			"contact_phone":      checkoutPayload.ContactPhone,
			"upi_transaction_id": fmt.Sprintf("SMOKE%012d", time.Now().UnixNano()%1e12),
		},
	), 200)
	mark("POST /api/admin/bookings/{id}/verify", s.call(
		http.MethodPost, "/api/admin/bookings/"+bookingID+"/verify", m.AdminToken,
		map[string]string{"action": "APPROVE"},
	), 200)

	mark("POST /api/auth/login", s.call(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": "load_admin", "password": "AdminPass!123",
	}), 200)
	mark("POST /api/auth/refresh", s.call(http.MethodPost, "/api/auth/refresh", "", map[string]string{
		"refresh_token": m.AdminRefreshToken,
	}), 200)
	mark("POST /api/auth/logout", s.call(http.MethodPost, "/api/auth/logout", m.AdminToken, nil), 200)

	mark("POST /api/gate/validate", s.call(http.MethodPost, "/api/gate/validate", m.ScannerToken, map[string]string{
		"qr_token": m.GateQRToken,
	}), 200)
	mark("POST /api/gate/check-in", s.call(http.MethodPost, "/api/gate/check-in", m.ScannerToken, map[string]string{
		"qr_token": m.GateQRToken,
	}), 409)

	mark("GET /api/counter/menu", s.call(http.MethodGet, "/api/counter/menu", m.BartenderToken, nil), 200)
	createdItem := s.call(http.MethodPost, "/api/counter/menu", m.BartenderToken, map[string]any{
		"name": "Load Test Smoke Item", "category": "DRINK", "unit_price": 777,
	})
	mark("POST /api/counter/menu", createdItem, 201)
	itemID := decode[struct {
		ID string `json:"id"`
	}](t, createdItem).ID
	mark("POST /api/counter/redeem", s.call(http.MethodPost, "/api/counter/redeem", m.BartenderToken, map[string]any{
		"bar_code":                      m.GateBarCode,
		"items":                         []map[string]any{{"menu_item_id": itemID, "quantity": 1}},
		"idempotency_key":               fmt.Sprintf("load-smoke-%d", time.Now().UnixNano()),
		"require_full_wallet_deduction": true,
	}), 200)
	mark("GET /api/counter/search", s.call(http.MethodGet, "/api/counter/search?bar_code="+m.GateBarCode, m.BartenderToken, nil), 200)
	mark("GET /api/counter/tickets/{bar_code}/history", s.call(
		http.MethodGet, "/api/counter/tickets/"+m.GateBarCode+"/history?limit=5", m.BartenderToken, nil,
	), 200)
	mark("DELETE /api/counter/menu/{id}", s.call(http.MethodDelete, "/api/counter/menu/"+itemID, m.BartenderToken, nil), 200)

	createdStaff := s.call(http.MethodPost, "/api/admin/staff", m.AdminToken, map[string]string{
		"username": fmt.Sprintf("smoke_staff_%d", time.Now().Unix()%100000), "password": "SmokePass!123", "role": "SCANNER",
	})
	mark("POST /api/admin/staff", createdStaff, 201)
	staffID := decode[struct {
		ID string `json:"id"`
	}](t, createdStaff).ID
	mark("PATCH /api/admin/staff/{id}/password", s.call(
		http.MethodPatch, "/api/admin/staff/"+staffID+"/password", m.AdminToken,
		map[string]string{"password": "SmokePass!456"},
	), 200)
	mark("DELETE /api/admin/staff/{id}", s.call(http.MethodDelete, "/api/admin/staff/"+staffID, m.AdminToken, nil), 200)

	mark("GET /api/admin/bookings", s.call(
		http.MethodGet, "/api/admin/bookings?status=PENDING&limit=5", m.AdminToken, nil,
	), 200)
	createdEvent := s.call(http.MethodPost, "/api/admin/events", m.AdminToken, map[string]any{
		"title": "Load Smoke Event", "starts_at": time.Now().UTC().Add(time.Hour),
		"ends_at": time.Now().UTC().Add(4 * time.Hour), "location": "Smoke Hall", "status": "DRAFT",
	})
	mark("POST /api/admin/events", createdEvent, 201)
	eventID := decode[struct {
		ID string `json:"id"`
	}](t, createdEvent).ID
	mark("POST /api/admin/events/{id}/tiers", s.call(
		http.MethodPost, "/api/admin/events/"+eventID+"/tiers", m.AdminToken,
		map[string]any{"name": "SMOKE", "base_price": 1000, "fnb_credit": 500, "entry_count": 1},
	), 201)
	mark("GET /api/admin/stats/{event_id}", s.call(
		http.MethodGet, "/api/admin/stats/"+m.EventID, m.AdminToken, nil,
	), 200)

	if len(covered) != 25 {
		t.Fatalf("api coverage: got %d routes want 25; missing=%v", len(covered), missingRoutes(covered))
	}
	t.Logf("api sweep: all 25 routes exercised, p99=%s", s.metrics.snapshot().P99)
}

func missingRoutes(covered map[string]bool) []string {
	all := []string{
		"GET /api/health", "GET /api/events/{id}", "GET /api/tickets/{uuid}", "GET /api/tickets/{uuid}/image",
		"POST /api/checkout/init", "POST /api/checkout/{booking_id}/upi-reference",
		"POST /api/auth/login", "POST /api/auth/refresh", "POST /api/auth/logout",
		"POST /api/gate/validate", "POST /api/gate/check-in", "GET /api/counter/menu",
		"POST /api/counter/menu", "DELETE /api/counter/menu/{id}", "GET /api/counter/search",
		"GET /api/counter/tickets/{bar_code}/history", "POST /api/counter/redeem",
		"POST /api/admin/staff", "PATCH /api/admin/staff/{id}/password", "DELETE /api/admin/staff/{id}",
		"GET /api/admin/bookings", "POST /api/admin/bookings/{id}/verify", "POST /api/admin/events",
		"POST /api/admin/events/{id}/tiers", "GET /api/admin/stats/{event_id}",
	}
	var missing []string
	for _, route := range all {
		if !covered[route] {
			missing = append(missing, route)
		}
	}
	return missing
}

func TestLoadSummary(t *testing.T) {
	s := getState(t)
	snap := s.metrics.snapshot()
	t.Logf("LOAD SUMMARY requests=%d errors5xx=%d rps=%.1f p50=%s p95=%s p99=%s max=%s",
		snap.Count, snap.Errors, snap.RPS, snap.P50, snap.P95, snap.P99, snap.Max)
	if snap.Errors != 0 {
		t.Fatalf("load summary: %d 5xx responses", snap.Errors)
	}
}
