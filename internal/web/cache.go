package web

import (
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

	mu    sync.Mutex
	items map[string]cacheEntry
}

type cacheEntry struct {
	value     any
	err       error
	fetchedAt time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, now: time.Now, items: map[string]cacheEntry{}}
}

// clear forgets every entry, e.g. after a fetch moved what they were
// computed from.
func (c *ttlCache) clear() {
	c.mu.Lock()
	c.items = map[string]cacheEntry{}
	c.mu.Unlock()
}

func (c *ttlCache) get(key string, fetch func() (any, error)) (any, error) {
	c.mu.Lock()
	entry, ok := c.items[key]
	fresh := ok && c.now().Sub(entry.fetchedAt) < c.ttl
	c.mu.Unlock()
	if fresh {
		return entry.value, entry.err
	}

	value, err := fetch()

	c.mu.Lock()
	now := c.now()
	c.items[key] = cacheEntry{value: value, err: err, fetchedAt: now}
	if len(c.items) > cachePruneAt {
		for k, e := range c.items {
			if now.Sub(e.fetchedAt) >= c.ttl {
				delete(c.items, k)
			}
		}
	}
	c.mu.Unlock()
	return value, err
}

// cachePruneAt is the size past which a store drops expired entries. Most
// caches have a key per checkout, but Git log selections have no bound;
// pruning keeps a cache to what was fetched within one TTL.
const cachePruneAt = 128
