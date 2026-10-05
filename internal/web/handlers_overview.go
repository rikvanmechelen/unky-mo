package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

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
// branch), head (uncommitted only) or commits (&commits=: consecutive
// commits selected in the Git log tab). The browser picks a mode or names
// commits, never a revision to compare with. The ETag is a hash of the
// body, so the tab's poll gets a body-less 304 while nothing changed.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	dir, q, ok := s.sessionChange(w, r)
	if !ok {
		return
	}
	o, err := s.change(dir, q)
	writeOverview(w, r, o, err, nil)
}

// changeQuery is the change an Overview request asks for.
type changeQuery struct {
	mode    string   // gitfiles.ModeBranch, ModeHead or ModeCommits
	commits []string // ModeCommits only: sorted, without duplicates
	// worktree (ModeCommits only) adds the uncommitted changes on top of
	// HEAD: commits may then be empty, and must otherwise end at HEAD.
	worktree bool
}

// parseChange reads ?base= (default branch) and, for base=commits, the
// comma-separated full commit ids in ?commits= and worktree=1 for the
// uncommitted changes. Only the syntax is checked here; gitfiles checks
// the commits themselves.
func parseChange(r *http.Request) (changeQuery, error) {
	q := changeQuery{mode: r.URL.Query().Get("base")}
	if q.mode == "" {
		q.mode = gitfiles.ModeBranch
	}
	list, wt := r.URL.Query().Get("commits"), r.URL.Query().Get("worktree")
	switch q.mode {
	case gitfiles.ModeBranch, gitfiles.ModeHead:
		if list != "" || wt != "" {
			return q, fmt.Errorf("commits and worktree only go with base=commits")
		}
		return q, nil
	case gitfiles.ModeCommits:
	default:
		return q, fmt.Errorf("unknown base %q", q.mode)
	}
	switch wt {
	case "":
	case "1":
		q.worktree = true
		if list == "" {
			return q, nil
		}
	default:
		return q, fmt.Errorf("bad worktree %q", wt)
	}
	for _, h := range strings.Split(list, ",") {
		if !commitIDRe.MatchString(h) {
			return q, fmt.Errorf("bad commit id %q", h)
		}
		q.commits = append(q.commits, h)
	}
	slices.Sort(q.commits)
	q.commits = slices.Compact(q.commits)
	if len(q.commits) > gitfiles.MaxSelection {
		return q, fmt.Errorf("at most %d commits", gitfiles.MaxSelection)
	}
	return q, nil
}

var commitIDRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// key caches what q asks for in the checkout at dir.
func (q changeQuery) key(dir string) string {
	k := dir + "\x00" + q.mode
	if q.mode == gitfiles.ModeCommits {
		k += "\x00" + strings.Join(q.commits, ",")
		if q.worktree {
			k += "\x00wt"
		}
	}
	return k
}

// sessionChange reads the change a session endpoint asks for and the
// session's checkout, or answers with why it can't.
func (s *Server) sessionChange(w http.ResponseWriter, r *http.Request) (string, changeQuery, bool) {
	q, err := parseChange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return "", q, false
	}
	windowID := r.PathValue("windowID")
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return "", q, false
	}
	return dir, q, true
}

// change reads (through the caches) the change q asks for in the checkout
// at dir.
func (s *Server) change(dir string, q changeQuery) (*gitfiles.Overview, error) {
	if q.mode != gitfiles.ModeCommits {
		return s.overview(dir, q.mode)
	}
	if q.worktree {
		return s.worktreeChange(dir, q)
	}
	sel, err := s.selection(dir, q)
	if err != nil {
		return nil, err
	}
	v, err := s.overviewCache.get(sel.Root+"\x00range\x00"+sel.Base+"\x00"+sel.Head, func() (any, error) {
		return s.deps.Git.OverviewRange(sel.Root, sel.Base, sel.Head)
	})
	if err != nil {
		return nil, err
	}
	return v.(*gitfiles.Overview), nil
}

// worktreeChange reads a selection that includes the uncommitted changes:
// from HEAD, or from the commit before the selected commits (which must end
// at HEAD, where the uncommitted changes sit), to the working tree. HEAD is
// read on every request (through a short cache): it moves, while the
// commits' resolution is kept long.
func (s *Server) worktreeChange(dir string, q changeQuery) (*gitfiles.Overview, error) {
	type rootHead struct{ root, head string }
	v, err := s.overviewCache.get("head\x00"+dir, func() (any, error) {
		root, head, err := s.deps.Git.HeadCommit(dir)
		return rootHead{root, head}, err
	})
	if err != nil {
		return nil, err
	}
	rh := v.(rootHead)
	base := rh.head
	if len(q.commits) > 0 {
		sel, err := s.selection(dir, q)
		if err != nil {
			return nil, err
		}
		if sel.Head != rh.head {
			return nil, &gitfiles.SelectionError{Reason: "worktree", Commit: sel.Head, Other: rh.head}
		}
		base = sel.Base
	}
	v, err = s.overviewCache.get(rh.root+"\x00wt\x00"+base, func() (any, error) {
		return s.deps.Git.OverviewWorktree(rh.root, base)
	})
	if err != nil {
		return nil, err
	}
	return v.(*gitfiles.Overview), nil
}

// selection resolves q's commits to the two commits their change lies
// between. A commit's parents never change, so the answer is kept long.
func (s *Server) selection(dir string, q changeQuery) (*gitfiles.Selection, error) {
	v, err := s.selectionCache.get(dir+"\x00"+strings.Join(q.commits, ","), func() (any, error) { return s.deps.Git.ResolveSelection(dir, q.commits) })
	if err != nil {
		return nil, err
	}
	return v.(*gitfiles.Selection), nil
}

// changeStatus is the HTTP status for a failure to read a change: a
// malformed or non-consecutive selection is the request's fault.
func changeStatus(err error) int {
	var se *gitfiles.SelectionError
	switch {
	case errors.Is(err, gitfiles.ErrBadSelection):
		return http.StatusBadRequest
	case errors.As(err, &se), errors.Is(err, gitfiles.ErrNoCommits):
		return http.StatusUnprocessableEntity
	case errors.Is(err, gitfiles.ErrUnknownCommit):
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// writeOverview answers with an overview (or why there's none).
func writeOverview(w http.ResponseWriter, r *http.Request, o *gitfiles.Overview, err error, target *targetView) {
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, overviewResponse{Repo: false, Target: target})
		return
	}
	if err != nil {
		writeError(w, changeStatus(err), err)
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
	dir, q, ok := s.sessionChange(w, r)
	if !ok {
		return
	}
	s.serveArchitecture(w, r, q.key(dir), func() (*gitfiles.Overview, error) { return s.change(dir, q) })
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
		writeError(w, changeStatus(err), err)
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
