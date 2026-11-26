package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/venu-live/venu-api/internal/db"
)

type repoQuerier struct {
	db.Querier
	pingErr error
}

func (q repoQuerier) Ping(context.Context) error { return q.pingErr }

func TestNewTicketingRepositoryDefaults(t *testing.T) {
	r := NewTicketingRepository(nil, 0)
	if r.timeout != 3*time.Second || r.queries == nil {
		t.Fatalf("unexpected repository defaults: %+v", r)
	}
}

func TestRepositoryPing(t *testing.T) {
	r := NewTicketingRepositoryWithQuerier(repoQuerier{}, func(context.Context) error { return nil }, nil, time.Second)
	if err := r.Ping(context.Background()); err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	want := errors.New("db down")
	r = NewTicketingRepositoryWithQuerier(repoQuerier{}, func(context.Context) error { return want }, nil, time.Second)
	if err := r.Ping(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected wrapped ping error, got %v", err)
	}
}

func TestRepositoryWithTxCommitAndRollback(t *testing.T) {
	committed := false
	txFn := func(ctx context.Context, fn func(context.Context, db.Querier) error) error {
		if err := fn(ctx, repoQuerier{}); err != nil {
			return err
		}
		committed = true
		return nil
	}
	r := NewTicketingRepositoryWithQuerier(repoQuerier{}, nil, txFn, time.Second)

	if err := r.WithTx(context.Background(), func(context.Context, db.Querier) error { return nil }); err != nil {
		t.Fatalf("tx failed: %v", err)
	}
	if !committed {
		t.Fatal("transaction did not commit")
	}

	sentinel := errors.New("reject")
	if err := r.WithTx(context.Background(), func(context.Context, db.Querier) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("expected transaction error to propagate, got %v", err)
	}
}

func TestRepositoryTimeoutIsApplied(t *testing.T) {
	var sawDeadline bool
	txFn := func(ctx context.Context, fn func(context.Context, db.Querier) error) error {
		_, sawDeadline = ctx.Deadline()
		return fn(ctx, repoQuerier{})
	}
	r := NewTicketingRepositoryWithQuerier(repoQuerier{}, nil, txFn, time.Millisecond)
	_ = r.WithTx(context.Background(), func(ctx context.Context, _ db.Querier) error {
		_, ok := ctx.Deadline()
		if !ok {
			t.Fatal("transaction context has no deadline")
		}
		return nil
	})
	if !sawDeadline {
		t.Fatal("repository did not apply query timeout")
	}
}
