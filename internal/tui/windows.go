package tui

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/notify"
)

// pruneOrphanedTermSessions kills "mo-terms-<N>" tmux sessions whose
// paired project window (@N) no longer exists. Without this, closing a
// project window leaks its per-window mo-terms session so its parked
// shells stay alive forever. Runs on every sessionTick from the main
// goroutine — tmux list/kill calls are fast enough not to stall.
func (m *Model) pruneOrphanedTermSessions() {
	if m.tmux == nil {
		return
	}
	sessions, err := m.tmux.ListSessions()
	if err != nil || len(sessions) == 0 {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	for _, name := range orphanedTermSessions(sessions, windows) {
		_ = m.tmux.KillSession(name)
	}
}

// orphanedTermSessions returns mo-terms-* session names whose suffix
// doesn't correspond to any live window. Handles two naming conventions:
//   - mo-terms-<N> (digit suffix) — legacy, keyed on window ID @N
//   - mo-terms-<hex12> (12-char hex) — instance-ID-based (new)
//
// Extracted for testability — pure function over strings/windows.
func orphanedTermSessions(sessions []string, windows []ttmux.Window) []string {
	liveWindowID := make(map[string]bool, len(windows))
	liveInstanceID := make(map[string]bool, len(windows))
	for _, w := range windows {
		if w.ID != "" {
			liveWindowID[strings.TrimPrefix(w.ID, "@")] = true
		}
		if w.InstanceID != "" {
			liveInstanceID[w.InstanceID] = true
		}
	}
	var orphans []string
	for _, s := range sessions {
		suffix, ok := strings.CutPrefix(s, "mo-terms-")
		if !ok || suffix == "" {
			continue
		}
		if allDigits(suffix) {
			// Legacy digit-suffix: paired with window @N.
			if !liveWindowID[suffix] {
				orphans = append(orphans, s)
			}
		} else if isHex12(suffix) {
			// Instance-ID-based: paired with @mo_instance_id.
			if !liveInstanceID[suffix] {
				orphans = append(orphans, s)
			}
		}
		// Other suffixes (windowName-based, etc.) are left alone.
	}
	return orphans
}

// isHex12 returns true if s is exactly 12 lowercase hex characters.
func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sessionToWindowMap returns a map from Claude session ID to the tmux window
// name currently running that session, by walking tmux panes and matching
// each live Claude PID via PPID chain. Used by buildDetailRows to attach
// real window names to br-session rows.
func (m *Model) sessionToWindowMap() map[string]string {
	result := map[string]string{}
	if m.tmux == nil {
		return result
	}
	windows, err := m.tmux.ListWindows()
	if err != nil || len(windows) == 0 {
		return result
	}
	sessions, _ := m.claude.LiveSessions()
	if len(sessions) == 0 {
		return result
	}
	for _, w := range windows {
		panePIDs, err := m.tmux.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		for i := range sessions {
			if _, already := result[sessions[i].SessionID]; already {
				continue
			}
			if m.claude.IsDescendantOf(sessions[i].PID, panePIDs) {
				result[sessions[i].SessionID] = w.Name
			}
		}
	}
	return result
}

// focusPrimaryIfLive tries to switch to the tmux window currently hosting a
// live primary session at (project, branch, cwd). Returns (attempted, realWin,
// err): `attempted=true` when a live session was found and a switch was
// attempted (err is set iff tmux switch failed), `attempted=false` when no
// live session exists at the target so callers should fall through to their
// normal launch path. Used by the launch gates to handle renamed primaries
// whose bare composed name no longer matches a tmux window.
func (m *Model) focusPrimaryIfLive(project, branch, cwd string) (bool, string, error) {
	realWin, session := m.primaryWindowForTarget(project, branch, cwd)
	if session == nil || realWin == "" {
		return false, "", nil
	}
	existed, err := m.focusIfExists(realWin)
	if !existed {
		return false, "", nil
	}
	return true, realWin, err
}

// primaryWindowForTarget returns the actual tmux window name currently
// hosting the "primary" (oldest) live Claude session at the given cwd,
// along with a pointer to that session. Returns ("", nil) when no live
// session exists there. Callers use this so renamed primaries — whose
// window name no longer matches the bare composed form — can still be
// switched to, killed, or resumed correctly.
func (m *Model) primaryWindowForTarget(project, branch, cwd string) (string, *claude.Session) {
	sessions := m.claude.SessionsForPath(cwd)
	if len(sessions) == 0 {
		return "", nil
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].StartedAt < sessions[j].StartedAt
	})
	primary := sessions[0] // copy so the returned pointer stays valid
	windowBySession := m.sessionToWindowMap()
	name, ok := windowBySession[primary.SessionID]
	if !ok {
		name = ttmux.ComposeWindowName(project, branch, "")
	}
	return name, &primary
}

