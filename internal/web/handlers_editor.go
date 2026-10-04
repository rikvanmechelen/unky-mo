package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// handleSessionFile serves one file of a session's checkout for the chat
// view's editor tabs: the working-tree version, or with ?rev=HEAD the
// committed one (the left side of a diff tab), or with ?rev=base the one
// at the branch's merge base (the left side of a branch diff, see
// readBase).
//
// The path comes from the browser, so it must be one the Files panel lists:
// a tracked or untracked-but-not-ignored file (Tree), or a changed one
// (Changes, which also covers files deleted from the working tree, or the
// branch overview, which covers files deleted in the branch's commits). Ignored
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
	if rev != "" && rev != "HEAD" && rev != "base" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported rev %q", rev))
		return
	}
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	s.serveFile(w, r, dir, path, rev)
}

// serveFile answers with one file of the checkout containing dir (see
// handleSessionFile for the path rules and revs).
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, dir, path, rev string) {
	root, status, err := s.listedPath(dir, path)
	if err != nil {
		writeError(w, status, err)
		return
	}

	var c *gitfiles.Content
	switch rev {
	case "HEAD":
		c, err = s.deps.Git.ReadHEAD(root, path)
	case "base":
		c, err = s.readBase(dir, root, path)
	default:
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
	writeContent(w, r, c, false)
}

// fileResponse is one file version for an editor tab. ReadOnly marks a
// version that can't be saved (a branch that isn't checked out).
type fileResponse struct {
	*gitfiles.Content
	ReadOnly bool `json:"readOnly,omitempty"`
}

// writeContent answers with a file version, ETag'd by its hash.
func writeContent(w http.ResponseWriter, r *http.Request, c *gitfiles.Content, readOnly bool) {
	etag := contentETag(c)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, fileResponse{Content: c, ReadOnly: readOnly})
}

// listedPath returns the repo root of the checkout containing dir if path
// is one of its listed files (see handleSessionFile), or the HTTP status
// and error to answer with.
func (s *Server) listedPath(dir, path string) (root string, status int, err error) {
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
	if root, ok := s.inBranchOverview(dir, path); ok {
		return root, 0, nil
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

// maxSaveBody bounds a save request: the file's text JSON-escaped (up to 6
// bytes per byte for control characters) plus the small envelope.
const maxSaveBody = 6*gitfiles.MaxContentBytes + 64<<10

// handleSaveFile saves an editor tab: PUT {path, text, baseHash}. The path
// rules are the read's (listedPath, then gitfiles.Resolve), and the write
// only happens if the file still has baseHash — the hash of the version
// the edit started from. Otherwise it's a 409 carrying the current
// version, so the browser can show what changed instead of overwriting
// Claude's edit. Saving doesn't depend on session status: the hash check
// is what keeps a save from clobbering a concurrent edit.
func (s *Server) handleSaveFile(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	s.saveFile(w, r, dir)
}

// saveFile saves an editor tab in the checkout containing dir (see
// handleSaveFile).
func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, dir string) {
	var body struct {
		Path     string `json:"path"`
		Text     string `json:"text"`
		BaseHash string `json:"baseHash"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSaveBody)).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, gitfiles.ErrTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if body.Path == "" || body.BaseHash == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path and baseHash are required"))
		return
	}
	root, status, err := s.listedPath(dir, body.Path)
	if err != nil {
		writeError(w, status, err)
		return
	}

	c, err := s.deps.Git.WriteFile(root, body.Path, body.Text, body.BaseHash)
	var conflict *gitfiles.ConflictError
	switch {
	case errors.As(err, &conflict):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "current": conflict.Current})
		return
	case errors.Is(err, gitfiles.ErrOutsideRoot):
		writeError(w, http.StatusForbidden, err)
		return
	case errors.Is(err, gitfiles.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("ETag", contentETag(c))
	writeJSON(w, c)
}
