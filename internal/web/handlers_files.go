package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// filesResponse is the Files panel's Changed tab + footer. Repo is false
// when the session's cwd isn't inside a git checkout.
type filesResponse struct {
	Repo bool `json:"repo"`
	*gitfiles.Changes
}

type treeResponse struct {
	Repo  bool     `json:"repo"`
	Root  string   `json:"root,omitempty"`
	Paths []string `json:"paths"`
}

// handleSessionFiles serves the changed files, line counts and upstream
// sync state of the checkout a live session runs in. The directory comes
// from the state file — the browser only names the window.
func (s *Server) handleSessionFiles(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	v, err := s.filesCache.get(dir, func() (any, error) { return s.deps.Git.Changes(dir) })
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, filesResponse{Repo: false})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, filesResponse{Repo: true, Changes: v.(*gitfiles.Changes)})
}

// handleSessionTree serves every tracked and untracked file in the
// session's checkout, for the Files panel's "All files" tab.
func (s *Server) handleSessionTree(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	v, err := s.treeCache.get(dir, func() (any, error) {
		root, paths, err := s.deps.Git.Tree(dir)
		return treeResponse{Repo: true, Root: root, Paths: paths}, err
	})
	if errors.Is(err, gitfiles.ErrNotRepo) {
		writeJSON(w, treeResponse{Repo: false, Paths: []string{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}

// handleSessionDiff serves the diff of one changed file in the session's
// checkout. The path comes from the browser, so it's only diffed when it's
// in that checkout's current changes list (and git runs at the repo root
// that list came from).
func (s *Server) handleSessionDiff(w http.ResponseWriter, r *http.Request) {
	windowID, path := r.PathValue("windowID"), r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing path"))
		return
	}
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	v, err := s.filesCache.get(dir, func() (any, error) { return s.deps.Git.Changes(dir) })
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	ch := v.(*gitfiles.Changes)
	var file *gitfiles.File
	for i := range ch.Files {
		if ch.Files[i].Path == path {
			file = &ch.Files[i]
			break
		}
	}
	if file == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("%s has no changes", path))
		return
	}
	diff, truncated, err := s.deps.Git.Diff(ch.Root, file.Path, file.Status == "?")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{
		"path": file.Path, "status": file.Status, "added": file.Added, "removed": file.Removed,
		"diff": diff, "truncated": truncated,
	})
}
