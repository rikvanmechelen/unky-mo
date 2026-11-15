package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
)

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
	// Starting is the primary window when it has no session yet.
	Starting *startingWindowView `json:"starting,omitempty"`
}

type startingWindowView struct {
	WindowID   string `json:"window_id"`
	WindowName string `json:"window_name"`
}

// windowIDByName is the id of the window called name, or "".
func (s *Server) windowIDByName(name string) string {
	windows, err := s.deps.Sessions.ListWindows()
	if err != nil {
		return ""
	}
	for _, w := range windows {
		if w.Name == name {
			return w.ID
		}
	}
	return ""
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBody)).Decode(&req); err != nil {
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
		if primary == nil {
			// A window with no session behind it: still starting, or
			// stopped on a prompt (e.g. trusting the folder). Offer to
			// open it, where the chat view shows what it's waiting on.
			if id := s.windowIDByName(primaryName); id != "" {
				resp.Error = primaryName + " has a window that hasn't started a session yet; it may be waiting on a prompt"
				resp.Choices = []string{"open", "replace", "sibling"}
				resp.Starting = &startingWindowView{WindowID: id, WindowName: primaryName}
			}
		} else {
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
