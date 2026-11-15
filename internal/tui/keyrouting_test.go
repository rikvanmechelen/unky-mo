package tui

import (
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/project"
)

// The key-press arm of Update checks the open prompts one after another,
// and that order is their priority: when two are open at once, the first
// one takes the key. This test records today's order so splitting Update
// (docs/plans/review-remediation.md, step 2.3) can't change it silently.

type routedPrompt struct {
	name   string
	family string // prompts of one flow clear each other, so pairs within a family aren't compared
	open   func(m *Model)
	isOpen func(m Model) bool
}

func routedPrompts() []routedPrompt {
	input := func() *textinput.Model { ti := textinput.New(); ti.Focus(); return &ti }
	return []routedPrompt{
		{"newProjectInput", "newProject",
			func(m *Model) { m.newProject = &newProjectPrompt{input: *input(), parentDir: "/ws"} },
			func(m Model) bool { return m.newProject != nil }},
		{"worktreeInput", "worktree",
			func(m *Model) { m.worktree = &worktreePrompt{input: *input()} },
			func(m Model) bool { return m.worktree != nil }},
		{"liftSessionInput", "lift",
			func(m *Model) { m.lift = &liftPrompt{input: *input()} },
			func(m Model) bool { return m.lift != nil && !m.lift.dirty }},
		{"pendingLiftDirty", "lift",
			func(m *Model) { m.lift = &liftPrompt{dirty: true, branch: "x"} },
			func(m Model) bool { return m.lift != nil && m.lift.dirty }},
		{"pendingNewMenu", "newSession",
			func(m *Model) { m.newSession = &newSessionPrompt{primaryWin: "alpha"} },
			func(m Model) bool { return m.newSession != nil }},
		{"pendingCleanup", "cleanup",
			func(m *Model) { m.cleanup = &cleanupMenu{stage: "action", branch: "b"} },
			func(m Model) bool { return m.cleanup != nil }},
		{"pendingWTExists", "wtExists",
			func(m *Model) { m.worktreeExists = &worktreeExistsPrompt{branch: "b"} },
			func(m Model) bool { return m.worktreeExists != nil }},
		{"pendingAgentPicker", "agentPicker",
			func(m *Model) { m.agentPicker = &agentPickerPrompt{branch: project.Branch{Name: "b"}} },
			func(m Model) bool { return m.agentPicker != nil }},
		{"pendingSuspendQuit", "suspend",
			func(m *Model) { m.suspendQuit = &suspendQuitPrompt{} },
			func(m Model) bool { return m.suspendQuit != nil }},
		{"pendingImport", "import",
			func(m *Model) { m.importExternal = &importPrompt{sessionID: "s", project: "p"} },
			func(m Model) bool { return m.importExternal != nil }},
	}
}

func escKey() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}) }

func pressKey(m Model, k tea.KeyPressMsg) Model {
	out, _ := m.Update(k)
	return out.(Model)
}

// TestPromptPriorityOrder opens every pair of prompts on the project
// screen (where all of them can show) and presses esc: the earlier one in
// the list must close, the later one must stay open.
func TestPromptPriorityOrder(t *testing.T) {
	prompts := routedPrompts()
	for i, hi := range prompts {
		for _, lo := range prompts[i+1:] {
			if hi.family == lo.family {
				continue
			}
			t.Run(hi.name+"_over_"+lo.name, func(t *testing.T) {
				m := withDetail(goldenModel(t))
				hi.open(&m)
				lo.open(&m)
				m = pressKey(m, escKey())
				if hi.isOpen(m) {
					t.Errorf("%s should have taken esc and closed", hi.name)
				}
				if !lo.isOpen(m) {
					t.Errorf("%s should still be open (it ranks below %s)", lo.name, hi.name)
				}
			})
		}
	}
}

// TestPromptsBeatGlobalKeys: an open prompt swallows the keys the screen
// would otherwise act on (here ?, which opens help).
func TestPromptsBeatGlobalKeys(t *testing.T) {
	for _, p := range routedPrompts() {
		if p.name == "pendingSuspendQuit" {
			continue // any key but s cancels it, ? included; checked below
		}
		t.Run(p.name, func(t *testing.T) {
			m := withDetail(goldenModel(t))
			p.open(&m)
			m = pressKey(m, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
			if m.screen == ScreenHelp {
				t.Errorf("? reached the help toggle with %s open", p.name)
			}
			if !p.isOpen(m) && p.family != "newProject" && p.family != "worktree" && p.family != "lift" {
				// Menus ignore an unknown key; inputs type it. Either way the prompt stays.
				t.Errorf("%s closed on an unrelated key", p.name)
			}
		})
	}
	t.Run("pendingSuspendQuit", func(t *testing.T) {
		m := goldenModel(t)
		m.suspendQuit = &suspendQuitPrompt{}
		m = pressKey(m, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
		if m.suspendQuit != nil || m.screen == ScreenHelp {
			t.Errorf("? should cancel suspend-quit and do nothing else")
		}
	})
}

// TestScreenGatedPrompts: the worktree and lift inputs only take keys on
// the project screen; elsewhere the key goes on down the chain.
func TestScreenGatedPrompts(t *testing.T) {
	m := goldenModel(t)
	m.worktree = &worktreePrompt{input: textinput.New()}
	m = pressKey(m, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	if m.screen != ScreenHelp {
		t.Errorf("a worktree input left open off the project screen must not take keys")
	}
}

// TestTicketPickerFirst: on the ticket screen the picker outranks every
// prompt, including the new-project input.
func TestTicketPickerFirst(t *testing.T) {
	m := goldenModel(t)
	m.screen = ScreenTicket
	m.startProjectPicker("jira", "OP")
	m.newProject = &newProjectPrompt{input: textinput.New()}
	m = pressKey(m, escKey())
	if m.newProject == nil {
		t.Errorf("esc went to the new-project input instead of the ticket picker")
	}
}

// TestStickyStatusClearedThenKeyRouted: a sticky status line is cleared by
// any key, and the key is still handled.
func TestStickyStatusClearedThenKeyRouted(t *testing.T) {
	m := goldenModel(t)
	m.statusMsg = "web CA not trusted"
	m.statusSticky = true
	m = pressKey(m, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	if m.statusMsg != "" || m.statusSticky {
		t.Errorf("sticky status not cleared")
	}
	if m.screen != ScreenHelp {
		t.Errorf("the key that cleared the status should still open help")
	}
}
