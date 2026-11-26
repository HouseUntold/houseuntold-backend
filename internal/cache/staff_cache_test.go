package cache

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/db"
)

func TestStaffRoleCacheSetGetInvalidate(t *testing.T) {
	c := NewStaffRoleCache(time.Hour)
	staff := db.StaffAccount{ID: uuid.New(), Username: "gate", Role: db.StaffRoleSCANNER, IsActive: true}

	if _, ok := c.Get(staff.ID); ok {
		t.Fatal("empty cache returned a hit")
	}
	c.Set(staff)
	got, ok := c.Get(staff.ID)
	if !ok || got.Role != db.StaffRoleSCANNER {
		t.Fatalf("cache hit failed: %+v %v", got, ok)
	}

	c.Invalidate(staff.ID)
	if _, ok := c.Get(staff.ID); ok {
		t.Fatal("invalidated entry still present")
	}
}

func TestStaffRoleCacheExpiryAndCleanup(t *testing.T) {
	c := NewStaffRoleCache(5 * time.Millisecond)
	staff := db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleBARTENDER}
	c.Set(staff)
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.Get(staff.ID); ok {
		t.Fatal("expired entry returned a hit")
	}

	c.Set(staff)
	ctx, cancel := context.WithCancel(context.Background())
	c.StartCleanup(ctx, time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	cancel()
	c.mu.RLock()
	remaining := len(c.entries)
	c.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("cleanup left %d expired entries", remaining)
	}
}

func TestNewStaffRoleCacheDefaults(t *testing.T) {
	c := NewStaffRoleCache(0)
	if c.ttl != 10*time.Second {
		t.Fatalf("unexpected default ttl: %s", c.ttl)
	}
}
