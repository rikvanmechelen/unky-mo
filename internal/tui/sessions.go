package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/status"
)

// dashSessionItem represents an active session in the dashboard's right panel.
type dashSessionItem struct {
	Name        string
	WindowName  string
	WindowID    string // stable tmux window ID (@N); used for safe target construction
	AgentKey    string // coding agent mnemonic; empty = default (Claude)
	Status      SessionStatus
	ProjectPath string             // cwd — used to resolve external PID/sessionID on import
	Section     string             // "projects" (default) or "external"
	Git         *project.GitStatus // set for git-backed strays; nil otherwise
}

// refreshDashSessions rebuilds the active sessions list for the dashboard
// panel by walking m.sessionViews. Unlike the state file, the dashboard
// does not emit placeholders for projects without live sessions — only
// projects that actually have a session get a row.
//
// Ordering: ProjectItem order drives the top-level grouping. Within each
// project, main-checkout sessions render first (primary then siblings),
// then each worktree's sessions. Strays (git-backed) follow the projects
// section; non-git strays go in the external section.
func (m *Model) refreshDashSessions() {
	mainByProject := map[string][]sessionView{}
	worktreeByKey := map[string][]sessionView{}
	var stray, external []sessionView

	for _, v := range m.sessionViews {
		if v.IsStray {
			if v.Section == "external" {
				external = append(external, v)
			} else {
				stray = append(stray, v)
			}
			continue
		}
		if v.IsWorktree {
			key := v.ProjectPath + "|" + strings.TrimPrefix(v.ProjectName, "@")
			worktreeByKey[key] = append(worktreeByKey[key], v)
			continue
		}
		mainByProject[v.ProjectPath] = append(mainByProject[v.ProjectPath], v)
	}
	for k := range mainByProject {
		sortViewsForDisplay(mainByProject[k])
	}
	for k := range worktreeByKey {
		sortViewsForDisplay(worktreeByKey[k])
	}

	var items []dashSessionItem
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		for _, v := range mainByProject[pi.project.Path] {
			items = append(items, viewToDashItem(v))
		}

		branchesSeen := map[string]bool{}
		var branchOrder []string
		for key := range worktreeByKey {
			if !strings.HasPrefix(key, pi.project.Path+"|") {
				continue
			}
			branch := strings.TrimPrefix(key, pi.project.Path+"|")
			if !branchesSeen[branch] {
				branchesSeen[branch] = true
				branchOrder = append(branchOrder, branch)
			}
		}
		sort.Strings(branchOrder)
		for _, branch := range branchOrder {
			for _, v := range worktreeByKey[pi.project.Path+"|"+branch] {
				items = append(items, viewToDashItem(v))
			}
		}
	}

	// Strays: git-backed go into projects with git info, non-git into
	// external. ProjectPath is always the session's CWD so the import flow
	// can find the right PID/sessionID (maps are CWD-keyed) and launch the
	// new tmux window at exactly where claude was running.
	for _, v := range stray {
		items = append(items, viewToDashItem(v))
	}
	for _, v := range external {
		items = append(items, viewToDashItem(v))
	}

	m.dashSessionItems = items
	if m.dashSessionCursor >= len(items) {
		m.dashSessionCursor = max(0, len(items)-1)
	}
}

// viewToDashItem converts one sessionView into a dashSessionItem. Name
// keeps the real tmux window name so the sidebar + dashboard show the same
// label (including custom-title suffixes).
func viewToDashItem(v sessionView) dashSessionItem {
	item := dashSessionItem{
		Name:        v.WindowName,
		WindowName:  v.WindowName,
		WindowID:    v.WindowID,
		AgentKey:    v.AgentKey,
		Status:      v.Status,
		ProjectPath: v.CWD,
		Section:     v.Section,
	}
	if v.IsStray && v.Branch != "" {
		gs := project.GitStatus{Branch: v.Branch, Dirty: v.Dirty}
		item.Git = &gs
	}
	return item
}

