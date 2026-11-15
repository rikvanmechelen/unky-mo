package tui

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/status"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

func (m Model) refreshSessions() tea.Cmd {
	// Snapshot the project list by (path → name) so the goroutine can classify
	// without touching model state.
	projectNames := make(map[string]string)
	for _, item := range m.list.Items() {
		if pi, ok := item.(ProjectItem); ok {
			projectNames[pi.project.Path] = pi.project.Name
		}
	}
	tmuxClient := m.tmux // safe for read off-goroutine
	claudeClient := m.claude
	if claudeClient == nil {
		claudeClient = ops.NewDefaultClaudeReader()
	}
	mgr := m.statusMgr
	watcher := m.statusWatcher
	return func() tea.Msg {
		gitLookups := realGitLookups()
		// Taken before the CLI call so any hook landing during it counts as
		// fresher than this snapshot (see status.Manager.ProcessAgentStatus).
		observedAt := time.Now()
		sessions, listErr := claudeClient.LiveSessions()
		var hostPIDs map[int]bool
		if tmuxClient != nil {
			hostPIDs, _ = tmuxClient.PanePIDs()
		}
		// Resolve real window names for every live session by walking pane
		// PID chains once. Used to populate sessionView.WindowName/WindowID/Index.
		windowBySession := resolveSessionWindows(tmuxClient, claudeClient, sessions)

		// Track which sessions are still alive for watcher management.
		liveSIDs := make(map[string]bool, len(sessions))

		views := make([]sessionView, 0, len(sessions))
		externalPIDs := make(map[string]int)
		externalSessions := make(map[string]string)

		for _, s := range sessions {
			liveSIDs[s.SessionID] = true
			isExternal := len(hostPIDs) > 0 && !claudeClient.IsDescendantOf(s.PID, hostPIDs)

			var st SessionStatus
			if isExternal {
				st = StatusExternal
			} else if mgr != nil {
				mgr.ProcessAgentStatus(s.SessionID, s.Status, s.WaitingFor, observedAt)
				st = mgrStatusToTUI(mgr.Status(s.SessionID))
				// If the status manager has no state yet for this session,
				// bootstrap it via a JSONL read and start watching.
				if st == StatusNone {
					jsonlPath := filepath.Join(claudeClient.ProjectsDirForPath(s.CWD), s.SessionID+".jsonl")
					if mgr != nil {
						mgr.ProcessJSONLChange(s.SessionID, jsonlPath)
						st = mgrStatusToTUI(mgr.Status(s.SessionID))
					}
				}
			}
			// Default to Active if we still don't know.
			if st == StatusNone && !isExternal {
				st = StatusActive
			}

			// Register watcher for JSONL reconciliation.
			if watcher != nil && !isExternal {
				jsonlPath := filepath.Join(claudeClient.ProjectsDirForPath(s.CWD), s.SessionID+".jsonl")
				watcher.WatchSession(s.SessionID, jsonlPath)
			}

			v := sessionView{
				SessionID: s.SessionID,
				PID:       s.PID,
				CWD:       s.CWD,
				Status:    st,
				External:  isExternal,
			}
			if mgr != nil && (st == StatusQuestion || st == StatusPermission) {
				// A question or permission prompt the hooks didn't deliver
				// (TUI restarted while it was open, hook dropped) is read back
				// from the transcript.
				jsonlPath := filepath.Join(claudeClient.ProjectsDirForPath(s.CWD), s.SessionID+".jsonl")
				v.PendingTool, v.PendingInput, _ = mgr.RecoverPending(s.SessionID,
					func(st status.SessionStatus) (string, json.RawMessage, bool) {
						return status.ReadPendingTool(jsonlPath, st == status.StatusQuestion)
					})
			}

			classifyCWD(&v, s.CWD, projectNames, gitLookups)
			// A stray prefers the label chosen with `claude --name` / `/name`.
			if v.IsStray && s.Name != "" {
				v.ProjectName = s.Name
			}

			// Real window name: look up via session→window map; fall back to
			// composed bare/worktree name so dashboards still render even if
			// the session is mid-launch and not yet attached to any pane.
			if w, ok := windowBySession[s.SessionID]; ok {
				v.WindowName = w.Name
				v.WindowID = w.ID
				v.InstanceID = w.InstanceID
				v.AgentKey = w.AgentKey
			} else {
				v.WindowName = composeFallbackWindow(v)
			}
			v.Index = parseWindowIndex(v.WindowName)

			// Import caches: key by CWD (preserves today's dashSessionItem
			// lookup semantics, so `enter` on an External row still finds
			// the PID/sessionID).
			if isExternal {
				externalPIDs[v.CWD] = s.PID
				externalSessions[v.CWD] = s.SessionID
			}

			views = append(views, v)
		}

		// Sessions that ended without their SessionEnd hook (killed, the
		// window closed) are forgotten, and their JSONL is no longer
		// watched. Only from a listing that worked: a failed one lists
		// nobody.
		if listErr == nil && mgr != nil {
			for _, id := range mgr.Prune(liveSIDs, observedAt) {
				if watcher != nil {
					watcher.UnwatchSession(id)
				}
			}
		}

		// Claude Code can run a window's actual conversation through a
		// background-hosted process (observed on a session with auto mode
		// on: a content-less "interactive" process in the pane, plus a
		// same-cwd "background"-kind process doing the real work). Both
		// resolve to the same window via the ancestry walk above, so the
		// window would show two rows — and the web chat view would tail the
		// empty one. Drop the transcript-less row where a sibling has one.
		views = dedupeSameWindowSessions(views)

		// Second pass: non-Claude agent windows (see agentWindowViews).
		if tmuxClient != nil {
			if windows, err := tmuxClient.ListWindows(); err == nil {
				views = append(views, agentWindowViews(windows, views, projectNames, gitLookups)...)
			}
		}

		// Third pass: detect Claude Code agent teams. Reads
		// ~/.claude/teams/*/config.json and enriches lead sessions
		// with teammate pane info from their tmux window.
		enrichViewsWithTeamInfo(views, tmuxClient)

		return sessionRefreshMsg{
			views:            views,
			externalPIDs:     externalPIDs,
			externalSessions: externalSessions,
		}
	}
}

