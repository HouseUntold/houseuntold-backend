package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/services"
)

type seedManifest struct {
	ScannerToken   string `json:"scanner_token"`
	BartenderToken string `json:"bartender_token"`
	EventID        string `json:"event_id"`
	FemaleCategory string `json:"female_category_id"`
	BeerItemID     string `json:"beer_item_id"`
}

type ticketRef struct {
	ID      uuid.UUID
	EventID uuid.UUID
	BarCode string
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
}

type tokenStore struct {
	mu        sync.RWMutex
	scanner   string
	bartender string
}

type metrics struct {
	mu        sync.Mutex
	name      string
	latencies []time.Duration
	statuses  map[int]int
	failures  int
}

type metricSnapshot struct {
	Name      string
	Requests  int
	RPS       float64
	P50       time.Duration
	P95       time.Duration
	P99       time.Duration
	Errors    int
	ErrorRate float64
	Statuses  map[int]int
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "API base URL")
	seedFile := flag.String("seed-file", ".loadtest/seed.json", "seed manifest path")
	duration := flag.Duration("duration", 10*time.Minute, "soak duration")
	gateWorkers := flag.Int("gate-workers", 5, "virtual gatekeepers")
	gateTotal := flag.Int("gate-total", 2000, "total gate check-in requests")
	barWorkers := flag.Int("bar-workers", 5, "virtual bartenders")
	barTotal := flag.Int("bar-total", 1000, "total bar redeem requests")
	checkoutUsers := flag.Int("checkout-users", 100, "virtual checkout users")
	checkoutTotal := flag.Int("checkout-total", 1000, "total checkout requests")
	cleanup := flag.Bool("cleanup", true, "reset seeded ticket state and delete soak bookings")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fatal("DATABASE_URL is required")
	}
	qrSecret := strings.TrimSpace(os.Getenv("QR_SIGNING_SECRET"))
	if qrSecret == "" {
		fatal("QR_SIGNING_SECRET is required")
	}

	seed, err := loadSeed(*seedFile)
	if err != nil {
		fatal("load seed: %v", err)
	}
	eventID, err := uuid.Parse(seed.EventID)
	if err != nil {
		fatal("parse event_id: %v", err)
	}
	femaleCategoryID, err := uuid.Parse(seed.FemaleCategory)
	if err != nil {
		fatal("parse female_category_id: %v", err)
	}
	beerItemID, err := uuid.Parse(seed.BeerItemID)
	if err != nil {
		fatal("parse beer_item_id: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fatal("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	gateTickets, err := loadTickets(db, "A0000", "A2499", *gateTotal)
	if err != nil {
		fatal("load gate tickets: %v", err)
	}
	barTickets, err := loadTickets(db, "A2000", "A2499", 200)
	if err != nil {
		fatal("load bar tickets: %v", err)
	}
	if len(barTickets) > 200 {
		barTickets = barTickets[:200]
	}
	if len(barTickets) == 0 {
		fatal("no active bar tickets in range A2000-A2499; run cmd/seed first")
	}

	if *cleanup {
		defer func() {
			if err := cleanupSoakData(db); err != nil {
				fmt.Fprintf(os.Stderr, "cleanup failed: %v\n", err)
			}
		}()
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          512,
			MaxIdleConnsPerHost:   128,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}

	tokens := &tokenStore{scanner: seed.ScannerToken, bartender: seed.BartenderToken}
	seedUser := func(key, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	creds := map[string]string{
		"scanner":   seedUser("SOAK_SCANNER_USER", "load_scanner") + "|" + seedUser("SOAK_SCANNER_PASS", "ScannerPass!123"),
		"bartender": seedUser("SOAK_BARTENDER_USER", "load_bartender") + "|" + seedUser("SOAK_BARTENDER_PASS", "BartenderPass!123"),
	}
	if err := tokens.refresh(ctx, client, *baseURL, creds); err != nil {
		fatal("initial staff login: %v", err)
	}
	refreshTokens := func(ctx context.Context) error {
		return tokens.refresh(ctx, client, *baseURL, creds)
	}

	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	signer := auth.NewQRSigner(qrSecret)
	now := time.Now().UTC()
	qrTokens := make([]string, len(gateTickets))
	for i, ticket := range gateTickets {
		qrTokens[i] = signer.SignToken(ticket.ID, ticket.EventID, now.Add(-time.Minute), now.Add(*duration+10*time.Minute))
	}

	gateMetrics := &metrics{name: "gate_check_in", statuses: map[int]int{}}
	barMetrics := &metrics{name: "bar_redeem", statuses: map[int]int{}}
	checkoutMetrics := &metrics{name: "checkout_init", statuses: map[int]int{}}

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		runWorkers(ctx, gateMetrics, *gateWorkers, *gateTotal, *duration, func(ctx context.Context, seq int) (int, time.Duration, error) {
			idx := seq % len(gateTickets)
			return doJSON(ctx, client, http.MethodPost, *baseURL+"/api/gate/check-in", tokens, "scanner", map[string]string{
				"qr_token": qrTokens[idx],
			}, "", refreshTokens)
		})
	}()
	go func() {
		defer wg.Done()
		runWorkers(ctx, barMetrics, *barWorkers, *barTotal, *duration, func(ctx context.Context, seq int) (int, time.Duration, error) {
			ticket := barTickets[seq%len(barTickets)]
			body := services.RedeemItemsRequest{
				BarCode: ticket.BarCode,
				Items: []services.RedeemLineItemInput{{
					MenuItemID: beerItemID,
					Quantity:   1,
				}},
				RequireFullWalletDeduction: true,
			}
			return doJSON(ctx, client, http.MethodPost, *baseURL+"/api/counter/redeem", tokens, "bartender", body, "soak-"+runID+"-"+strconv.Itoa(seq), refreshTokens)
		})
	}()
	go func() {
		defer wg.Done()
		runWorkers(ctx, checkoutMetrics, *checkoutUsers, *checkoutTotal, *duration, func(ctx context.Context, seq int) (int, time.Duration, error) {
			body := map[string]any{
				"event_id":           eventID.String(),
				"category_id":        femaleCategoryID.String(),
				"contact_phone":      "+919999999999",
				"contact_email":      fmt.Sprintf("soak-%s-%d@example.test", runID, seq),
				"upi_transaction_id": fmt.Sprintf("SOAK%s%04d", runID, seq),
				"guests": []map[string]any{{
					"name":               "Soak Guest",
					"age":                28,
					"gender":             "FEMALE",
					"is_primary_contact": true,
				}},
			}
			ip := fmt.Sprintf("198.51.100.%d", seq%254+1)
			idempotencyKey := fmt.Sprintf("soak-%s-checkout-%04d", runID, seq)
			return doJSON(ctx, client, http.MethodPost, *baseURL+"/api/checkout/init", tokens, "", body, ip, refreshTokens, map[string]string{
				"Idempotency-Key": idempotencyKey,
			})
		})
	}()
	wg.Wait()
	elapsed := time.Since(start)

	fmt.Printf("House Untold realistic soak test\n")
	fmt.Printf("duration=%s gatekeepers=%d bartenders=%d checkout_users=%d\n\n", elapsed.Round(time.Millisecond), *gateWorkers, *barWorkers, *checkoutUsers)
	for _, m := range []*metrics{gateMetrics, barMetrics, checkoutMetrics} {
		printSnapshot(m.snapshot(elapsed))
	}
}

func loadSeed(path string) (seedManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return seedManifest{}, err
	}
	var seed seedManifest
	if err := json.Unmarshal(raw, &seed); err != nil {
		return seedManifest{}, err
	}
	return seed, nil
}

