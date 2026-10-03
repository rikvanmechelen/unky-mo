package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
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

// launchRequest is everything the browser may say about a launch. There is
// deliberately no path or command field: the cwd is resolved from the
// configured project + branch, and the command from the configured agent,
// since the launch ends in `send-keys "exec <cmd>"` in a real shell.
type launchRequest struct {
	Branch   string `json:"branch"`
	ResumeID string `json:"resume_id"`
	Agent    string `json:"agent"`
	// Mode resolves a busy primary window, mirroring the TUI's `n` menu:
	// "switch" (use the running session), "replace" (park it: SIGINT +
	// kill-window, then launch in its place), "sibling" (run alongside).
	Mode string `json:"mode"`
	// Focus switches the attached tmux client to the target window too.
	// Off by default: whoever is in the browser usually isn't at the
	// terminal, and jumping its view around would be a surprise.
	Focus bool `json:"focus"`
}

type launchResponse struct {
	WindowID string `json:"window_id"`
}

// conflictResponse is the 409 body when the checkout's primary window is
// busy and no Mode was given — the browser renders it as the TUI's
// switch / park+new / concurrent menu and re-posts with the chosen mode.
type conflictResponse struct {
	Error   string           `json:"error"`
	Choices []string         `json:"choices"`
	Primary *liveSessionView `json:"primary"`
}

