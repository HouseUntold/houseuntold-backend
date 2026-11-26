package integration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/venu-live/venu-api/internal/migrations"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/store"
)

func TestGateCheckInAtomic_PostgresIntegration(t *testing.T) {
	tc.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("venu_test"),
		postgres.WithUsername("venu"),
		postgres.WithPassword("venu"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := migrations.Up(dsn, filepath.Join("..", "..", "db", "migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedIntegrationTickets(t, ctx, dsn)

	pool, err := store.NewPostgresPool(ctx, store.PoolConfig{
		DSN:               dsn,
		MaxConns:          5,
		MinConns:          1,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := repositories.NewTicketingRepository(pool, 10*time.Second)

	var successes, conflicts, otherErrors atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.CheckInTicketAtomic(ctx, "digest-1", uuid.Nil)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, pgx.ErrNoRows):
				conflicts.Add(1)
			default:
				otherErrors.Add(1)
			}
		}()
	}
	wg.Wait()

	if successes.Load() != 1 || conflicts.Load() != 1 || otherErrors.Load() != 0 {
		t.Fatalf("double scan = success:%d conflict:%d other:%d, want 1/1/0", successes.Load(), conflicts.Load(), otherErrors.Load())
	}

	var checked int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM tickets WHERE qr_token_digest = 'digest-1' AND is_checked_in = true`).Scan(&checked); err != nil {
		t.Fatalf("verify check-in: %v", err)
	}
	if checked != 1 {
		t.Fatalf("checked-in rows = %d, want 1", checked)
	}
}

func seedIntegrationTickets(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	eventID := uuid.New()
	categoryID := uuid.New()
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "event",
			query: `INSERT INTO events (id, title, starts_at, ends_at, location, status) VALUES ($1, 'Integration Event', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '5 hours', 'Test Venue', 'ON_SALE')`,
			args:  []any{eventID},
		},
		{
			name:  "category",
			query: `INSERT INTO ticket_categories (id, event_id, name, base_price, fnb_credit, entry_count, is_gender_strict) VALUES ($1, $2, 'STAG_FEMALE', 1000, 500, 1, false)`,
			args:  []any{categoryID, eventID},
		},
		{
			name: "bookings",
			query: `INSERT INTO bookings (id, event_id, category_id, contact_phone, contact_email, total_amount, upi_transaction_id, status)
				SELECT gen_random_uuid(), $1, $2, '+919999999999', 'integration@example.test', 1000, 'INTEGRATION-' || n, 'CONFIRMED'
				FROM generate_series(1, 5) n`,
			args: []any{eventID, categoryID},
		},
		{
			name: "tickets",
			query: `WITH ranked AS (
					SELECT id, ROW_NUMBER() OVER (ORDER BY created_at) AS rn FROM bookings WHERE event_id = $1
				)
				INSERT INTO tickets (id, booking_id, event_id, qr_token_digest, bar_code, food_balance, initial_food_balance)
				SELECT gen_random_uuid(), id, $1, 'digest-' || rn, 'A' || LPAD(rn::text, 4, '0'), 500, 500 FROM ranked`,
			args: []any{eventID},
		},
		{
			name:  "guests",
			query: `INSERT INTO ticket_guests (ticket_id, name, age, gender, is_primary_contact) SELECT id, 'Guest ' || bar_code, 28, 'FEMALE', true FROM tickets WHERE event_id = $1`,
			args:  []any{eventID},
		},
	}

	pool, err := store.NewPostgresPool(ctx, store.PoolConfig{DSN: dsn, MaxConns: 2, MinConns: 1})
	if err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	defer pool.Close()
	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed %s: %v", stmt.name, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM tickets WHERE event_id = $1`, eventID).Scan(&count); err != nil {
		t.Fatalf("count seeded tickets: %v", err)
	}
	if count != 5 {
		t.Fatal(fmt.Sprintf("seeded tickets = %d, want 5", count))
	}
}
