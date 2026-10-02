package web

import (
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
