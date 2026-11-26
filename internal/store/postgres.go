package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	DSN               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// NewPostgresPool sizes each API instance from the shared Postgres connection budget.
func NewPostgresPool(ctx context.Context, pc PoolConfig) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(pc.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse_pg_dsn: %w", err)
	}
	// MaxConns per instance = floor((postgres_max - reserved) / api_instances), capped for free-tier safety.
	// Example: (100 - 60) / 2 = 20, so two API instances never exceed 40 of 100 server connections.
	if pc.MaxConns > 0 {
		cfg.MaxConns = pc.MaxConns
	} else {
		cfg.MaxConns = 20
	}
	// Keep ~25% warm (5 of 20) so bursts avoid connect churn without holding idle connections too long.
	if pc.MinConns > 0 {
		cfg.MinConns = pc.MinConns
	} else {
		cfg.MinConns = 5
	}
	// One hour avoids LANL-style reconnects while still rotating sockets and clearing server-side state.
	if pc.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = pc.MaxConnLifetime
	} else {
		cfg.MaxConnLifetime = time.Hour
	}
	// Thirty minutes reclaims quiet connections faster than lifetime without thrashing the pool.
	if pc.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = pc.MaxConnIdleTime
	} else {
		cfg.MaxConnIdleTime = 30 * time.Minute
	}
	// One-minute health checks detect stale pooled connections early enough for production failover.
	if pc.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = pc.HealthCheckPeriod
	} else {
		cfg.HealthCheckPeriod = time.Minute
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect_pg_pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping_pg_pool: %w", err)
	}
	return pool, nil
}
