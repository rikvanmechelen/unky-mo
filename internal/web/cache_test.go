package web

import (
	"fmt"
	"sync"
	"sync/atomic"
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

// Concurrent misses for one key run one fetch and all get its value.
func TestTTLCacheSingleFlight(t *testing.T) {
	c := newTTLCache(time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.get("k", func() (any, error) {
				calls.Add(1)
				<-release
				return "v", nil
			})
			if v != "v" || err != nil {
				t.Errorf("got %v %v", v, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("%d fetches, want 1", n)
	}
}

// A fetch that panics doesn't leave its key stuck: the next get fetches.
func TestTTLCachePanicReleasesKey(t *testing.T) {
	c := newTTLCache(time.Minute)
	func() {
		defer func() { _ = recover() }()
		c.get("k", func() (any, error) { panic("boom") })
	}()
	done := make(chan any)
	go func() {
		v, _ := c.get("k", func() (any, error) { return "again", nil })
		done <- v
	}()
	select {
	case v := <-done:
		if v != "again" {
			t.Errorf("got %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("the key stayed in flight")
	}
}

// maxEntries keeps the newest entries.
func TestTTLCacheMaxEntries(t *testing.T) {
	c := newTTLCache(time.Hour)
	c.maxEntries = 2
	clock := time.Unix(0, 0)
	c.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for _, k := range []string{"a", "b", "c"} {
		c.get(k, func() (any, error) { return k, nil })
	}
	if _, ok := c.items["a"]; ok || len(c.items) != 2 {
		t.Errorf("items %v", c.items)
	}
}
