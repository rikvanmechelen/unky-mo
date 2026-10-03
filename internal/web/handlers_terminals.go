package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rvanmech/unky-mo/internal/state"
)

// terminalView is a drawer tab as the browser sees it. ID is the pane
// number without tmux's "%" (so it travels in URLs unescaped).
type terminalView struct {
	ID      string `json:"id"`
	Name    string `json:"name"` // e.g. "fish unky-mo"
	Cwd     string `json:"cwd"`
	Visible bool   `json:"visible"`
}

// windowRow returns the state-file row of the tmux window windowID.
func (s *Server) windowRow(windowID string) (state.ProjectState, bool) {
	st, err := s.deps.State.Read()
	if err != nil {
		return state.ProjectState{}, false
	}
	for _, p := range st.Projects {
		if p.WindowID == windowID {
			return p, true
		}
	}
	return state.ProjectState{}, false
}

// ownedTerminal resolves {windowID} and {pane} from the request and checks
// that the pane is one of that window's terminals — the browser can never
// address Claude's pane, the sidebar, or another window's panes. Writes
// the error response and returns ok=false otherwise.
func (s *Server) ownedTerminal(w http.ResponseWriter, r *http.Request) (string, bool) {
	windowID, pane := r.PathValue("windowID"), r.PathValue("pane")
	row, ok := s.windowRow(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no window %s", windowID))
		return "", false
	}
	terms, err := s.deps.Terminals.List(row)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return "", false
	}
	for _, t := range terms {
		if t.ID == "%"+pane {
			return t.ID, true
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("no terminal %s in window %s", pane, windowID))
	return "", false
}

func (s *Server) handleTerminals(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	row, ok := s.windowRow(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no window %s", windowID))
		return
	}
	terms, err := s.deps.Terminals.List(row)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	out := make([]terminalView, 0, len(terms))
	for _, t := range terms {
		name := t.Command
		if base := filepath.Base(t.Cwd); t.Cwd != "" && base != "/" {
			name += " " + base
		}
		out = append(out, terminalView{ID: strings.TrimPrefix(t.ID, "%"), Name: name, Cwd: t.Cwd, Visible: t.Visible})
	}
	writeJSON(w, out)
}

// handleNewTerminal opens a terminal for the window, parked in its
// mo-terms session (see Terminals.New).
func (s *Server) handleNewTerminal(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	row, ok := s.windowRow(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no window %s", windowID))
		return
	}
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	id, err := s.deps.Terminals.New(row)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"id": strings.TrimPrefix(id, "%")})
}

func (s *Server) handleTerminalOutput(w http.ResponseWriter, r *http.Request) {
	pane, ok := s.ownedTerminal(w, r)
	if !ok {
		return
	}
	text, err := s.deps.Terminals.Capture(pane)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"text": text})
}

// handleTerminalInput types one line into the terminal and presses Enter.
// Same trust level as the prompt box: anyone who can reach mo web can
// already drive a live agent (see the non-loopback auth requirement).
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if strings.ContainsAny(req.Text, "\r\n") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("one line at a time"))
		return
	}
	pane, ok := s.ownedTerminal(w, r)
	if !ok {
		return
	}
	if err := s.deps.Terminals.SendLine(pane, req.Text); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{})
}

func (s *Server) handleTerminalInterrupt(w http.ResponseWriter, r *http.Request) {
	pane, ok := s.ownedTerminal(w, r)
	if !ok {
		return
	}
	if err := s.deps.Terminals.Interrupt(pane); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{})
}
