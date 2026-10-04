package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// modeView is the permission mode shown in Claude Code's footer, under the
// prompt box:
//
//	⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt
//
// Mode is the --permission-mode name ("auto", "plan", …) or "" when the
// label isn't one we know; Label is the footer's own words ("auto mode").
// No structured source carries the live mode: hooks and the JSONL only
// record it when an event or prompt happens, and shift+tab fires neither.
type modeView struct {
	Mode  string `json:"mode"`
	Label string `json:"label"`
}

// modeLine matches the footer's mode indicator: a ⏸ or ⏵⏵ glyph, the
// mode's label, then "on".
var modeLine = regexp.MustCompile(`^\s*(?:⏸|⏵⏵)\s+(\S.*?)\s+on\b`)

// modeLabels maps footer labels to --permission-mode names.
var modeLabels = map[string]string{
	"manual mode":        "manual",
	"accept edits":       "acceptEdits",
	"plan mode":          "plan",
	"auto mode":          "auto",
	"bypass permissions": "bypassPermissions",
}

// parseMode finds the mode indicator below the prompt box's bottom border.
// Returns nil when there's none (e.g. a dialog is replacing the prompt box).
func parseMode(screen string) *modeView {
	lines := strings.Split(screen, "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isRule(lines[i]) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	for _, l := range lines[start:] {
		if m := modeLine.FindStringSubmatch(l); m != nil {
			label := strings.TrimSpace(m[1])
			return &modeView{Mode: modeLabels[strings.ToLower(label)], Label: label}
		}
	}
	return nil
}

// modeSettleTries bounds how long a mode change waits for the footer to
// redraw after each shift+tab (tries × Server.modeSettle).
const modeSettleTries = 40

var errModeUnseen = errors.New("can't see the mode in Claude's footer")

func (s *Server) claudeTarget(windowID string) string {
	return s.tmuxSession + ":" + windowID + ".0"
}

func (s *Server) captureMode(target string) (*modeView, error) {
	screen, err := s.deps.ClaudePane.Capture(target)
	if err != nil {
		return nil, err
	}
	return parseMode(screen), nil
}

// handleMode returns the session's current permission mode, or
// {"mode": null} when the footer doesn't show one.
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	if _, _, _, ok := s.resolveSession(windowID); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	m, err := s.captureMode(s.claudeTarget(windowID))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]*modeView{"mode": m})
}

// handleSetMode changes the permission mode the way a user would: by
// pressing shift+tab in Claude's pane. The body is {"mode": "<name>"} to
// cycle until the footer shows that mode, or {"next": true} for one step.
// The cycle's order and members depend on the user's settings (auto mode
// only when enabled, bypass only when launched for it), so nothing is
// hard-coded: each press is checked against the footer, and a mode that
// isn't reached before the cycle repeats is a 409.
//
// Only while idle or active: in "question" or "permission" a dialog has
// the keyboard, and shift+tab would act on it instead.
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	var body struct {
		Mode string `json:"mode"`
		Next bool   `json:"next"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if !body.Next && !knownMode(body.Mode) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown mode %q", body.Mode))
		return
	}
	_, _, status, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if status != "idle" && status != "active" {
		writeError(w, http.StatusConflict, fmt.Errorf("session is %s; answer it before changing mode", status))
		return
	}

	// One change at a time: two interleaved cycles would each see the
	// other's presses.
	s.modeMu.Lock()
	defer s.modeMu.Unlock()

	target := s.claudeTarget(windowID)
	cur, err := s.captureMode(target)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if cur == nil {
		writeError(w, http.StatusConflict, errModeUnseen)
		return
	}
	if body.Next {
		if cur, err = s.pressShiftTab(target, cur); err != nil {
			writeModeError(w, err)
			return
		}
		writeJSON(w, map[string]*modeView{"mode": cur})
		return
	}
	seen := map[string]bool{}
	for cur.Mode != body.Mode {
		if seen[cur.Label] {
			writeError(w, http.StatusConflict, fmt.Errorf("%s isn't in this session's shift+tab cycle", body.Mode))
			return
		}
		seen[cur.Label] = true
		if cur, err = s.pressShiftTab(target, cur); err != nil {
			writeModeError(w, err)
			return
		}
	}
	writeJSON(w, map[string]*modeView{"mode": cur})
}

// pressShiftTab sends one shift+tab and waits for the footer to show a
// different mode than from.
func (s *Server) pressShiftTab(target string, from *modeView) (*modeView, error) {
	if err := s.deps.ClaudePane.CycleMode(target); err != nil {
		return nil, err
	}
	for i := 0; i < modeSettleTries; i++ {
		cur, err := s.captureMode(target)
		if err != nil {
			return nil, err
		}
		if cur != nil && cur.Label != from.Label {
			return cur, nil
		}
		time.Sleep(s.modeSettle)
	}
	return nil, errModeUnseen
}

func writeModeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errModeUnseen) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeError(w, http.StatusBadGateway, err)
}

func knownMode(mode string) bool {
	for _, m := range modeLabels {
		if m == mode {
			return true
		}
	}
	return false
}
