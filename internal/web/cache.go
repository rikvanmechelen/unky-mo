package web

import (
	"errors"
	"sync"
	"time"
)

// ttlCache memoizes the result of a possibly-expensive fetch per key,
// re-running fetch only after ttl has elapsed since the last fetch. Used to
// decouple browser poll frequency from rate-limited upstream calls (gh,
// Jira).
type ttlCache struct {
	ttl time.Duration
	now func() time.Time

	// maxEntries, when set, caps the entries kept: past it the oldest go
	// first (for caches whose entries are large, like annotated files).
	maxEntries int

	mu       sync.Mutex
	items    map[string]cacheEntry
	inflight map[string]*cacheFlight
}

// cacheFlight is a fetch in progress that later callers for the same key
// wait on instead of running their own.
type cacheFlight struct {
	done  chan struct{}
	value any
	err   error
}

type cacheEntry struct {
	value     any
	err       error
	fetchedAt time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, now: time.Now, items: map[string]cacheEntry{}, inflight: map[string]*cacheFlight{}}
}

// clear forgets every entry, e.g. after a fetch moved what they were
// computed from.
func (c *ttlCache) clear() {
	c.mu.Lock()
	c.items = map[string]cacheEntry{}
	c.mu.Unlock()
}

// get returns key's cached value, or fetches it. Concurrent misses for one
// key share a single fetch: several tabs polling the same checkout (or a
// slow analysis polled every few seconds) would otherwise each start their
// own git or analysis run.
func (c *ttlCache) get(key string, fetch func() (any, error)) (any, error) {
	c.mu.Lock()
	entry, ok := c.items[key]
	if ok && c.now().Sub(entry.fetchedAt) < c.ttl {
		c.mu.Unlock()
		return entry.value, entry.err
	}
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.value, f.err
	}
	f := &cacheFlight{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = map[string]*cacheFlight{}
	}
	c.inflight[key] = f
	c.mu.Unlock()

	finished := false
	defer func() {
		// A fetch that panicked still releases its waiters (with an error)
		// and its key, so the key isn't stuck waiting forever.
		if !finished {
			f.err = errFetchPanicked
		}
		c.mu.Lock()
		delete(c.inflight, key)
		if finished {
			c.store(key, f.value, f.err)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.value, f.err = fetch()
	finished = true
	return f.value, f.err
}

var errFetchPanicked = errors.New("the request failed")

// store keeps a fetched value. Must be called with c.mu held.
func (c *ttlCache) store(key string, value any, err error) {
	now := c.now()
	c.items[key] = cacheEntry{value: value, err: err, fetchedAt: now}
	if len(c.items) > cachePruneAt {
		for k, e := range c.items {
			if now.Sub(e.fetchedAt) >= c.ttl {
				delete(c.items, k)
			}
		}
	}
	for c.maxEntries > 0 && len(c.items) > c.maxEntries {
		oldest, at := "", now
		for k, e := range c.items {
			if !e.fetchedAt.After(at) {
				oldest, at = k, e.fetchedAt
			}
		}
		delete(c.items, oldest)
	}
}

// cachePruneAt is the size past which a store drops expired entries. Most
// caches have a key per checkout, but Git log selections have no bound;
// pruning keeps a cache to what was fetched within one TTL.
const cachePruneAt = 128
