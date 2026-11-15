package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
)

// recentSessionLimit mirrors the TUI's project detail view (RecentSessions(path, 5)).
const recentSessionLimit = 5

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type agentView struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// handleAgents lists the configured agents — key and name only, never the
// command they run.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	def := s.defaultAgent()
	out := make([]agentView, 0, len(s.deps.Agents))
	for _, a := range s.deps.Agents {
		out = append(out, agentView{Key: a.Key, Name: a.Name, Default: def != nil && a.Key == def.Key})
	}
	writeJSON(w, out)
}

type liveSessionView struct {
	WindowID   string `json:"window_id"`
	WindowName string `json:"window_name"`
	SessionID  string `json:"session_id"`
	Status     string `json:"status"`
	Primary    bool   `json:"primary"`
}

type recentSessionView struct {
	SessionID  string    `json:"session_id"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	LastActive time.Time `json:"last_active"`
	Live       bool      `json:"live"`
	WindowID   string    `json:"window_id,omitempty"`
}

type checkoutView struct {
	Branch string              `json:"branch"`
	IsMain bool                `json:"is_main"`
	Path   string              `json:"path"`
	Live   []liveSessionView   `json:"live"`
	Recent []recentSessionView `json:"recent"`
}

// handleProjectSessions lists, per checkout (main + each worktree), the
// sessions live there right now and the recent sessions that can be resumed.
func (s *Server) handleProjectSessions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}
	branches, err := s.deps.Worktrees.ListBranches(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	st := s.readState()

	out := []checkoutView{}
	for _, b := range branches {
		cwd := checkoutPath(path, b)
		if cwd == "" {
			continue
		}
		cv := checkoutView{Branch: b.Name, IsMain: b.IsMain, Path: cwd, Live: []liveSessionView{}, Recent: []recentSessionView{}}
		live := liveRowsAt(st, cwd)
		windowBySession := map[string]string{}
		for i, row := range live {
			windowBySession[row.SessionID] = row.WindowID
			cv.Live = append(cv.Live, liveSessionView{
				WindowID:   row.WindowID,
				WindowName: row.WindowName,
				SessionID:  row.SessionID,
				Status:     row.Status,
				Primary:    i == 0,
			})
		}
		for _, rs := range s.deps.History.RecentSessions(cwd, recentSessionLimit) {
			wid := windowBySession[rs.SessionID]
			title := rs.Title
			if title == "" {
				title = rs.Summary // first user message reads better than an ID prefix
			}
			if title == "" {
				title = rs.DisplayName()
			}
			cv.Recent = append(cv.Recent, recentSessionView{
				SessionID:  rs.SessionID,
				Title:      title,
				Summary:    rs.Summary,
				LastActive: rs.LastActive,
				Live:       rs.IsLive || wid != "",
				WindowID:   wid,
			})
		}
		out = append(out, cv)
	}
	writeJSON(w, out)
}

// handleStopSession stops the session in a window: SIGINT (SIGTERM
// fallback), then kill-window — the TUI's cleanup kill stage.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	_, sessionID, _, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	s.launchMu.Lock()
	defer s.launchMu.Unlock()

	sessions, err := s.deps.Sessions.LiveSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, sess := range sessions {
		if sess.SessionID == sessionID {
			s.deps.Sessions.StopSessions([]claude.Session{sess})
			writeJSON(w, map[string]any{})
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("session %s is no longer running", sessionID))
}

func (s *Server) readState() *state.StateFile {
	st, err := s.deps.State.Read()
	if err != nil || st == nil {
		return &state.StateFile{}
	}
	return st
}

// liveRowsAt returns the state-file rows with a live session at cwd,
// primary first (lowest Index — the TUI writes 0 for the primary).
func liveRowsAt(st *state.StateFile, cwd string) []state.ProjectState {
	var out []state.ProjectState
	for _, row := range st.Projects {
		if row.Path == cwd && row.SessionID != "" && row.WindowID != "" {
			out = append(out, row)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Index < out[j-1].Index; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// checkoutPath is where a branch is checked out: the main project path, its
// worktree, or "" when it isn't checked out anywhere.
func checkoutPath(projectPath string, b project.Branch) string {
	switch {
	case b.WorktreePath != "":
		return b.WorktreePath
	case b.IsMain:
		return projectPath
	}
	return ""
}

// windowIDFromTarget turns an ops launch target ("session:@N") into "@N".
func windowIDFromTarget(target string) string {
	if i := strings.LastIndex(target, ":"); i >= 0 {
		return target[i+1:]
	}
	return target
}
