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
	c.items[key] = cacheEntry{value: value, err: err, fetchedAt: c.now()}
	c.mu.Unlock()
	return value, err
}
