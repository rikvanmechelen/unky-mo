package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/project"
)

// Per-prompt and per-screen key tests, possible now that each prompt and
// screen has its own handler (docs/plans/review-remediation.md, step
// 2.10). Commands are only checked for being there: running them would
// reach tmux.

func keyOf(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "tab":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	case "right":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyRight})
	case "left":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft})
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg(tea.Key{Code: r, Text: s})
}

func press(t *testing.T, m Model, k string) (Model, tea.Cmd) {
	t.Helper()
	out, cmd := m.Update(keyOf(k))
	return out.(Model), cmd
}

func TestCleanupMenuKeys(t *testing.T) {
	sessions := []claude.Session{{PID: 1}}
	cases := []struct {
		name    string
		menu    cleanupMenu
		key     string
		open    bool   // the menu is still open
		stage   string // its stage if so
		wantCmd bool
	}{
		{"kill: y stops the sessions, then asks what to remove", cleanupMenu{stage: "kill", sessions: sessions, worktreePath: "/w"}, "y", true, "action", true},
		{"kill: enter cancels", cleanupMenu{stage: "kill", sessions: sessions}, "enter", false, "", false},
		{"kill: an unrelated key does nothing", cleanupMenu{stage: "kill", sessions: sessions}, "z", true, "kill", false},
		{"worktree: w removes the worktree", cleanupMenu{stage: "action", worktreePath: "/w", branch: "b"}, "w", false, "", true},
		{"worktree: b removes worktree and branch", cleanupMenu{stage: "action", worktreePath: "/w", branch: "b"}, "B", false, "", true},
		{"worktree: enter cancels (destructive)", cleanupMenu{stage: "action", worktreePath: "/w"}, "enter", false, "", false},
		{"branch: y deletes it", cleanupMenu{stage: "action", branch: "b"}, "y", false, "", true},
		{"branch: n cancels", cleanupMenu{stage: "action", branch: "b"}, "n", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := withDetail(goldenModel(t))
			menu := c.menu
			m.cleanup = &menu
			m, cmd := press(t, m, c.key)
			if (m.cleanup != nil) != c.open || (c.open && m.cleanup.stage != c.stage) {
				t.Errorf("menu %+v, want open=%v stage %q", m.cleanup, c.open, c.stage)
			}
			if (cmd != nil) != c.wantCmd {
				t.Errorf("cmd %v, want one: %v", cmd != nil, c.wantCmd)
			}
		})
	}
}

func TestSmallPromptKeys(t *testing.T) {
	cases := []struct {
		name    string
		open    func(*Model)
		isOpen  func(Model) bool
		key     string
		stays   bool
		wantCmd bool
	}{
		{"new session: s switches", func(m *Model) { m.newSession = &newSessionPrompt{primaryWin: "alpha"} }, func(m Model) bool { return m.newSession != nil }, "s", false, true},
		{"new session: enter is switch", func(m *Model) { m.newSession = &newSessionPrompt{primaryWin: "alpha"} }, func(m Model) bool { return m.newSession != nil }, "enter", false, true},
		{"new session: p parks and launches", func(m *Model) { m.newSession = &newSessionPrompt{primaryWin: "alpha"} }, func(m Model) bool { return m.newSession != nil }, "p", false, true},
		{"new session: c launches alongside", func(m *Model) { m.newSession = &newSessionPrompt{project: "alpha"} }, func(m Model) bool { return m.newSession != nil }, "c", false, true},
		{"new session: esc cancels", func(m *Model) { m.newSession = &newSessionPrompt{} }, func(m Model) bool { return m.newSession != nil }, "esc", false, false},
		{"new session: other keys wait", func(m *Model) { m.newSession = &newSessionPrompt{} }, func(m Model) bool { return m.newSession != nil }, "z", true, false},
		{"agent picker: an agent's key launches", func(m *Model) { m.agentPicker = &agentPickerPrompt{branch: project.Branch{Name: "main", IsMain: true}} }, func(m Model) bool { return m.agentPicker != nil }, "g", false, true},
		{"agent picker: upper case too", func(m *Model) { m.agentPicker = &agentPickerPrompt{} }, func(m Model) bool { return m.agentPicker != nil }, "G", false, true},
		{"agent picker: enter cancels", func(m *Model) { m.agentPicker = &agentPickerPrompt{} }, func(m Model) bool { return m.agentPicker != nil }, "enter", false, false},
		{"import: y takes it over", func(m *Model) { m.importExternal = &importPrompt{sessionID: "s"} }, func(m Model) bool { return m.importExternal != nil }, "y", false, true},
		{"import: enter cancels (destructive)", func(m *Model) { m.importExternal = &importPrompt{sessionID: "s"} }, func(m Model) bool { return m.importExternal != nil }, "enter", false, false},
		{"suspend-quit: s suspends", func(m *Model) { m.suspendQuit = &suspendQuitPrompt{} }, func(m Model) bool { return m.suspendQuit != nil }, "s", false, true},
		{"suspend-quit: anything else cancels", func(m *Model) { m.suspendQuit = &suspendQuitPrompt{} }, func(m Model) bool { return m.suspendQuit != nil }, "y", false, false},
		{"worktree exists: f focuses it", func(m *Model) { m.worktreeExists = &worktreeExistsPrompt{branch: "feat"} }, func(m Model) bool { return m.worktreeExists != nil }, "f", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := withDetail(goldenModel(t))
			c.open(&m)
			m, cmd := press(t, m, c.key)
			if c.isOpen(m) != c.stays {
				t.Errorf("open after %q: %v, want %v", c.key, c.isOpen(m), c.stays)
			}
			if (cmd != nil) != c.wantCmd {
				t.Errorf("cmd %v, want one: %v", cmd != nil, c.wantCmd)
			}
		})
	}
}

