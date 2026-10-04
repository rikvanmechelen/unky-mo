package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// handleSessionFile serves one file of a session's checkout for the chat
// view's editor tabs: the working-tree version, or with ?rev=HEAD the
// committed one (the left side of a diff tab).
//
// The path comes from the browser, so it must be one the Files panel lists:
// a tracked or untracked-but-not-ignored file (Tree), or a changed one
// (Changes, which also covers files deleted from the working tree). Ignored
// files like .env are out of reach. gitfiles then re-checks that the path
// doesn't resolve outside the checkout through a symlink.
//
// The ETag is the content hash, so open tabs can poll for on-disk changes
// with If-None-Match and get a body-less 304 while nothing changed.
func (s *Server) handleSessionFile(w http.ResponseWriter, r *http.Request) {
	windowID, path, rev := r.PathValue("windowID"), r.URL.Query().Get("path"), r.URL.Query().Get("rev")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing path"))
		return
	}
	if rev != "" && rev != "HEAD" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported rev %q", rev))
		return
	}
	root, status, err := s.listedPath(windowID, path)
	if err != nil {
		writeError(w, status, err)
		return
	}

	var c *gitfiles.Content
	if rev == "HEAD" {
		c, err = s.deps.Git.ReadHEAD(root, path)
	} else {
		c, err = s.deps.Git.ReadFile(root, path)
	}
	if errors.Is(err, gitfiles.ErrOutsideRoot) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	etag := contentETag(c)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, c)
}

// listedPath returns the repo root of the live session at windowID if path
// is one of that checkout's listed files (see handleSessionFile), or the
// HTTP status and error to answer with.
func (s *Server) listedPath(windowID, path string) (root string, status int, err error) {
	dir, ok := s.sessionPath(windowID)
	if !ok {
		return "", http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID)
	}
	v, err := s.treeCache.get(dir, func() (any, error) {
		root, paths, err := s.deps.Git.Tree(dir)
		return treeResponse{Repo: true, Root: root, Paths: paths}, err
	})
	if errors.Is(err, gitfiles.ErrNotRepo) {
		return "", http.StatusNotFound, err
	}
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	tree := v.(treeResponse)
	if _, found := slices.BinarySearch(tree.Paths, path); found {
		return tree.Root, 0, nil
	}
	// Not in the (up to 10s old) tree: maybe deleted, or created since.
	v, err = s.filesCache.get(dir, func() (any, error) { return s.deps.Git.Changes(dir) })
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	ch := v.(*gitfiles.Changes)
	for _, f := range ch.Files {
		if f.Path == path {
			return ch.Root, 0, nil
		}
	}
	return "", http.StatusNotFound, fmt.Errorf("%s is not a file in this checkout", path)
}

// contentETag is a strong ETag for one file version: its hash, or a marker
// for a missing or too-large file (which has no hash).
func contentETag(c *gitfiles.Content) string {
	switch {
	case !c.Exists:
		return `"missing"`
	case c.Hash == "":
		return fmt.Sprintf(`"large-%d"`, c.Size)
	default:
		return `"` + c.Hash + `"`
	}
}
