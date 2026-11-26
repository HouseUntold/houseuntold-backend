package test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/services"
	"github.com/venu-live/venu-api/internal/whatsapp"
)

type pgNotifier struct {
	mu    sync.Mutex
	count int
}

func (n *pgNotifier) EnqueueTicketConfirmation(whatsapp.TicketPassMessage) {
	n.mu.Lock()
	n.count++
	n.mu.Unlock()
}

func (n *pgNotifier) CloseAndWait(context.Context) error { return nil }

func (n *pgNotifier) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

func realPostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("VENU_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set VENU_TEST_DATABASE_URL to run real PostgreSQL smoke/load tests")
	}
	return dsn
}

func seedPostgresSchema(t *testing.T, dsn string) (string, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := "venu_smoke_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse admin DSN: %v", err)
	}
	adminCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("connect admin postgres: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := admin.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	migrations, err := filepath.Glob("../db/migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(migrations)
	for _, path := range migrations {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		upSQL := string(migration)
		if idx := strings.Index(upSQL, "-- +goose Down"); idx >= 0 {
			upSQL = upSQL[:idx]
		}
		if _, err := admin.Exec(ctx, upSQL); err != nil {
			t.Fatalf("apply migration %s: %v", path, err)
		}
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse pool DSN: %v", err)
	}
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema
	poolCfg.MaxConns = 20
	poolCfg.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("cleanup schema %s: %v", schema, err)
		}
		_ = admin.Close(context.Background())
	})
	return schema, pool
}

func seedEventAndCategories(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID, femaleCatID, coupleCatID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()

	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, title, starts_at, ends_at, location, status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		eventID, "House Untold Postgres Smoke", now.Add(time.Hour), now.Add(24*time.Hour), "Bengaluru", string(db.EventStatusONSALE),
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_categories (id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict)
		VALUES ($1, $2, 'STAG_FEMALE', 'A', 999, 999, 1, false)`, femaleCatID, eventID); err != nil {
		t.Fatalf("seed female category: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_categories (id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict)
		VALUES ($1, $2, 'COUPLE', 'A', 2000, 1000, 2, true)`, coupleCatID, eventID); err != nil {
		t.Fatalf("seed couple category: %v", err)
	}
	return eventID, femaleCatID, coupleCatID
}