// dedupeSameWindowSessions drops transcript-less sessionViews that share a
// tmux window with a session that does have a transcript.
//
// `claude agents --json` can report more than one live process for a single
// pane: observed with a same-cwd "background"-kind session doing the real
// work alongside an "interactive" one that never writes its own JSONL. Both
// resolve to the same window via the PID-ancestry walk, so the window gets
// two rows — and the web chat view, which tails the row's transcript, picks
// the empty one and renders nothing.
//
// The rule is deliberately narrow: a row is only dropped when it has no
// transcript AND a sibling on the same window has one. Several real
// sessions legitimately share a window — an agent team's teammates run as
// panes in the lead's window, each with its own session ID and transcript
// (see enrichViewsWithTeamInfo) — and those must all survive.
func dedupeSameWindowSessions(views []sessionView) []sessionView {
	// Which windows have at least one transcript-bearing session?
	hasReal := make(map[string]bool)
	transcript := make([]bool, len(views))
	for i, v := range views {
		if v.Section != "projects" || v.WindowID == "" {
			continue
		}
		transcript[i] = sessionHasTranscript(v)
		if transcript[i] {
			hasReal[v.WindowID] = true
		}
	}
	if len(hasReal) == 0 {
		return views
	}
	out := views[:0]
	for i, v := range views {
		if v.Section == "projects" && v.WindowID != "" && !transcript[i] && hasReal[v.WindowID] {
			continue // a sibling on this window carries the real conversation
		}
		out = append(out, v)
	}
	return out
}

// knownProjectPath returns the ProjectItem name for cwd if cwd is a known
// project path, or "" otherwise.
func knownProjectPath(projectNames map[string]string, cwd string) string {
	return projectNames[cwd]
}

// worktreeParent splits a ".worktrees/<branch>" cwd into (parentPath,
// parentName, branch). parentName is "" when the parent project isn't in
// the projectNames map (rare; orphaned worktree).
func worktreeParent(cwd string, projectNames map[string]string) (string, string, string) {
	idx := strings.Index(cwd, ".worktrees/")
	if idx < 0 {
		return "", "", ""
	}
	parentPath := cwd[:idx+len(".worktrees/")-1]
	parentPath = strings.TrimSuffix(parentPath, ".worktrees")
	branch := filepath.Base(cwd)
	return parentPath, projectNames[parentPath], branch
}

// enrichViewsWithTeamInfo calls ops.ListTeams to detect Claude Code agent
// teams and annotates lead session views with teammate pane information.
func enrichViewsWithTeamInfo(views []sessionView, tmuxClient ops.TmuxClient) {
	// Build the sessionID → windowID map that ListTeams needs.
	sessionWindows := make(map[string]string)
	viewBySession := make(map[string]int)
	for i, v := range views {
		if v.SessionID != "" {
			viewBySession[v.SessionID] = i
			if v.WindowID != "" {
				sessionWindows[v.SessionID] = v.WindowID
			}
		}
	}

	teams, err := ops.ListTeams(tmuxClient, sessionWindows)
	if err != nil || len(teams) == 0 {
		return
	}

	for _, ts := range teams {
		idx, ok := viewBySession[ts.LeadSession]
		if !ok {
			continue
		}
		views[idx].TeamName = ts.Name
		views[idx].TeamRole = "lead"
		for _, tm := range ts.Teammates {
			views[idx].Teammates = append(views[idx].Teammates, teammateView{
				Name:   tm.Name,
				Status: tm.Status,
				PaneID: tm.PaneID,
			})
		}
	}
}

