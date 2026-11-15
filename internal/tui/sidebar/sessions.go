package sidebar

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	moSync "github.com/rvanmech/unky-mo/internal/sync"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
)

// resolveOwnWindowName returns the current tmux window name for this pane.
// The main TUI may rename our window at any time (custom titles, sibling
// ordinal shuffling), so callers must re-resolve on every tick rather than
// caching — otherwise the "current window" highlight goes stale.
func resolveOwnWindowName() string {
	name := ""
	if paneID := os.Getenv("TMUX_PANE"); paneID != "" {
		out, err := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{window_name}").Output()
		if err == nil {
			name = strings.TrimSpace(string(out))
		}
	}
	if name == "" {
		name = ttmux.CurrentWindowName()
	}
	return name
}

// itemMatchesOwnWindow decides whether a sidebar row belongs to the sidebar's
// own tmux window. InstanceID is the strongest key — when both sidebar and
// row have it, match on that alone. Fallback to WindowID (stable tmux @N),
// then WindowName for cold state rows (placeholder StatusNone entries and
// sessions still mid-launch).
func itemMatchesOwnWindow(item SidebarItem, ownInstanceID, ownID, ownName string) bool {
	if ownInstanceID != "" && item.InstanceID != "" {
		return item.InstanceID == ownInstanceID
	}
	if ownID != "" && item.WindowID != "" {
		return item.WindowID == ownID
	}
	return item.WindowName == ownName
}

// resolveOwnWindowID returns this pane's tmux window id (e.g. "@5"). Unlike
// the window name, the id is stable across renames, so it's the preferred
// key for matching the sidebar's own session row in the state file.
func resolveOwnWindowID() string {
	paneID := os.Getenv("TMUX_PANE")
	if paneID == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{window_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ownAgentKey returns the agent key for this sidebar's own window from the
// state file. Empty string means Claude (default).
func (m *Model) ownAgentKey(sf *state.StateFile) string {
	if sf == nil {
		return ""
	}
	if m.instanceID != "" {
		for _, p := range sf.Projects {
			if p.InstanceID == m.instanceID {
				return p.AgentKey
			}
		}
	}
	// Fallback: match by window ID or name.
	for _, p := range sf.Projects {
		if itemMatchesOwnWindow(SidebarItem{WindowName: p.WindowName, WindowID: p.WindowID, InstanceID: p.InstanceID}, m.instanceID, m.windowID, m.windowName) {
			return p.AgentKey
		}
	}
	return ""
}

// ownSessionID returns the Claude session ID for this sidebar's own window.
// When the instance ID is set, it looks up the matching state file row
// directly — no PID descent needed. Falls back to ownWindowSession for
// pre-refactor windows or stale state.
func (m *Model) ownSessionID(sf *state.StateFile) string {
	if m.instanceID != "" && sf != nil {
		for _, p := range sf.Projects {
			if p.InstanceID == m.instanceID {
				return p.SessionID
			}
		}
	}
	// Fallback: PID-descent discovery for pre-refactor windows.
	if live := m.ownWindowSession(); live != nil {
		return live.SessionID
	}
	return ""
}

// ownWindowSession returns the Claude session running in this sidebar's own
// tmux window, picked by matching session PIDs against the window's pane
// PIDs. This is what distinguishes concurrent sessions sharing a CWD — plain
// path lookup (SessionForPath) returns an arbitrary first match.
func (m *Model) ownWindowSession() *claude.Session {
	candidates := m.claude.SessionsForPath(m.windowPath)
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &candidates[0]
	}
	if m.windowName == "" || m.tmux == nil {
		return &candidates[0]
	}
	target := fmt.Sprintf("%s:%s", m.tmux.SessionName(), m.windowName)
	panePIDs, err := m.tmux.WindowPanePIDs(target)
	if err != nil || len(panePIDs) == 0 {
		return &candidates[0]
	}
	return pickOwnSession(candidates, panePIDs, m.claude.IsDescendantOf)
}

// pickOwnSession selects the session whose PID is a descendant of one of the
// window's pane PIDs. Falls back to the first candidate if no descendant
// match is found. Extracted from ownWindowSession for testability.
func pickOwnSession(candidates []claude.Session, panePIDs map[int]bool, isDescendant func(int, map[int]bool) bool) *claude.Session {
	for i := range candidates {
		if isDescendant(candidates[i].PID, panePIDs) {
			return &candidates[i]
		}
	}
	return &candidates[0]
}

func (m *Model) refreshFromSessions() {
	// Fallback: read live sessions directly
	sessions, _ := m.claude.LiveSessions()
	liveByPath := make(map[string]bool)
	for _, s := range sessions {
		liveByPath[s.CWD] = true
	}

	// Keep existing items but update statuses
	for i := range m.items {
		if m.items[i].IsHome {
			continue
		}
		if liveByPath[m.items[i].Path] {
			m.items[i].Status = "active"
		} else {
			m.items[i].Status = "none"
		}
	}
}

func (m *Model) refreshSyncStatus() {
	m.syncStatus = ""
	if m.windowName == "" || m.windowPath == "" {
		return
	}
	syncDir := moSync.DefaultSyncDir()
	// Read local sync repo metadata without network (no git pull)
	sessions, err := moSync.ListLocal(syncDir)
	if err != nil {
		return
	}
	syncName := m.syncProjectName()
	for _, s := range sessions {
		if s.ProjectName == syncName {
			// Found a synced session — check if local JSONL is newer
			localDir := m.claude.ProjectsDirForPath(m.windowPath)
			localPath := localDir + "/" + s.SessionID + ".jsonl"
			info, err := os.Stat(localPath)
			if err != nil {
				m.syncStatus = "stale"
				return
			}
			if info.ModTime().After(s.PushedAt) {
				m.syncStatus = "stale"
			} else {
				m.syncStatus = "synced"
			}
			return
		}
	}
}

// syncProjectName extracts the canonical sync project name from the tmux
// window name, stripping any suffix (session title or sibling ordinal).
// For worktree windows ("project@branch [title]") returns "project@branch".
func (m Model) syncProjectName() string {
	project, branch, _, ok := ttmux.ParseWindowName(m.windowName)
	if !ok {
		return m.windowName
	}
	if branch != "" {
		return project + "@" + branch
	}
	return project
}

func (m Model) selectedItem() *SidebarItem {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.IsHome || item.Path == "" {
		return nil
	}
	return &item
}

// statusCmd returns a tea.Cmd that sends a status message, or nil if empty.
func statusCmd(s string) tea.Cmd {
	if s == "" {
		return nil
	}
	return func() tea.Msg { return sidebarStatusMsg(s) }
}

// handleEnter dispatches enter based on whether a session or terminal is selected.
func (m *Model) handleEnter() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.IsTerminal {
		return m.switchTerminalByPaneID(item.PaneID)
	}
	return m.switchToSelected()
}

