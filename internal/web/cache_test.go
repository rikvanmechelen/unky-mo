package web

import (
	"fmt"
	"testing"
	"time"
)

func TestTTLCacheReusesWithinTTL(t *testing.T) {
	calls := 0
	c := newTTLCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }

	fetch := func() (any, error) { calls++; return calls, nil }

	v1, _ := c.get("k", fetch)
	v2, _ := c.get("k", fetch)

	if calls != 1 {
		t.Fatalf("want 1 fetch within TTL, got %d", calls)
	}
	if v1 != v2 {
		t.Fatalf("want same cached value, got %v and %v", v1, v2)
	}
}

func TestTTLCacheRefetchesAfterTTL(t *testing.T) {
	calls := 0
	c := newTTLCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }

	fetch := func() (any, error) { calls++; return calls, nil }

	c.get("k", fetch)
	now = now.Add(2 * time.Minute)
	c.get("k", fetch)

	if calls != 2 {
		t.Fatalf("want 2 fetches after TTL elapsed, got %d", calls)
	}
}

func TestTTLCacheKeysAreIndependent(t *testing.T) {
	calls := 0
	c := newTTLCache(time.Minute)
	fetch := func() (any, error) { calls++; return calls, nil }

	c.get("a", fetch)
	c.get("b", fetch)

	if calls != 2 {
		t.Fatalf("want distinct keys to fetch independently, got %d calls", calls)
	}
}

// Past cachePruneAt keys, a store drops the expired entries and keeps the
// fresh ones.
func TestTTLCachePrunesExpired(t *testing.T) {
	c := newTTLCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }
	fetch := func() (any, error) { return 1, nil }

	for i := 0; i < cachePruneAt-1; i++ {
		c.get(fmt.Sprintf("old%d", i), fetch)
	}
	now = now.Add(2 * time.Minute)
	c.get("fresh", fetch)
	if len(c.items) != cachePruneAt {
		t.Fatalf("at the limit nothing is pruned yet: %d items", len(c.items))
	}
	c.get("fresh2", fetch)
	if len(c.items) != 2 {
		t.Fatalf("want only the 2 fresh entries left, got %d", len(c.items))
	}
}
