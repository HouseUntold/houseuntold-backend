package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewPostgresPoolRejectsInvalidDSN(t *testing.T) {
	pool, err := NewPostgresPool(context.Background(), PoolConfig{DSN: "not-a-dsn"})
	if pool != nil || err == nil || !strings.Contains(err.Error(), "parse_pg_dsn") {
		t.Fatalf("invalid DSN must be rejected, got pool=%v err=%v", pool, err)
	}
}

func TestNewPostgresPoolReportsPingFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := NewPostgresPool(ctx, PoolConfig{
		DSN:             "postgres://user:pass@127.0.0.1:1/venu?connect_timeout=1",
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: time.Minute,
		MaxConnIdleTime: time.Minute,
	})
	if pool != nil || err == nil || !strings.Contains(err.Error(), "ping_pg_pool") {
		t.Fatalf("unreachable DB must fail ping, got pool=%v err=%v", pool, err)
	}
}