// syncWindowTitles reads the latest custom-title entry for each live Claude
// session and renames the tmux window to match. Works for both primary
// (unsuffixed) and sibling windows. When a session's title is cleared, the
// window reverts to the bare project/branch name if free, or to the next
// available ordinal suffix. Skips renames that would collide with another
// existing window name.
func (m *Model) syncWindowTitles() {
	if m.tmux == nil {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	sessions, _ := m.claude.LiveSessions()
	if len(sessions) == 0 {
		return
	}
	existingNames := make(map[string]bool, len(windows))
	for _, w := range windows {
		existingNames[w.Name] = true
	}
	namesSlice := func() []string {
		out := make([]string, 0, len(existingNames))
		for n := range existingNames {
			out = append(out, n)
		}
		return out
	}
	for _, w := range windows {
		project, branch, suffix, ok := ttmux.ParseWindowName(w.Name)
		if !ok {
			continue
		}
		panePIDs, err := m.tmux.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		var sess *claude.Session
		for i := range sessions {
			if m.claude.IsDescendantOf(sessions[i].PID, panePIDs) {
				sess = &sessions[i]
				break
			}
		}
		if sess == nil {
			continue
		}
		title := m.claude.CustomTitleFor(sess.CWD, sess.SessionID)
		// Decide desired suffix:
		//   title != ""  → use the title
		//   title == ""  → revert to bare if free, else next free ordinal
		//                  (only meaningful when current suffix is non-empty)
		var desiredSuffix string
		switch {
		case title != "":
			desiredSuffix = title
		case suffix != "":
			bareName := ttmux.ComposeWindowName(project, branch, "")
			if !existingNames[bareName] {
				desiredSuffix = ""
			} else if n, err := strconv.Atoi(suffix); err == nil && n >= 2 {
				// Already an ordinal sibling — keep its slot. Recomputing
				// would count this window's own name as taken and flip it
				// between [2] and [3] on every tick.
				continue
			} else {
				desiredSuffix = ttmux.NextAvailableOrdinal(namesSlice(), project, branch)
			}
		default:
			// Title empty, already bare — nothing to do.
			continue
		}
		if desiredSuffix == suffix {
			continue
		}
		desired := ttmux.ComposeWindowName(project, branch, desiredSuffix)
		if desired == w.Name || existingNames[desired] {
			continue
		}
		if err := m.tmux.RenameWindow(w.ID, desired); err == nil {
			delete(existingNames, w.Name)
			existingNames[desired] = true
		}
	}
}

// beginRestart restarts the sidebars and quits; Run then execs the
// freshly-installed binary in place. Shared by ctrl+alt+r and the
// NotifyRestart socket message.
func (m Model) beginRestart() (tea.Model, tea.Cmd) {
	m.restartSidebars()
	m.restartRequested = true
	return m, tea.Quit
}

// restartSidebars sends ctrl+alt+r to all sidebar panes (pane .1 in each
// window) so they reload the new binary. ctrl+r alone in a sidebar is now
// a local refresh, not a restart — the restart binding moved to match the
// main TUI.
func (m Model) restartSidebars() {
	if m.tmux == nil {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	moBin := m.ops.MoBinaryPath
	sidebarWidth := m.ops.SidebarWidth
	if sidebarWidth <= 0 {
		sidebarWidth = 42
	}
	for _, w := range windows {
		if w.Index == "0" {
			continue // skip the TUI window itself
		}
		if moBin != "" && w.InstanceID != "" {
			// New path: kill the sidebar pane and respawn with the instance ID.
			// The mo-terms parking session outlives the sidebar, so terminals
			// survive the restart.
			target := fmt.Sprintf("%s:%s", m.tmux.SessionName(), w.Index)
			sidebarCmd := fmt.Sprintf("%s sidebar --instance-id=%s", moBin, w.InstanceID)
			_ = m.tmux.KillPane(target + ".1")
			if _, err := m.tmux.SplitWindow(target, sidebarWidth, w.CWD, sidebarCmd); err == nil {
				_ = m.tmux.SelectPane(target + ".0")
			}
		} else {
			// Legacy fallback: send alt+ctrl+r to the sidebar pane so its
			// handler execs the new binary.
			target := fmt.Sprintf("%s:%s.1", m.tmux.SessionName(), w.Index)
			m.tmux.SendRawKeys(target, "M-C-r")
		}
	}
}

// disableHomeRespawn turns off the respawn hooks so an intentional quit
// actually closes the window and (if it's the last one) the session.
func (m Model) disableHomeRespawn() {
	if m.rawTmux == nil {
		return
	}
	session := m.rawTmux.SessionName
	target0 := session + ":0"
	m.rawTmux.SetWindowRemainOnExit(target0, false)
	m.rawTmux.UnsetSessionHook("window-unlinked")
}

// installHomeRespawnHooks sets up two tmux safety nets on window 0:
//
//  1. remain-on-exit + pane-exited hook: if mo crashes or exits while other
//     windows exist, the pane respawns mo instead of closing. If window 0 is
//     the last window, it lets the pane die so the session ends normally.
//
//  2. session-level window-unlinked hook: if window 0 is destroyed entirely
//     (e.g. `tmux kill-window`), a replacement is created at index 0.
func installHomeRespawnHooks(tc *ttmux.Client) {
	if tc == nil {
		return
	}
	moBin, err := os.Executable()
	if err != nil {
		return
	}
	session := tc.SessionName
	target0 := session + ":0"

	// Keep window 0's pane alive after mo exits so we can respawn it.
	tc.SetWindowRemainOnExit(target0, true)

	// Pane-exited hook: if other windows exist, respawn mo; otherwise
	// turn off remain-on-exit so the pane/window die naturally.
	paneHook := fmt.Sprintf(
		`if-shell "[ $(tmux list-windows -t '%s' | wc -l) -gt 1 ]" "respawn-pane -k -t '%s' '%s'" "set-window-option -t '%s' remain-on-exit off"`,
		session, target0, moBin, target0,
	)
	tc.SetWindowHook(target0, "pane-exited", paneHook)

	// Session-level hook: if window 0 is destroyed (e.g. tmux kill-window),
	// create a replacement at index 0 and launch mo in it.
	windowUnlinkedHook := fmt.Sprintf(
		`if-shell "! tmux list-windows -t '%s' -F '##{window_index}' | grep -q '^0$'" "new-window -t '%s:0' '%s'"`,
		session, session, moBin,
	)
	tc.SetSessionHook("window-unlinked", windowUnlinkedHook)
}

// safeWindowTarget constructs a tmux target string for the given window,
// preferring the stable window ID (@N) when available. This avoids tmux
// misinterpreting dots in window names (e.g. "moma.org.cubed") as pane
// separators.
func (m Model) safeWindowTarget(name, windowID string) string {
	session := m.tmux.SessionName()
	if windowID != "" {
		return session + ":" + windowID
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return ttmux.ExactTarget(session, name)
	}
	return ttmux.SafeTarget(session, name, windows)
}

// focusIfExists switches the client to windowName if a tmux window by that
// name exists. Returns (existed, err): callers gate launch decisions on
// existed, and report err as the "failed to switch" case when it is non-nil.
// This is the single owner of the "switch if the target window exists" gate
// shared by the launch/resume/attach paths.
func (m Model) focusIfExists(windowName string) (bool, error) {
	if !m.tmux.WindowExists(windowName) {
		return false, nil
	}
	return true, m.tmux.SwitchToWindow(m.safeWindowTarget(windowName, ""))
}

// restartInPlace replaces this process with the (freshly installed) mo
// binary — same PID, same tmux pane, no nesting. The state file is left in
// place for the new process to rewrite, so sidebars don't blank in between.
func restartInPlace(ns *notify.Server) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	// Release the socket now — exec skips deferred calls, and the new
	// process binds the same path.
	if ns != nil {
		ns.Stop()
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
