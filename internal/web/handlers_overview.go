package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
)

// overviewResponse is the Overview tab. Repo is false when the session's
// cwd isn't inside a git checkout. Target describes the branch the
// reviewer view resolved (absent in the chat view).
type overviewResponse struct {
	Repo bool `json:"repo"`
	*gitfiles.Overview
	Target *targetView `json:"target,omitempty"`
}

// handleOverview serves a live session's change for the Overview tab:
// ?base=branch (default: everything since the branch split off the default
// branch) or head (uncommitted only). The browser picks a mode, never a
// revision. The ETag is a hash of the body, so the tab's poll gets a
// body-less 304 while nothing changed.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
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
	o, err := s.overview(dir, mode)
	writeOverview(w, r, o, err, nil)
}

// writeOverview answers with an overview (or why there's none).
func writeOverview(w http.ResponseWriter, r *http.Request, o *gitfiles.Overview, err error, target *targetView) {
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, overviewResponse{Repo: false, Target: target})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	body, err := json.Marshal(overviewResponse{Repo: true, Overview: o, Target: target})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeHashed(w, r, body)
}

// architectureResponse is the Overview tab's architecture and contract
// surface. Repo is false outside a git checkout.
type architectureResponse struct {
	Repo bool `json:"repo"`
	*review.Analysis
}

// handleArchitecture serves the architecture delta and contract surface of
// a live session's change, for the same ?base= modes as handleOverview. It
// analyzes the (shared, cached) overview, so both views agree on the files.
func (s *Server) handleArchitecture(w http.ResponseWriter, r *http.Request) {
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
	s.serveArchitecture(w, r, dir+"\x00"+mode, func() (*gitfiles.Overview, error) { return s.overview(dir, mode) })
}

// serveArchitecture analyzes the overview get returns, cached under key.
func (s *Server) serveArchitecture(w http.ResponseWriter, r *http.Request, key string, get func() (*gitfiles.Overview, error)) {
	v, err := s.archCache.get(key, func() (any, error) {
		o, err := get()
		if err != nil {
			return nil, err
		}
		a, err := s.deps.Review.Analyze(o)
		if err != nil {
			return nil, err
		}
		return json.Marshal(architectureResponse{Repo: true, Analysis: a})
	})
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, architectureResponse{Repo: false})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeHashed(w, r, v.([]byte))
}

// handleFetchBase updates origin's copy of the session's overview base,
// for the Overview's "last fetched N days ago · Fetch".
func (s *Server) handleFetchBase(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	o, err := s.overview(dir, gitfiles.ModeBranch)
	s.fetchBase(w, o, err)
}

// fetchBase fetches o's base and forgets the overviews computed from the
// old one.
func (s *Server) fetchBase(w http.ResponseWriter, o *gitfiles.Overview, err error) {
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if o.Base == "" {
		writeError(w, http.StatusConflict, fmt.Errorf("this change has no base branch to fetch"))
		return
	}
	s.fetchMu.Lock()
	err = s.deps.Git.FetchBase(o.Root, o.Base)
	s.fetchMu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.overviewCache.clear()
	s.archCache.clear()
	s.callCache.clear()
	w.WriteHeader(http.StatusNoContent)
}

// overview reads (through a short cache) the change of the checkout at dir.
func (s *Server) overview(dir, mode string) (*gitfiles.Overview, error) {
	v, err := s.overviewCache.get(dir+"\x00"+mode, func() (any, error) { return s.deps.Git.Overview(dir, mode) })
	if err != nil {
		return nil, err
	}
	return v.(*gitfiles.Overview), nil
}

// inBranchOverview reports whether path is a file the branch's change
// lists. A file deleted in one of the branch's commits is in neither the
// tree nor the uncommitted changes, but its branch diff can still be shown.
func (s *Server) inBranchOverview(dir, path string) (root string, ok bool) {
	o, err := s.overview(dir, gitfiles.ModeBranch)
	if err != nil {
		return "", false
	}
	for _, f := range o.Files {
		if f.Path == path {
			return o.Root, true
		}
	}
	return "", false
}

// readBase reads the base side of a branch diff: path as it was at the
// merge base (under its old name if the branch renamed it). Without a merge
// base (the overview fell back to uncommitted changes) that's HEAD. The
// revision is always the server's own merge base, never the browser's.
func (s *Server) readBase(dir, root, path string) (*gitfiles.Content, error) {
	o, err := s.overview(dir, gitfiles.ModeBranch)
	if err != nil {
		return nil, err
	}
	if o.MergeBase == "" {
		return s.deps.Git.ReadHEAD(root, path)
	}
	old := path
	for _, f := range o.Files {
		if f.Path == path && f.OldPath != "" {
			old = f.OldPath
		}
	}
	c, err := s.deps.Git.ReadAt(o.Root, o.MergeBase, old)
	if c != nil {
		c.Path = path
	}
	return c, err
}

// writeHashed writes a JSON body with an ETag of its hash, answering a
// matching If-None-Match with a body-less 304.
func writeHashed(w http.ResponseWriter, r *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
