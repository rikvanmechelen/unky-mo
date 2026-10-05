package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
)

// bashChangeView is one Bash call's change in the listing. Overlaps counts
// the session's other changing Bash calls that ran at the same time: their
// changes show up in each other's diffs, since a snapshot sees the whole
// checkout.
type bashChangeView struct {
	bashsnap.Change
	Overlaps int `json:"overlaps,omitempty"`
}

// bashRow resolves {windowID} to its live session, or writes a 404.
func (s *Server) bashRow(w http.ResponseWriter, r *http.Request) (sessionID, path string, ok bool) {
	windowID := r.PathValue("windowID")
	row, found := s.windowRow(windowID)
	if !found || row.SessionID == "" || row.Path == "" || s.deps.BashDiffs == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no Bash changes for window %s", windowID))
		return "", "", false
	}
	return row.SessionID, row.Path, true
}

// sameCheckout reports whether a record's checkout is the session's: the
// window's path is the top level or inside it. A session that cd'd into
// another repo keeps those records to itself.
func sameCheckout(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// handleBashChanges lists the Bash calls of the window's session that
// changed its checkout, by tool use id. ETag = body hash, so polls that find
// nothing new are 304s.
func (s *Server) handleBashChanges(w http.ResponseWriter, r *http.Request) {
	sessionID, path, ok := s.bashRow(w, r)
	if !ok {
		return
	}
	changes, err := s.deps.BashDiffs.List(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]bashChangeView{}
	for id, c := range changes {
		if sameCheckout(c.Root, path) {
			out[id] = bashChangeView{Change: c}
		}
	}
	for id, a := range out {
		for other, b := range out {
			if other != id && a.Start.Before(b.End) && b.Start.Before(a.End) {
				a.Overlaps++
			}
		}
		out[id] = a
	}
	body, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeHashed(w, r, body)
}

// handleBashDiff serves one Bash call's diff. The tool use id only names a
// record of the window's own session; the checkout and revisions come from
// that record.
func (s *Server) handleBashDiff(w http.ResponseWriter, r *http.Request) {
	sessionID, path, ok := s.bashRow(w, r)
	if !ok {
		return
	}
	toolUseID := r.PathValue("toolUseID")
	if !bashsnap.ValidToolUse(toolUseID) {
		writeError(w, http.StatusNotFound, errors.New("unknown tool use"))
		return
	}
	root, d, err := s.deps.BashDiffs.Diff(r.Context(), sessionID, toolUseID)
	if errors.Is(err, bashsnap.ErrNoRecord) || (err == nil && !sameCheckout(root, path)) {
		writeError(w, http.StatusNotFound, errors.New("no change recorded for this call"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	body, err := json.Marshal(d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeHashed(w, r, body)
}