// sessionView is one row per live Claude session — the unit every consumer
// (dashboard, state file, sidebar, notification overrides) actually needs.
// Built once per refresh in the refreshSessions goroutine and stashed on
// the model; rendered by refreshDashSessions + writeStateFile.
//
// Classification by Section + Parent + IsStray:
//   - Known project, main checkout:   Section="projects", Parent="",        IsStray=false
//   - Worktree of known project:      Section="projects", Parent=parent,    IsStray=false, IsWorktree=true
//   - Stray inside a git repo:        Section="projects", Parent="",        IsStray=true
//   - Stray outside any git repo:     Section="external", Parent="",        IsStray=true
//
// For concurrent siblings sharing a target, WindowName and Index disambiguate.
type sessionView struct {
	SessionID   string
	PID         int
	CWD         string
	ProjectPath string        // stable key for aggregation (=cwd, worktree parent, or stray repo-root/cwd)
	ProjectName string        // display name on the row
	Parent      string        // parent project name for worktree/sibling grouping; "" otherwise
	WindowName  string        // real tmux window name from sessionToWindowMap; composed fallback if unresolved
	WindowID    string        // stable tmux window id (e.g. "@5"); empty when window couldn't be resolved
	InstanceID  string        // mo-generated instance ID (from @mo_instance_id window option); empty for pre-refactor windows
	AgentKey    string        // coding agent mnemonic (from @mo_agent window option); empty = default
	Index       int           // 0 bare, 2+ ordinal, -1 custom-title (for ordering siblings)
	Status      SessionStatus // raw status from poll (notif overrides applied in updateProjectStatuses)
	Section     string        // "projects" | "external"
	Branch      string        // git-backed strays only
	Dirty       int           // git-backed strays only
	IsStray     bool
	IsWorktree  bool
	External    bool // PID not descendant of mo tmux panes (StatusExternal)

	// Set only while Status is StatusQuestion or StatusPermission: the tool
	// call Claude is blocked on (may be empty, see status.Manager.Pending).
	PendingTool  string
	PendingInput json.RawMessage

	// Team fields — populated when session is part of a Claude Code agent team.
	TeamName  string         // team name from ~/.claude/teams/{name}/config.json
	TeamRole  string         // "lead" or "teammate"
	Teammates []teammateView // populated for leads only
}

// teammateView describes a teammate pane within a team lead's window.
type teammateView struct {
	Name   string
	Status string // "active" (pane alive), "idle" (pane dead or gone)
	PaneID string // tmux pane ID for focus switching
}

// sessionHasTranscript reports whether v's session has written a non-empty
// JSONL transcript.
func sessionHasTranscript(v sessionView) bool {
	if v.SessionID == "" || v.CWD == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(claude.ProjectsDirForPath(v.CWD), v.SessionID+".jsonl"))
	return err == nil && info.Size() > 0
}

func (m *Model) updateProjectStatuses(polled sessionRefreshMsg) {
	items := m.list.Items()

	// Stash external-session metadata so the import prompt can reach it later.
	m.externalPIDs = polled.externalPIDs
	m.externalSessions = polled.externalSessions

	// Status manager is the source of truth — no notification overrides needed.
	views := polled.views
	m.sessionViews = views

	// Aggregate per project path for the left-column ProjectItem dot.
	// Priority ranking: Permission > Idle > Active > External > None.
	projectAgg := make(map[string]SessionStatus)
	for _, v := range views {
		if v.IsStray {
			continue // strays don't color a ProjectItem
		}
		key := v.ProjectPath
		if v.IsWorktree {
			// Worktree sessions roll up into their parent project's dot.
			// ProjectPath already points at the parent path for worktrees.
		}
		if rank(v.Status) > rank(projectAgg[key]) {
			projectAgg[key] = v.Status
		}
	}

	// Count totals for the title bar.
	activeCount := 0
	attentionCount := 0
	for _, v := range views {
		switch v.Status {
		case StatusExternal:
			// External sessions aren't "mo sessions" — don't count as active.
		case StatusIdle, StatusPermission:
			activeCount++
			attentionCount++
		case StatusActive:
			activeCount++
		}
	}

	for i, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		if st, ok := projectAgg[pi.project.Path]; ok {
			pi.status = st
		} else {
			pi.status = StatusNone
		}
		items[i] = pi
	}

	m.activeSessions = activeCount
	m.attentionCount = attentionCount
	m.list.SetItems(items)
	m.refreshDashSessions()
	m.syncWindowTitles()
	m.writeStateFile()
}

