package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/rvanmech/unky-mo/internal/notify"
)

func newBootID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// handleBoot returns this process's boot id. After POST /api/restart the
// browser polls it until the id changes, then reloads the page so it gets
// the new server's static files.
func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"boot": s.bootID})
}

// handleRestart does what ctrl+alt+r does in the TUI: restart the TUI, its
// sidebars and (through the TUI's startup checks) this web server. It only
// queues the restart on the TUI's socket, so the 202 goes out before this
// process is replaced.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.deps.Restarter == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("restart isn't available"))
		return
	}
	if err := s.deps.Restarter.Restart(); err != nil {
		if errors.Is(err, notify.ErrTUINotRunning) {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
