package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"
)

const (
	cacheTTL       = 60 * time.Second
	cooldownOn429  = 5 * time.Minute
	cachePath      = "/tmp/mo-claude-usage.json"
	cacheTmpSuffix = ".tmp"
)

// cached is the on-disk representation at /tmp/mo-claude-usage.json.
type cached struct {
	// Body is the last successful API response. On 429 it remains unchanged
	// and CooldownUntil is bumped; on 401 we don't touch the cache.
	Body          apiResponse `json:"body"`
	FetchedAt     time.Time   `json:"fetched_at"`
	CooldownUntil time.Time   `json:"cooldown_until,omitempty"`
}

type cacheStore interface {
	load() (cached, bool, error)
	save(c cached) error
}

type cacheOnDisk struct{}

func (cacheOnDisk) load() (cached, bool, error) {
	b, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cached{}, false, nil
		}
		return cached{}, false, err
	}
	var c cached
	if err := json.Unmarshal(b, &c); err != nil {
		return cached{}, false, err
	}
	return c, true, nil
}

func (cacheOnDisk) save(c cached) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := cachePath + cacheTmpSuffix
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath)
}

// tokenReader returns an OAuth access token. The real implementation reads
// ~/.claude/.credentials.json; tests inject a stub.
type tokenReader func() (string, error)

// fetchWithDeps implements Fetch's decision tree against pluggable deps.
// The real entry point (Fetch) wires in on-disk cache + real HTTP client;
// tests wire in fakes.
func fetchWithDeps(ctx context.Context, cs cacheStore, c apiClient, opts ...tokenReader) (Snapshot, error) {
	now := time.Now()

	readToken := readAccessToken
	if len(opts) > 0 && opts[0] != nil {
		readToken = opts[0]
	}

	prev, hasPrev, _ := cs.load() // read errors fall through to a refetch

	// Cooldown (set after a 429). Keep serving last-known-good until it lifts.
	if hasPrev && now.Before(prev.CooldownUntil) {
		return toSnapshot(prev.Body, prev.FetchedAt, "stale"), nil
	}

	// Fresh cache: respect TTL unless a window has reset since fetch.
	if hasPrev && now.Sub(prev.FetchedAt) < cacheTTL && !anyWindowResetSince(prev.Body, prev.FetchedAt, now) {
		return toSnapshot(prev.Body, prev.FetchedAt, "cache"), nil
	}

	token, err := readToken()
	if err != nil {
		if hasPrev {
			return toSnapshot(prev.Body, prev.FetchedAt, "stale"), nil
		}
		return Snapshot{}, err
	}

	res := c.call(ctx, token)

	// 401 → auth expired. Do not touch cache.
	if errors.Is(res.err, ErrAuthExpired) || res.code == http.StatusUnauthorized {
		return Snapshot{}, ErrAuthExpired
	}

	// 429 → record cooldown, serve last-known-good.
	if res.code == http.StatusTooManyRequests {
		if hasPrev {
			prev.CooldownUntil = now.Add(cooldownOn429)
			_ = cs.save(prev)
			return toSnapshot(prev.Body, prev.FetchedAt, "stale"), nil
		}
		// No previous cache — record an empty cooldown so we don't hammer.
		empty := cached{FetchedAt: now, CooldownUntil: now.Add(cooldownOn429)}
		_ = cs.save(empty)
		return Snapshot{}, errors.New("usage: 429 and no cached snapshot available yet")
	}

	// Network or other errors → serve stale if we have it.
	if res.err != nil {
		if hasPrev {
			return toSnapshot(prev.Body, prev.FetchedAt, "stale"), nil
		}
		return Snapshot{}, res.err
	}

	// Success. Write cache, return fresh snapshot.
	next := cached{Body: res.body, FetchedAt: now}
	_ = cs.save(next)
	return toSnapshot(res.body, now, "api"), nil
}

// anyWindowResetSince returns true if at least one window's ResetsAt fell
// between `since` (last fetch) and `now`. When that happens the cached
// utilization is wrong even if we're within cacheTTL.
func anyWindowResetSince(r apiResponse, since, now time.Time) bool {
	windows := [...]time.Time{r.FiveHour.ResetsAt, r.SevenDay.ResetsAt, r.SevenDayOpus.ResetsAt, r.SevenDaySonnet.ResetsAt}
	for _, t := range windows {
		if t.IsZero() {
			continue
		}
		if t.After(since) && !t.After(now) {
			return true
		}
	}
	return false
}