func loadTickets(db *sql.DB, from, to string, needed int) ([]ticketRef, error) {
	rows, err := db.Query(`SELECT t.id, t.event_id, t.bar_code FROM tickets t JOIN events e ON e.id = t.event_id WHERE t.bar_code BETWEEN $1 AND $2 AND e.status = 'ON_SALE' AND e.ends_at > NOW() ORDER BY t.bar_code`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := []ticketRef{}
	for rows.Next() {
		var t ticketRef
		if err := rows.Scan(&t.ID, &t.EventID, &t.BarCode); err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if needed > 0 && len(tickets) < needed {
		return nil, fmt.Errorf("need %d tickets in %s-%s, found %d", needed, from, to, len(tickets))
	}
	return tickets, nil
}

func cleanupSoakData(db *sql.DB) error {
	queries := []string{
		`UPDATE tickets SET is_checked_in = false, checked_in_at = NULL WHERE bar_code BETWEEN 'A0000' AND 'A2499'`,
		`DELETE FROM transaction_line_items WHERE transaction_id IN (
			SELECT bt.id FROM bar_transactions bt JOIN tickets t ON t.id = bt.ticket_id WHERE t.bar_code BETWEEN 'A2000' AND 'A2499')`,
		`DELETE FROM bar_transactions WHERE ticket_id IN (SELECT id FROM tickets WHERE bar_code BETWEEN 'A2000' AND 'A2499')`,
		`UPDATE ticket_guests SET free_shot_redeemed = false WHERE ticket_id IN (SELECT id FROM tickets WHERE bar_code BETWEEN 'A2000' AND 'A2499')`,
		`UPDATE tickets SET food_balance = initial_food_balance, is_checked_in = false, checked_in_at = NULL WHERE bar_code BETWEEN 'A2000' AND 'A2499'`,
		`DELETE FROM bookings WHERE upi_transaction_id LIKE 'SOAK%'`,
		`DELETE FROM rate_limits`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (t *tokenStore) refresh(ctx context.Context, client *http.Client, baseURL string, creds map[string]string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for role, credential := range creds {
		parts := strings.SplitN(credential, "|", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid %s credentials", role)
		}
		token, err := login(ctx, client, baseURL, parts[0], parts[1])
		if err != nil {
			return fmt.Errorf("%s login: %w", role, err)
		}
		if role == "scanner" {
			t.scanner = token
		}
		if role == "bartender" {
			t.bartender = token
		}
	}
	return nil
}

func (t *tokenStore) get(role string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if role == "scanner" {
		return t.scanner
	}
	if role == "bartender" {
		return t.bartender
	}
	return ""
}

func login(ctx context.Context, client *http.Client, baseURL, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out loginResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("missing access_token")
	}
	return out.AccessToken, nil
}

func doJSON(ctx context.Context, client *http.Client, method, url string, tokens *tokenStore, role string, payload any, clientIP string, refresh func(context.Context) error, headers ...map[string]string) (int, time.Duration, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, 0, err
	}

	attempt := func(token string) (int, time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return 0, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "soak")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if clientIP != "" {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
		for _, header := range headers {
			for key, value := range header {
				req.Header.Set(key, value)
			}
		}
		start := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(start)
		if err != nil {
			return 0, elapsed, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, elapsed, nil
	}

	status, elapsed, err := attempt(tokens.get(role))
	if status != http.StatusUnauthorized || role == "" {
		return status, elapsed, err
	}
	if err := refresh(ctx); err != nil {
		return status, elapsed, err
	}
	return attempt(tokens.get(role))
}

func runWorkers(ctx context.Context, m *metrics, workers, total int, duration time.Duration, fn func(context.Context, int) (int, time.Duration, error)) {
	if workers <= 0 || total <= 0 {
		return
	}
	base := total / workers
	remainder := total % workers
	start := time.Now().Add(50 * time.Millisecond)
	gate := make(chan struct{})

	var wg sync.WaitGroup
	seqStart := 0
	for w := 0; w < workers; w++ {
		count := base
		if w < remainder {
			count++
		}
		if count == 0 {
			continue
		}
		seq := seqStart
		seqStart += count
		interval := time.Duration(float64(duration) / float64(count))
		wg.Add(1)
		go func(seq, count int, interval time.Duration) {
			defer wg.Done()
			<-gate
			for i := 0; i < count; i++ {
				at := start.Add(time.Duration(i) * interval)
				if wait := time.Until(at); wait > 0 {
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						if !timer.Stop() {
							<-timer.C
						}
						return
					case <-timer.C:
					}
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
				status, elapsed, err := fn(ctx, seq+i)
				m.record(status, elapsed, err)
			}
		}(seq, count, interval)
	}
	close(gate)
	wg.Wait()
}

func (m *metrics) record(status int, elapsed time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, elapsed)
	m.statuses[status]++
	if err != nil || status < 200 || status >= 300 {
		m.failures++
	}
}

func (m *metrics) snapshot(elapsed time.Duration) metricSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	sorted := append([]time.Duration(nil), m.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	requests := len(sorted)
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	errorRate := 0.0
	if requests > 0 {
		errorRate = float64(m.failures) / float64(requests) * 100
	}
	return metricSnapshot{
		Name:      m.name,
		Requests:  requests,
		RPS:       float64(requests) / seconds,
		P50:       percentile(sorted, 0.50),
		P95:       percentile(sorted, 0.95),
		P99:       percentile(sorted, 0.99),
		Errors:    m.failures,
		ErrorRate: errorRate,
		Statuses:  m.statuses,
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printSnapshot(s metricSnapshot) {
	statuses := make([]string, 0, len(s.Statuses))
	for code, count := range s.Statuses {
		statuses = append(statuses, fmt.Sprintf("%d:%d", code, count))
	}
	sort.Strings(statuses)
	fmt.Printf("%s requests=%d rps=%.2f p50=%s p95=%s p99=%s errors=%d error_rate=%.2f%% statuses=[%s]\n",
		s.Name, s.Requests, s.RPS, s.P50, s.P95, s.P99, s.Errors, s.ErrorRate, strings.Join(statuses, " "))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "soak: "+format+"\n", args...)
	os.Exit(1)
}
