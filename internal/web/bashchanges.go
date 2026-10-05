package web

import (
	"context"
	"sync"
	"time"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
)

// snapshotChanges is the production BashChanges over a bashsnap.Store. A
// finished record never changes, so each one's file list is computed once
// and kept (bounded).
type snapshotChanges struct {
	store *bashsnap.Store

	mu        sync.Mutex
	stats     map[string][]bashsnap.FileStat // session \x00 tool use id
	lastSweep time.Time
}

const maxBashStats = 4096

// NewBashChanges reads the snapshots `mo snapshot` writes.
func NewBashChanges(store *bashsnap.Store) BashChanges {
	return &snapshotChanges{store: store, stats: map[string][]bashsnap.FileStat{}}
}

func (c *snapshotChanges) List(ctx context.Context, sessionID string) (map[string]bashsnap.Change, error) {
	c.sweep()
	recs, err := c.store.Records(sessionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bashsnap.Change, len(recs))
	for id, rec := range recs {
		key := sessionID + "\x00" + id
		c.mu.Lock()
		files, ok := c.stats[key]
		c.mu.Unlock()
		if !ok {
			if files, err = c.store.Stat(ctx, rec); err != nil {
				continue // the checkout is gone, or its objects were swept
			}
			c.mu.Lock()
			if len(c.stats) >= maxBashStats {
				clear(c.stats)
			}
			c.stats[key] = files
			c.mu.Unlock()
		}
		if len(files) == 0 {
			continue // only mode or ignored-file changes
		}
		out[id] = bashsnap.Change{Root: rec.Root, Start: rec.Start, End: rec.End, Files: files}
	}
	return out, nil
}

func (c *snapshotChanges) Diff(ctx context.Context, sessionID, toolUseID string) (string, *bashsnap.Diff, error) {
	rec, err := c.store.Record(sessionID, toolUseID)
	if err != nil {
		return "", nil, err
	}
	d, err := c.store.Diff(ctx, rec)
	return rec.Root, d, err
}

// sweep drops old snapshots at most once an hour (the TUI also sweeps on
// start).
func (c *snapshotChanges) sweep() {
	c.mu.Lock()
	due := time.Since(c.lastSweep) > time.Hour
	if due {
		c.lastSweep = time.Now()
	}
	c.mu.Unlock()
	if due {
		c.store.Sweep()
	}
}
