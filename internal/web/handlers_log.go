package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// logResponse is the Files panel's Graph tab. Repo is false when the
// session's cwd isn't inside a git checkout.
type logResponse struct {
	Repo bool `json:"repo"`
	*gitfiles.Log
}

// handleSessionLog serves the commit graph of a live session's checkout:
// ?scope=branch (default) or all. The ETag is a hash of the body, so the
// panel's poll gets a body-less 304 while nothing moved.
func (s *Server) handleSessionLog(w http.ResponseWriter, r *http.Request) {
	windowID, scope := r.PathValue("windowID"), r.URL.Query().Get("scope")
	if scope == "" {
		scope = gitfiles.ScopeBranch
	}
	if scope != gitfiles.ScopeBranch && scope != gitfiles.ScopeAll {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown scope %q", scope))
		return
	}
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	v, err := s.logCache.get(dir+"\x00"+scope, func() (any, error) {
		l, err := s.deps.Git.Log(dir, scope)
		if err != nil {
			return nil, err
		}
		return json.Marshal(logResponse{Repo: true, Log: l})
	})
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, logResponse{Repo: false})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeHashed(w, r, v.([]byte))
}

// sessionRoot returns the repo root of the live session at windowID, or
// the HTTP status and error to answer with.
func (s *Server) sessionRoot(windowID string) (string, int, error) {
	dir, ok := s.sessionPath(windowID)
	if !ok {
		return "", http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID)
	}
	v, err := s.filesCache.get(dir, func() (any, error) { return s.deps.Git.Changes(dir) })
	if errors.Is(err, gitfiles.ErrNotRepo) {
		return "", http.StatusNotFound, err
	}
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	return v.(*gitfiles.Changes).Root, 0, nil
}

// commitStatus maps a gitfiles commit error to its HTTP status.
func commitStatus(err error) int {
	if errors.Is(err, gitfiles.ErrUnknownCommit) || errors.Is(err, gitfiles.ErrNotInCommit) {
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// handleCommit serves one commit's message and changed files. The hash
// must be a full commit id: gitfiles refuses anything else (a ref name, an
// abbreviation, an option) before git sees it.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	root, status, err := s.sessionRoot(r.PathValue("windowID"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	d, err := s.deps.Git.Commit(root, r.PathValue("hash"))
	if err != nil {
		writeError(w, commitStatus(err), err)
		return
	}
	// A commit never changes, but the session's checkout might move to
	// another repo, so it's only cached briefly.
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, d)
}

// handleCommitFile serves both versions of one file a commit changed, for
// a read-only commit diff tab: ?path= must be one of the commit's changed
// files. Contents come from git's object store, never the working tree.
func (s *Server) handleCommitFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing path"))
		return
	}
	root, status, err := s.sessionRoot(r.PathValue("windowID"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	d, err := s.deps.Git.CommitFile(root, r.PathValue("hash"), path)
	if err != nil {
		writeError(w, commitStatus(err), err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, d)
}
