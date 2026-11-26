package cache

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

type staffCacheEntry struct {
	staff     db.StaffAccount
	expiresAt time.Time
}

// StaffRoleCache is a process-local, concurrency-safe TTL cache for verified staff accounts and roles.
// It is not shared between API replicas; use token version checks and database-backed revocation for cross-process invalidation.
// Never serialized to clients or third-party-readable cookies; invalidated immediately on password update or deactivation.
type StaffRoleCache struct {
	mu      sync.RWMutex
	entries map[uuid.UUID]staffCacheEntry
	ttl     time.Duration
}

func NewStaffRoleCache(ttl time.Duration) *StaffRoleCache {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &StaffRoleCache{
		entries: make(map[uuid.UUID]staffCacheEntry, 64),
		ttl:     ttl,
	}
}

func (c *StaffRoleCache) Get(id uuid.UUID) (db.StaffAccount, bool) {
	now := time.Now().UTC()
	c.mu.RLock()
	entry, ok := c.entries[id]
	c.mu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		return db.StaffAccount{}, false
	}
	return entry.staff, true
}

func (c *StaffRoleCache) Set(staff db.StaffAccount) {
	c.mu.Lock()
	c.entries[staff.ID] = staffCacheEntry{
		staff:     staff,
		expiresAt: time.Now().UTC().Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *StaffRoleCache) Invalidate(id uuid.UUID) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}

func (c *StaffRoleCache) StartCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().UTC()
				c.mu.Lock()
				for id, entry := range c.entries {
					if now.After(entry.expiresAt) {
						delete(c.entries, id)
					}
				}
				c.mu.Unlock()
			}
		}
	}()
}