// handleLaunch starts or resumes a session on a project checkout. Same
// semantics as the TUI's `n` key and enter on a br-session row.
func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	switch req.Mode {
	case "", "switch", "replace", "sibling":
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown mode %q", req.Mode))
		return
	}
	if req.ResumeID != "" && !sessionIDPattern.MatchString(req.ResumeID) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid session id"))
		return
	}
	agent := s.defaultAgent()
	if req.Agent != "" {
		agent = s.agentByKey(req.Agent)
		if agent == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown agent %q", req.Agent))
			return
		}
	}
	if agent == nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("no agents configured"))
		return
	}

	// One launch at a time: two quick clicks must not both see the primary
	// window as free and race to create it.
	s.launchMu.Lock()
	defer s.launchMu.Unlock()

	branches, err := s.deps.Worktrees.ListBranches(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var checkout *project.Branch
	for i := range branches {
		if branches[i].Name == req.Branch {
			checkout = &branches[i]
			break
		}
	}
	if checkout == nil || checkoutPath(path, *checkout) == "" {
		s.launchWorktree(w, name, path, req, agent)
		return
	}
	cwd := checkoutPath(path, *checkout)
	branchName := ""
	if !checkout.IsMain {
		branchName = checkout.Name
	}

	if req.ResumeID != "" && !s.deps.History.TranscriptExists(cwd, req.ResumeID) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no session %s in this checkout", req.ResumeID))
		return
	}

	st := s.readState()
	live := liveRowsAt(st, cwd)
	// Resuming a session that's already running: just hand back its window.
	if req.ResumeID != "" {
		for _, row := range live {
			if row.SessionID == req.ResumeID {
				s.respondWindow(w, row.WindowID, req.Focus)
				return
			}
		}
	}

	shellCmd := ops.AgentShellCmd(agent, req.ResumeID)
	primaryName := ttmux.ComposeWindowName(name, branchName, "")
	var primary *state.ProjectState
	if len(live) > 0 {
		primary = &live[0]
	}

	if primary == nil && !s.deps.Sessions.WindowExists(primaryName) {
		s.respondLaunch(w)(s.deps.Sessions.Launch(ops.LaunchParams{
			WindowName:    primaryName,
			Cwd:           cwd,
			ShellCmd:      shellCmd,
			AgentKey:      agent.Key,
			AttachSidebar: true,
			SwitchFocus:   req.Focus,
		}))
		return
	}

	switch req.Mode {
	case "":
		resp := conflictResponse{
			Error:   "a session is already running in " + primaryName,
			Choices: []string{"replace", "sibling"},
		}
		if primary != nil {
			resp.Error = "a session is already running in " + primary.WindowName
			resp.Choices = []string{"switch", "replace", "sibling"}
			resp.Primary = &liveSessionView{
				WindowID:   primary.WindowID,
				WindowName: primary.WindowName,
				SessionID:  primary.SessionID,
				Status:     primary.Status,
				Primary:    true,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(resp)
	case "switch":
		if primary == nil {
			writeError(w, http.StatusConflict, fmt.Errorf("no running session to switch to"))
			return
		}
		s.respondWindow(w, primary.WindowID, req.Focus)
	case "replace":
		windowName, pid := primaryName, 0
		if primary != nil {
			windowName = primary.WindowName
			pid = s.livePID(primary.SessionID)
		}
		s.respondLaunch(w)(s.deps.Sessions.ParkAndLaunch(ops.ParkParams{
			PID:               pid,
			PrimaryWindowName: windowName,
			Cwd:               cwd,
			ResumeID:          req.ResumeID,
			ShellCmd:          shellCmd,
			AgentKey:          agent.Key,
			NoSwitch:          !req.Focus,
		}))
	case "sibling":
		s.respondLaunch(w)(s.deps.Sessions.LaunchSibling(ops.SiblingParams{
			ProjectName: name,
			Branch:      branchName,
			Cwd:         cwd,
			ResumeID:    req.ResumeID,
			ShellCmd:    shellCmd,
			AgentKey:    agent.Key,
			NoSwitch:    !req.Focus,
		}))
	}
}

// launchWorktree handles a branch with no checkout yet: create the worktree
// (and the branch, if it doesn't exist) and launch in it — the TUI's `w`/`W`.
func (s *Server) launchWorktree(w http.ResponseWriter, projectName, projectPath string, req launchRequest, agent *config.AgentConfig) {
	if err := validateBranchName(req.Branch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ResumeID != "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("branch %q has no checkout to resume in", req.Branch))
		return
	}
	res, err := s.deps.Sessions.CreateWorktreeAndLaunch(ops.WorktreeParams{
		ProjectName: projectName,
		ProjectPath: projectPath,
		Branch:      req.Branch,
		ShellCmd:    ops.AgentShellCmd(agent, ""),
		AgentKey:    agent.Key,
		NoSwitch:    !req.Focus,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if res.ExistsConflict {
		writeError(w, http.StatusConflict, errors.New(res.Status))
		return
	}
	if res.Target != "" {
		writeJSON(w, launchResponse{WindowID: windowIDFromTarget(res.Target)})
		return
	}
	// A window for the branch already existed; find its ID in the state file.
	for _, row := range s.readState().Projects {
		if row.WindowName == res.WindowName && row.WindowID != "" {
			writeJSON(w, launchResponse{WindowID: row.WindowID})
			return
		}
	}
	writeError(w, http.StatusConflict, fmt.Errorf("%s", res.Status))
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

func (s *Server) respondLaunch(w http.ResponseWriter) func(*ops.LaunchResult, error) {
	return func(res *ops.LaunchResult, err error) {
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, launchResponse{WindowID: windowIDFromTarget(res.Target)})
	}
}

func (s *Server) respondWindow(w http.ResponseWriter, windowID string, focus bool) {
	if focus {
		if err := s.deps.Sessions.SwitchToWindow(s.tmuxSession + ":" + windowID); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("switch window: %w", err))
			return
		}
	}
	writeJSON(w, launchResponse{WindowID: windowID})
}

func (s *Server) livePID(sessionID string) int {
	sessions, err := s.deps.Sessions.LiveSessions()
	if err != nil {
		return 0
	}
	for _, sess := range sessions {
		if sess.SessionID == sessionID {
			return sess.PID
		}
	}
	return 0
}

func (s *Server) defaultAgent() *config.AgentConfig {
	cfg := config.Config{Agents: s.deps.Agents}
	return cfg.DefaultAgent()
}

func (s *Server) agentByKey(key string) *config.AgentConfig {
	cfg := config.Config{Agents: s.deps.Agents}
	return cfg.AgentByKey(key)
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

// validateBranchName applies git's ref-name rules (git check-ref-format)
// closely enough to reject anything git would, plus a leading "-" so a
// branch can never be read as a flag by git or tmux.
func validateBranchName(name string) error {
	bad := func() error { return fmt.Errorf("invalid branch name %q", name) }
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return bad()
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad()
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return bad()
		}
	}
	return nil
}