func TestNewProjectInputKeys(t *testing.T) {
	m := goldenModel(t)
	m.newProject = &newProjectPrompt{input: *newInput(""), parentDir: "/ws"}
	m, _ = press(t, m, "d")
	m, _ = press(t, m, "x")
	if got := m.newProject.input.Value(); got != "dx" {
		t.Fatalf("typed %q", got)
	}
	m, cmd := press(t, m, "enter")
	if m.newProject != nil || cmd == nil {
		t.Errorf("enter with a name: open=%v cmd=%v", m.newProject != nil, cmd != nil)
	}
	m.newProject = &newProjectPrompt{input: *newInput("   ")}
	m, cmd = press(t, m, "enter")
	if m.newProject != nil || cmd != nil {
		t.Errorf("enter with only spaces should just close")
	}
}

func TestDashboardKeys(t *testing.T) {
	m := goldenModel(t)
	m, _ = press(t, m, "tab")
	if m.dashFocusLeft {
		t.Error("tab moves to the right panel")
	}
	m, _ = press(t, m, "left")
	if !m.dashFocusLeft {
		t.Error("left comes back")
	}
	m, _ = press(t, m, "l")
	if m.dashFocusLeft || m.dashRightFocus != dashRightSessions {
		t.Error("l goes to the sessions panel")
	}
	m, _ = press(t, m, "?")
	if m.screen != ScreenHelp {
		t.Error("? opens help")
	}
	m, _ = press(t, m, "esc")
	if m.screen != ScreenDashboard {
		t.Error("esc closes help")
	}

	// enter on an external session opens the import prompt.
	m = goldenModel(t)
	m.dashFocusLeft = false
	m.dashRightFocus = dashRightSessions
	m.dashSessionItems = []dashSessionItem{{Name: "scratch", Status: StatusExternal, ProjectPath: "/tmp/scratch"}}
	m.externalSessions = map[string]string{"/tmp/scratch": "ext-1"}
	m.externalPIDs = map[string]int{"/tmp/scratch": 42}
	m, _ = press(t, m, "enter")
	if m.importExternal == nil || m.importExternal.sessionID != "ext-1" || m.importExternal.pid != 42 {
		t.Errorf("import prompt %+v", m.importExternal)
	}

	// q with live sessions asks first.
	m = goldenModel(t)
	m.activeSessions = 1
	m, _ = press(t, m, "q")
	if m.suspendQuit == nil {
		t.Error("q with live sessions should ask to suspend")
	}

	// N without workspace dirs says so instead of opening the input.
	m = goldenModel(t)
	m, _ = press(t, m, "N")
	if m.newProject != nil || m.statusMsg == "" {
		t.Error("N without workspace dirs")
	}
	m.workspaceDirs = []string{"/ws"}
	m, _ = press(t, m, "N")
	if m.newProject == nil || m.newProject.parentDir != "/ws" {
		t.Error("N opens the new-project input")
	}
}

func TestProjectKeys(t *testing.T) {
	m := withDetail(goldenModel(t))
	m, _ = press(t, m, "tab")
	if m.detailFocusLeft {
		t.Error("tab moves to the PR panel")
	}
	m, cmd := press(t, m, "enter")
	if m.detailPRExpanded != 0 || cmd == nil {
		t.Error("enter on a PR expands it and fetches its detail")
	}
	m, _ = press(t, m, "enter")
	if m.detailPRExpanded != -1 {
		t.Error("enter again collapses it")
	}
	if _, cmd := press(t, m, "o"); cmd == nil {
		t.Error("o opens the PR")
	}
	if _, cmd := press(t, m, "c"); cmd == nil {
		t.Error("c checks the PR out")
	}
	m, _ = press(t, m, "h")
	if !m.detailFocusLeft {
		t.Error("h comes back to the branches")
	}
	m, _ = press(t, m, "h")
	if m.screen != ScreenDashboard {
		t.Error("h on the branches goes back to the dashboard")
	}

	// x on main is refused; on a worktree branch it opens the cleanup menu.
	m = withDetail(goldenModel(t))
	m.detailCursor = 0 // main
	if m2, cmd := press(t, m, "x"); m2.cleanup != nil || cmd == nil {
		t.Error("x on main should refuse with a status")
	}
	for i, r := range m.detailRows {
		if r.kind == "branch" && r.branch.Name == "feat" {
			m.detailCursor = i
		}
	}
	m, _ = press(t, m, "x")
	if m.cleanup == nil || m.cleanup.branch != "feat" || m.cleanup.stage != "action" || m.cleanup.worktreePath == "" {
		t.Errorf("cleanup %+v", m.cleanup)
	}

	// W opens the new-branch input.
	m = withDetail(goldenModel(t))
	m, _ = press(t, m, "W")
	if m.worktree == nil {
		t.Error("W opens the branch input")
	}

	// enter on a remote-only branch pulls its session.
	m = withDetail(goldenModel(t))
	for i, r := range m.detailRows {
		if r.kind == "br-remote" {
			m.detailCursor = i
			break
		}
	}
	if _, cmd := press(t, m, "enter"); cmd == nil {
		t.Error("enter on a remote-only branch pulls it")
	}
}
