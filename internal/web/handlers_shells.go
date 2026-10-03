package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// shellOutputBytes is how much of a shell's output file the drawer shows.
const shellOutputBytes = 64 << 10

type shellView struct {
	ID      string `json:"id"` // the shell's PID
	Command string `json:"command"`
	Started string `json:"started"`
	Output  bool   `json:"output"` // has an output file to show
}

// liveSessionShells lists the shells of the live session in windowID,
// through a short cache (each listing costs a ps plus an lsof per shell).
func (s *Server) liveSessionShells(windowID string) ([]claude.ActiveShell, bool, error) {
	row, ok := s.windowRow(windowID)
	if !ok || row.SessionID == "" {
		return nil, false, nil
	}
	v, err := s.shellsCache.get(row.SessionID, func() (any, error) { return s.deps.Shells.List(row.SessionID) })
	if err != nil {
		return nil, true, err
	}
	shells, _ := v.([]claude.ActiveShell)
	return shells, true, nil
}

func (s *Server) handleShells(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	shells, ok, err := s.liveSessionShells(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	out := make([]shellView, 0, len(shells))
	for _, sh := range shells {
		out = append(out, shellView{ID: strconv.Itoa(sh.PID), Command: sh.Command, Started: sh.StartTime, Output: sh.OutputFile != ""})
	}
	writeJSON(w, out)
}

// handleShellOutput serves the tail of one shell's output file. The pid
// must be one of the session's current shells; the file path comes from
// that listing, never from the request.
func (s *Server) handleShellOutput(w http.ResponseWriter, r *http.Request) {
	windowID, pid := r.PathValue("windowID"), r.PathValue("pid")
	shells, ok, err := s.liveSessionShells(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	for _, sh := range shells {
		if strconv.Itoa(sh.PID) != pid || sh.OutputFile == "" {
			continue
		}
		text, truncated, err := s.deps.Shells.Tail(sh.OutputFile, shellOutputBytes)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"text": text, "truncated": truncated})
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("no shell %s with output in window %s", pid, windowID))
}
