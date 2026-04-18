// Package usage reads Claude Code's OAuth usage endpoint and caches the
// result on disk so the mo TUI can surface rate-limit-window utilization
// without hammering the endpoint (it returns 429 under concurrent load).
package usage

import (
	"context"
	"errors"
	"time"
)

// ErrAuthExpired is returned when the OAuth token in ~/.claude/.credentials.json
// is rejected by the API. The caller should surface a "run claude" hint.
var ErrAuthExpired = errors.New("usage: claude oauth token expired or invalid")

// Snapshot is the materialized view consumed by the TUI.
type Snapshot struct {
	FiveHour       Window
	SevenDay       Window
	SevenDayOpus   Window
	SevenDaySonnet Window

	FetchedAt time.Time
	Source    string // "api", "cache", "stale"
}

// Window is one rate-limit window's state.
type Window struct {
	Utilization float64
	ResetsAt    time.Time
}

// HasData reports whether this window was populated by the API.
func (w Window) HasData() bool {
	return !w.ResetsAt.IsZero() || w.Utilization > 0
}

// Fetch returns a fresh or cached snapshot.
//
// Decision tree (see cache.go for the implementation):
//  1. Load the cache. If we're inside a 429 cooldown, return cached (stale).
//  2. If the cache is still fresh (< cacheTTL) AND no window has reset since
//     the last fetch, return cached.
//  3. Otherwise call the API:
//     - 200: write cache, return fresh.
//     - 429: set cooldown, return last cached (stale). No error.
//     - 401: return ErrAuthExpired, do not write cache.
//     - other error: return last cached (stale) if any, else the error.
func Fetch(ctx context.Context) (Snapshot, error) {
	return fetchWithDeps(ctx, cacheOnDisk{}, realClient{})
}

// clamp is used by both the main TUI strip and sidebar line; keep it here
// so neither caller duplicates it.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
