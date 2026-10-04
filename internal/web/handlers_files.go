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
	s.serveTree(w, dir)
}

// serveTree answers with the file list of the checkout containing dir.
func (s *Server) serveTree(w http.ResponseWriter, dir string) {
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
