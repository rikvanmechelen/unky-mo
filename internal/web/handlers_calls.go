package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
)

// callsResponse is the Overview's function-level view. Repo is false
// outside a git checkout.
type callsResponse struct {
	Repo bool `json:"repo"`
	*review.CallGraph
}

// handleCalls serves the call graph of a live session's change, for the
// same ?base= modes as handleOverview.
func (s *Server) handleCalls(w http.ResponseWriter, r *http.Request) {
	windowID, mode := r.PathValue("windowID"), r.URL.Query().Get("base")
	if mode == "" {
		mode = gitfiles.ModeBranch
	}
	if mode != gitfiles.ModeBranch && mode != gitfiles.ModeHead {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown base %q", mode))
		return
	}
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	s.serveCalls(w, r, dir+"\x00"+mode, func() (*gitfiles.Overview, error) { return s.overview(dir, mode) })
}

// handleBranchCalls is handleCalls for the reviewer view's targets.
func (s *Server) handleBranchCalls(w http.ResponseWriter, r *http.Request) {
	mode, ok := branchMode(w, r)
	if !ok {
		return
	}
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	key := t.dir + "\x00" + mode
	if t.dir == "" {
		key = t.refKey()
	}
	s.serveCalls(w, r, key, func() (*gitfiles.Overview, error) { return s.targetOverview(t, mode) })
}

// serveCalls builds the call graph of the overview get returns. It's cached
// under key for as long as the change's files look the same, since one can
// take seconds and the browser polls every few.
func (s *Server) serveCalls(w http.ResponseWriter, r *http.Request, key string, get func() (*gitfiles.Overview, error)) {
	o, err := get()
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, callsResponse{Repo: false})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	body, err := s.callCache.get(key, changeFingerprint(o), func() ([]byte, error) {
		cg, err := s.deps.Calls.Calls(o)
		if err != nil {
			return nil, err
		}
		return json.Marshal(callsResponse{Repo: true, CallGraph: cg})
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeHashed(w, r, body)
}

// changeFingerprint changes whenever the change's code may have: a
// different file list or base, or (for a checkout) a changed file's size
// or modification time. A commit target (Head set) is immutable.
func changeFingerprint(o *gitfiles.Overview) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", o.Root, o.Mode, o.Rev, o.Head)
	for _, f := range o.Files {
		fmt.Fprintf(h, "%s\x00%s\x00%s", f.Path, f.OldPath, f.Status)
		if o.Head == "" {
			if fi, err := os.Lstat(filepath.Join(o.Root, filepath.FromSlash(f.Path))); err == nil {
				h.Write([]byte("\x00" + strconv.FormatInt(fi.Size(), 10) + "\x00" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)))
			}
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callCacheSize is how many call graphs are kept (one per target and mode).
const callCacheSize = 8

// callErrorTTL is how long a failed computation is remembered, so a broken
// repo isn't recomputed on every poll.
const callErrorTTL = 10 * time.Second

// fingerprintCache keeps one result per key for as long as the key's
// fingerprint stays the same. Concurrent misses for a key share one
// computation.
type fingerprintCache struct {
	now func() time.Time

	mu       sync.Mutex
	items    map[string]*fpEntry
	inflight map[string]*fpFlight
}

type fpEntry struct {
	fp   string
	body []byte
	err  error
	at   time.Time
	used time.Time
}

type fpFlight struct {
	fp   string
	done chan struct{}
	body []byte
	err  error
}

func newFingerprintCache() *fingerprintCache {
	return &fingerprintCache{now: time.Now, items: map[string]*fpEntry{}, inflight: map[string]*fpFlight{}}
}

func (c *fingerprintCache) clear() {
	c.mu.Lock()
	c.items = map[string]*fpEntry{}
	c.mu.Unlock()
}

func (c *fingerprintCache) get(key, fp string, compute func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok && e.fp == fp && (e.err == nil || c.now().Sub(e.at) < callErrorTTL) {
		e.used = c.now()
		c.mu.Unlock()
		return e.body, e.err
	}
	if f, ok := c.inflight[key]; ok && f.fp == fp {
		c.mu.Unlock()
		<-f.done
		return f.body, f.err
	}
	f := &fpFlight{fp: fp, done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()

	f.body, f.err = compute()
	close(f.done)

	c.mu.Lock()
	if c.inflight[key] == f {
		delete(c.inflight, key)
	}
	now := c.now()
	c.items[key] = &fpEntry{fp: fp, body: f.body, err: f.err, at: now, used: now}
	for len(c.items) > callCacheSize {
		var oldest string
		for k, e := range c.items {
			if oldest == "" || e.used.Before(c.items[oldest].used) {
				oldest = k
			}
		}
		delete(c.items, oldest)
	}
	c.mu.Unlock()
	return f.body, f.err
}