// rank orders SessionStatus values for project-level aggregation.
// Higher = takes priority when a project has multiple sessions.
func rank(s SessionStatus) int {
	switch s {
	case StatusPermission:
		return 4
	case StatusIdle:
		return 3
	case StatusActive:
		return 2
	case StatusExternal:
		return 1
	default:
		return 0
	}
}

// mergeRefreshedProjects takes the existing list items and a freshly-scanned
// project slice, and returns a merged item list. Existing projects keep their
// session status and git status; new projects enter with StatusNone; projects
// no longer in the scan are dropped. The result is sorted by project name.
func mergeRefreshedProjects(existing []list.Item, refreshed []project.Project) []list.Item {
	// Index existing items by path for O(1) lookup.
	byPath := make(map[string]ProjectItem, len(existing))
	for _, item := range existing {
		if pi, ok := item.(ProjectItem); ok {
			byPath[pi.project.Path] = pi
		}
	}

	items := make([]list.Item, 0, len(refreshed))
	for _, p := range refreshed {
		if old, ok := byPath[p.Path]; ok {
			// Preserve status + git, but pick up updated project metadata
			// (name, language, tags) from the fresh scan.
			old.project = p
			items = append(items, old)
		} else {
			items = append(items, ProjectItem{project: p, status: StatusNone})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].(ProjectItem).project.Name < items[j].(ProjectItem).project.Name
	})
	return items
}

func statusToString(s SessionStatus) string {
	switch s {
	case StatusActive:
		return state.StatusActive
	case StatusIdle:
		return state.StatusIdle
	case StatusPermission:
		return state.StatusPermission
	case StatusQuestion:
		return state.StatusQuestion
	case StatusExternal:
		return state.StatusExternal
	default:
		return state.StatusNone
	}
}

func (m *Model) routeNotificationToStatusMgr(n notify.Notification) {
	if m.statusMgr == nil {
		return
	}
	evt, ok := hookEventFromNotification(n)
	if !ok {
		return
	}
	switch evt.Type {
	case status.EventNotificationIdle:
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs input", filepath.Base(evt.ProjectPath)))
	case status.EventNotificationPerm:
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs permission", filepath.Base(evt.ProjectPath)))
	}
	m.statusMgr.ProcessHookEvent(evt)
}

// hookEventFromNotification maps a socket notification to a status hook
// event. V2 messages (status-hook.sh's hook_event_name format — every event
// type, carrying Claude's own payload) go through status.ParseHookPayload;
// the three legacy notification types map directly. ok is false for anything
// that isn't a status event.
func hookEventFromNotification(n notify.Notification) (status.HookEvent, bool) {
	if n.Type == notify.NotifyHookEvent {
		evt, err := status.ParseHookPayload(n.Raw)
		if err != nil || evt.SessionID == "" || evt.SessionID == "unknown" {
			return status.HookEvent{}, false
		}
		return evt, true
	}
	key := n.SessionID
	if key == "" {
		key = n.ProjectPath
	}
	evt := status.HookEvent{SessionID: key, ProjectPath: n.ProjectPath}
	switch n.Type {
	case notify.NotifyIdlePrompt:
		evt.Type = status.EventNotificationIdle
	case notify.NotifyPermissionPrompt:
		evt.Type = status.EventNotificationPerm
	case notify.NotifySessionStop:
		evt.Type = status.EventStop
	default:
		return status.HookEvent{}, false
	}
	return evt, true
}

// mgrStatusToTUI converts a status.SessionStatus to the TUI's SessionStatus.
func mgrStatusToTUI(s status.SessionStatus) SessionStatus {
	switch s {
	case status.StatusActive:
		return StatusActive
	case status.StatusIdle:
		return StatusIdle
	case status.StatusPermission:
		return StatusPermission
	case status.StatusQuestion:
		return StatusQuestion
	case status.StatusExternal:
		return StatusExternal
	default:
		return StatusNone
	}
}
