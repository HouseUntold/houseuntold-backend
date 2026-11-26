package repositories

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/venu-live/venu-api/internal/db"
)

// TxFunc executes a transactional block against a sqlc Querier bounded by the repository DB timeout.
type TxFunc func(ctx context.Context, fn func(txCtx context.Context, q db.Querier) error) error

type PoolStats struct {
	AcquiredConns int32 `json:"acquired_conns"`
	IdleConns     int32 `json:"idle_conns"`
	TotalConns    int32 `json:"total_conns"`
	MaxConns      int32 `json:"max_conns"`
}

// TicketingRepository wraps the pgxpool and sqlc Queries with mandatory per-operation timeouts.
type TicketingRepository struct {
	pool    *pgxpool.Pool
	queries db.Querier
	timeout time.Duration
	pingFn  func(context.Context) error
	txFn    TxFunc
}

func NewTicketingRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *TicketingRepository {
	if queryTimeout <= 0 {
		queryTimeout = 3 * time.Second
	}
	return &TicketingRepository{
		pool:    pool,
		queries: db.New(pool),
		timeout: queryTimeout,
	}
}

// NewTicketingRepositoryWithQuerier builds a repository backed by a custom db.Querier (e.g., API test suite).
func NewTicketingRepositoryWithQuerier(
	q db.Querier,
	pingFn func(context.Context) error,
	txFn TxFunc,
	queryTimeout time.Duration,
) *TicketingRepository {
	if queryTimeout <= 0 {
		queryTimeout = 3 * time.Second
	}
	return &TicketingRepository{
		queries: q,
		timeout: queryTimeout,
		pingFn:  pingFn,
		txFn:    txFn,
	}
}

func (r *TicketingRepository) PoolStats() PoolStats {
	if r.pool == nil {
		return PoolStats{}
	}
	stat := r.pool.Stat()
	return PoolStats{
		AcquiredConns: stat.AcquiredConns(),
		IdleConns:     stat.IdleConns(),
		TotalConns:    stat.TotalConns(),
		MaxConns:      stat.MaxConns(),
	}
}

func (r *TicketingRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.timeout)
}

func (r *TicketingRepository) Ping(ctx context.Context) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if r.pingFn != nil {
		if err := r.pingFn(tctx); err != nil {
			return fmt.Errorf("repo_ping: %w", err)
		}
		return nil
	}
	if err := r.pool.Ping(tctx); err != nil {
		return fmt.Errorf("repo_ping: %w", err)
	}
	return nil
}

func (r *TicketingRepository) Read(ctx context.Context, fn func(context.Context, db.Querier) error) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	return fn(tctx, r.queries)
}

// WithTx executes fn inside a PostgreSQL transaction bounded by the repository DB timeout.
func (r *TicketingRepository) WithTx(ctx context.Context, fn func(txCtx context.Context, q db.Querier) error) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	if r.txFn != nil {
		return r.txFn(tctx, fn)
	}

	tx, err := r.pool.BeginTx(tctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("repo_begin_tx: %w", err)
	}

	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	qtx := db.New(tx)
	if err := fn(tctx, qtx); err != nil {
		return err
	}

	if err := tx.Commit(tctx); err != nil {
		return fmt.Errorf("repo_commit_tx: %w", err)
	}
	return nil
}