func (m Model) syncPush() tea.Cmd {
	return func() tea.Msg {
		if m.windowName == "" || m.windowPath == "" {
			return sidebarStatusMsg("no project")
		}
		live := m.claude.SessionForPath(m.windowPath)
		if live == nil {
			return sidebarStatusMsg("no live session to sync")
		}
		syncDir := moSync.DefaultSyncDir()
		syncName := m.syncProjectName()
		if err := moSync.Push(syncName, m.windowPath, syncDir, live.SessionID); err != nil {
			return sidebarStatusMsg(fmt.Sprintf("sync err: %v", err))
		}
		return sidebarStatusMsg("synced " + syncName)
	}
}

func (m Model) switchToSelected() tea.Cmd {
	return func() tea.Msg {
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return nil
		}

		item := m.items[m.cursor]

		// Teammate items: focus the teammate's pane within the same window.
		if item.IsTeammate && item.TeamPaneID != "" {
			m.tmux.SelectPane(item.TeamPaneID)
			return sidebarStatusMsg("focused " + item.TeammateName)
		}

		var target string
		switch {
		case item.IsHome:
			target = fmt.Sprintf("%s:0", m.tmux.SessionName())
		case item.WindowID != "":
			// Prefer WindowID — stable across tmux rename. Targeting a raw
			// @N always resolves to the containing window, so a sidebar click
			// succeeds even mid-rename, before the state file has caught up.
			target = item.WindowID
		case item.WindowName != "":
			// Fallback for cold rows that don't yet carry a WindowID.
			target = fmt.Sprintf("%s:%s", m.tmux.SessionName(), item.WindowName)
		default:
			return nil
		}

		m.tmux.SwitchToWindow(target)

		return nil
	}
}
