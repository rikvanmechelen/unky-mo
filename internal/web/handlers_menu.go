package web

import (
	"fmt"
	"net/http"
)

// readMenu captures Claude's pane and parses the menu on it, nil when none.
func (s *Server) readMenu(windowID string) (*menuView, error) {
	screen, err := s.deps.ClaudePane.CaptureStyled(s.claudeTarget(windowID))
	if err != nil {
		return nil, err
	}
	return parseMenu(screen), nil
}

// handleMenu returns the slash-command menu Claude Code has open in the
// session's pane, or {"menu": null}. The screen is the only source: no hook
// or transcript line says a /model dialog is up. The ETag is the menu's
// sig, so the browser's polling costs a 304 while nothing moves.
func (s *Server) handleMenu(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	if _, _, _, ok := s.resolveSession(windowID); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	m, err := s.readMenu(windowID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if m != nil {
		etag := `"` + m.Sig + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, map[string]*menuView{"menu": m})
}