func TestRealPostgresCheckoutFirst99AndSingleUseGateQR(t *testing.T) {
	dsn := realPostgresDSN(t)
	_, pool := seedPostgresSchema(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	eventID, femaleCatID, coupleCatID := seedEventAndCategories(t, ctx, pool)
	cfg := &config.Config{
		AppBaseURL:         "https://venu.test",
		First99FemaleLimit: 99,
		TicketQRTTL:        time.Hour,
		UPIVPA:             "venu@upi",
		UPIMerchantName:    "House Untold",
	}
	signer := auth.NewQRSigner("test-qr-secret-32-bytes-minimum!")
	tokens := auth.NewTokenManager("access-secret", "refresh-secret", "venu", "venu-api", time.Minute, time.Hour)
	repo := repositories.NewTicketingRepository(pool, 15*time.Second)
	notifier := &pgNotifier{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	checkout := services.NewTicketingService(cfg, repo, tokens, signer, nil, notifier, logger)
	gate := services.NewGateService(repo, signer, logger)

	const buyers = 120
	type outcome struct {
		resp  services.InitCheckoutResponse
		err   error
		phone string
	}
	outcomes := make([]outcome, buyers)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 20)
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outcomes[i].phone = fmt.Sprintf("+9198765%05d", i)
			outcomes[i].resp, outcomes[i].err = checkout.InitGuestCheckout(ctx, services.InitCheckoutRequest{
				EventID:      eventID,
				CategoryID:   femaleCatID,
				ContactPhone: outcomes[i].phone,
				ContactEmail: fmt.Sprintf("female%d@example.com", i),
				Guests: []services.GuestInput{{
					Name:             fmt.Sprintf("Guest %d", i),
					Age:              25,
					Gender:           db.GenderTypeFEMALE,
					IsPrimaryContact: true,
				}},
			}, fmt.Sprintf("smoke-female-%d", i))
		}(i)
	}
	wg.Wait()

	var confirmedQR string
	for i, out := range outcomes {
		if out.err != nil {
			t.Fatalf("checkout %d failed: %v", i, out.err)
		}
		if out.resp.First99Applied && confirmedQR == "" {
			confirmedQR = out.resp.GateQRToken
		}
	}
	if confirmedQR == "" {
		t.Fatal("no first-99 confirmed ticket generated a gate QR")
	}

	var confirmedFree, pendingPaid int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE event_id=$1 AND status='CONFIRMED' AND total_amount=1`, eventID).Scan(&confirmedFree); err != nil {
		t.Fatalf("count confirmed first-99 bookings: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE event_id=$1 AND status='PENDING' AND total_amount=999`, eventID).Scan(&pendingPaid); err != nil {
		t.Fatalf("count pending paid bookings: %v", err)
	}
	if confirmedFree != 99 || pendingPaid != buyers-99 {
		rows, _ := pool.Query(ctx, `SELECT status, total_amount, COUNT(*) FROM bookings WHERE event_id=$1 GROUP BY status, total_amount ORDER BY status, total_amount`, eventID)
		var breakdown []string
		for rows.Next() {
			var status string
			var amount, count int64
			if err := rows.Scan(&status, &amount, &count); err == nil {
				breakdown = append(breakdown, fmt.Sprintf("%s/%d=%d", status, amount, count))
			}
		}
		rows.Close()
		t.Fatalf("first-99 race failed: confirmed_free=%d pending_paid=%d breakdown=%v", confirmedFree, pendingPaid, breakdown)
	}
	if got := notifier.Count(); got != int(confirmedFree) {
		t.Fatalf("notified %d guests, want %d", got, confirmedFree)
	}

	const scans = 20
	scanResults := make([]error, scans)
	wg = sync.WaitGroup{}
	for i := 0; i < scans; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, scanResults[i] = gate.CheckInTicket(ctx, confirmedQR, uuid.Nil)
		}(i)
	}
	wg.Wait()

	var successes int
	for _, err := range scanResults {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("single-use gate QR accepted %d concurrent scans, want 1", successes)
	}

	var pendingID uuid.UUID
	var pendingPhone string
	if err := pool.QueryRow(ctx, `SELECT id, contact_phone FROM bookings WHERE event_id=$1 AND status='PENDING' LIMIT 1`, eventID).Scan(&pendingID, &pendingPhone); err != nil {
		t.Fatalf("find pending booking: %v", err)
	}
	coupleResp, err := checkout.InitGuestCheckout(ctx, services.InitCheckoutRequest{
		EventID:      eventID,
		CategoryID:   coupleCatID,
		ContactPhone: "+919912345678",
		ContactEmail: "couple@example.com",
		Guests: []services.GuestInput{
			{Name: "Male Guest", Age: 28, Gender: db.GenderTypeMALE, IsPrimaryContact: true},
			{Name: "Female Guest", Age: 27, Gender: db.GenderTypeFEMALE},
		},
	}, "smoke-couple-booking")
	if err != nil {
		t.Fatalf("create couple booking: %v", err)
	}
	utr := "984532176540"
	if _, err := checkout.SubmitUPITransactionID(ctx, pendingID, pendingPhone, utr); err != nil {
		t.Fatalf("submit first UTR: %v", err)
	}
	if _, err := checkout.SubmitUPITransactionID(ctx, pendingID, pendingPhone, utr); err != nil {
		t.Fatalf("resubmitting the same UTR should be idempotent: %v", err)
	}
	if _, err := checkout.SubmitUPITransactionID(ctx, coupleResp.BookingID, "+919912345678", utr); err == nil {
		t.Fatal("duplicate UPI transaction ID was accepted")
	}
}
