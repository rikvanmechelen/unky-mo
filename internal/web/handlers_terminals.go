package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
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
	s.writeScreen(w, pane)
}

// screenView is a terminal's capture as the browser sees it: Text carries
// tmux's SGR colour escapes, which terminal.js turns into styled spans.
type screenView struct {
	Text          string `json:"text"`
	CursorLine    int    `json:"cursorLine"`
	CursorCol     int    `json:"cursorCol"`
	CursorVisible bool   `json:"cursorVisible"`
}

func (s *Server) writeScreen(w http.ResponseWriter, pane string) {
	sc, err := s.deps.Terminals.Capture(pane)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, screenView{Text: sc.Text, CursorLine: sc.CursorLine, CursorCol: sc.CursorCol, CursorVisible: sc.CursorVisible})
}

// liveKeyNames are the tmux key names a live terminal may send: what a
// shell's line editor uses (completion, history, word and line editing)
// plus enough to get around a full-screen program. Anything else is
// refused, since a name is handed to tmux as a key, not as text.
var liveKeyNames = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`Enter Tab BTab BSpace DC IC Escape Space
		Up Down Left Right Home End PPage NPage
		C-Up C-Down C-Left C-Right M-Up M-Down M-Left M-Right S-Up S-Down S-Left S-Right
		F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12 C-Space M-BSpace M-Enter`) {
		m[k] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		m["C-"+string(c)] = true
		m["M-"+string(c)] = true
	}
	for _, c := range "0123456789.,<>/?_-" {
		m["M-"+string(c)] = true
	}
	return m
}()

const (
	maxLiveKeys    = 256
	maxLiveText    = 4 << 10
	maxLivePaste   = 64 << 10
	liveKeysSettle = 40 * time.Millisecond
)

// handleTerminalKeys is the live terminal's input: keystrokes go straight
// to the shell — so its own completion, suggestions and history search
// work — and the answer is the screen right after, so typing doesn't wait
// for the next output poll. Body: {"keys": [{"text": "ls"}, {"key": "Tab"}]}
// or {"paste": "…"}. Text may hold no control characters (those are keys);
// a paste may hold newlines and tabs and goes in as a bracketed paste.
func (s *Server) handleTerminalKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []struct {
			Text string `json:"text"`
			Key  string `json:"key"`
		} `json:"keys"`
		Paste string `json:"paste"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLivePaste*2)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if len(req.Keys) > maxLiveKeys || len(req.Paste) > maxLivePaste {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("too much input at once"))
		return
	}
	keys := make([]tmux.Key, 0, len(req.Keys))
	total := 0
	for _, k := range req.Keys {
		switch {
		case k.Key != "" && k.Text == "":
			if !liveKeyNames[k.Key] {
				writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported key %q", k.Key))
				return
			}
			keys = append(keys, tmux.Key{Name: k.Key})
		case k.Text != "" && k.Key == "":
			if stripControl(k.Text) != k.Text || strings.ContainsAny(k.Text, "\n\t") {
				writeError(w, http.StatusBadRequest, fmt.Errorf("control characters must be sent as keys"))
				return
			}
			total += len(k.Text)
			keys = append(keys, tmux.Key{Text: k.Text})
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("each key needs exactly one of text or key"))
			return
		}
	}
	if total > maxLiveText {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("too much input at once"))
		return
	}
	paste := stripControl(req.Paste)
	pane, ok := s.ownedTerminal(w, r)
	if !ok {
		return
	}
	if len(keys) > 0 {
		if err := s.deps.Terminals.SendKeys(pane, keys); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
	}
	if paste != "" {
		if err := s.deps.Terminals.Paste(pane, paste); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
	}
	// Give the shell a moment to redraw (a completion, a suggestion).
	time.Sleep(liveKeysSettle)
	s.writeScreen(w, pane)
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

// handleCloseTerminal kills one of the window's terminals.
func (s *Server) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	pane, ok := s.ownedTerminal(w, r)
	if !ok {
		return
	}
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if err := s.deps.Terminals.Close(pane); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{})
}