// resolveSessionWindows mirrors sessionToWindowMap but takes the sessions as
// a pre-fetched argument so it runs cleanly off-goroutine. Returns the full
// Window so callers get both the (mutable) name and the stable window id.
func resolveSessionWindows(tc ops.TmuxClient, cr ops.ClaudeReader, sessions []claude.Session) map[string]ttmux.Window {
	result := map[string]ttmux.Window{}
	if tc == nil || len(sessions) == 0 {
		return result
	}
	if cr == nil {
		cr = ops.NewDefaultClaudeReader()
	}
	windows, err := tc.ListWindows()
	if err != nil || len(windows) == 0 {
		return result
	}
	for _, w := range windows {
		panePIDs, err := tc.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		for i := range sessions {
			if _, already := result[sessions[i].SessionID]; already {
				continue
			}
			if cr.IsDescendantOf(sessions[i].PID, panePIDs) {
				result[sessions[i].SessionID] = w
			}
		}
	}
	return result
}

// composeFallbackWindow returns the bare composed tmux window name for a
// view when the session's real window can't be resolved via PID chain
// (usually because the session is mid-launch).
func composeFallbackWindow(v sessionView) string {
	if v.IsStray {
		return v.ProjectName
	}
	if v.IsWorktree {
		branch := strings.TrimPrefix(v.ProjectName, "@")
		return ttmux.ComposeWindowName(v.Parent, branch, "")
	}
	return ttmux.ComposeWindowName(v.ProjectName, "", "")
}

// parseWindowIndex returns the sibling ordinal from a window name's
// [suffix]: 0 for a bare window, N for "[N]", -1 for a custom-title suffix
// like "[foo]". Stray windows (which don't follow the project/branch
// naming) return 0 too.
func parseWindowIndex(name string) int {
	_, _, suffix, ok := ttmux.ParseWindowName(name)
	if !ok || suffix == "" {
		return 0
	}
	if n, err := strconv.Atoi(suffix); err == nil {
		return n
	}
	return -1
}

// gitLookups are the git reads classifyCWD needs, injected so the
// classification can be tested without repositories.
type gitLookups struct {
	root   func(dir string) string
	status func(root string) project.GitStatus
}

func realGitLookups() gitLookups {
	return gitLookups{root: project.FindGitRoot, status: project.GetGitStatus}
}

// classifyCWD places a session (or agent window) running in cwd: in a known
// project (its main checkout), under a known project as a worktree
// (<project>.worktrees/<branch>), as a stray in another git repo (with its
// branch and dirty count), or as external, outside any repo. A cwd inside a
// known project's repo but not its root is attributed to the project.
func classifyCWD(v *sessionView, cwd string, projectNames map[string]string, git gitLookups) {
	v.Section = "projects"
	switch {
	case knownProjectPath(projectNames, cwd) != "":
		v.ProjectPath = cwd
		v.ProjectName = knownProjectPath(projectNames, cwd)

	case strings.Contains(cwd, ".worktrees/"):
		// ProjectPath is the parent project's path, so dashboard ordering
		// groups the worktree under it; the real cwd stays in v.CWD.
		parentPath, parentName, branch := worktreeParent(cwd, projectNames)
		v.ProjectPath = parentPath
		v.ProjectName = "@" + branch
		v.Parent = parentName
		v.IsWorktree = true

	default:
		repoRoot := git.root(cwd)
		switch {
		case repoRoot == "":
			v.ProjectPath = cwd
			v.ProjectName = filepath.Base(cwd)
			v.Section = "external"
			v.IsStray = true
		case projectNames[repoRoot] != "":
			v.ProjectPath = repoRoot
			v.ProjectName = projectNames[repoRoot]
		default:
			v.ProjectPath = repoRoot
			v.ProjectName = filepath.Base(repoRoot)
			v.IsStray = true
			if gs := git.status(repoRoot); gs.Branch != "" {
				v.Branch = gs.Branch
				v.Dirty = gs.Dirty
			}
		}
	}
}

// agentWindowViews finds non-Claude agent windows (their @mo_agent option
// names another agent): they don't appear in `claude agents`, so they're
// discovered from the window list. Windows a Claude session already
// claimed, and window 0 (the TUI), are skipped.
func agentWindowViews(windows []ttmux.Window, claimed []sessionView, projectNames map[string]string, git gitLookups) []sessionView {
	taken := make(map[string]bool)
	for _, v := range claimed {
		if v.WindowID != "" {
			taken[v.WindowID] = true
		}
	}
	var out []sessionView
	for _, w := range windows {
		if w.AgentKey == "" || w.AgentKey == "c" || taken[w.ID] || w.Index == "0" {
			continue
		}
		v := sessionView{
			WindowName: w.Name,
			WindowID:   w.ID,
			InstanceID: w.InstanceID,
			AgentKey:   w.AgentKey,
			CWD:        w.CWD,
			Status:     StatusActive,
		}
		classifyCWD(&v, w.CWD, projectNames, git)
		v.Index = parseWindowIndex(v.WindowName)
		out = append(out, v)
	}
	return out
}
