package tui

import (
	"fmt"
	"strings"
)

func (m Model) helpView() string {
	title := titleStyle.Render(" Help ")

	sections := []struct {
		name  string
		binds []footerBinding
	}{
		{"Navigation", []footerBinding{
			{"↑/k", "Move up"},
			{"↓/j", "Move down"},
			{"enter/→", "Open project detail"},
			{"esc/←", "Go back"},
			{"/", "Filter projects"},
		}},
		{"Sessions", []footerBinding{
			{"n", "New session (prompts if one is already running: switch/park+new/concurrent)"},
			{"a", "Attach to session (switch tmux window)"},
			{"r", "Resume most recent session"},
		}},
		{"Branches (project detail)", []footerBinding{
			{"A", "Pick coding agent before launching (Claude, Gemini, etc.)"},
			{"w", "Open branch under cursor as a worktree"},
			{"W", "Prompt for a new branch name + worktree"},
			{"m", "Check out branch in main repo (refuse if dirty)"},
			{"M", "Stash first, then check out in main"},
			{"x", "Remove worktree / delete branch (prompts; refuses on main)"},
		}},
		{"Other", []footerBinding{
			{"s", "Suspend (leaves tmux session running; re-launch mo to resume)"},
			{"?", "Toggle this help"},
			{"ctrl+r", "Refresh (re-poll sessions, branches, state file)"},
			{"ctrl+alt+r", "Restart (reload freshly-installed binary)"},
			{"q", "Quit"},
		}},
	}

	var b strings.Builder
	b.WriteString(title + "\n\n")

	for _, sec := range sections {
		b.WriteString(headerStyle.Render(sec.name) + "\n")
		for _, bind := range sec.binds {
			k := footerKeyStyle.Width(12).Render(bind.key)
			d := footerDescStyle.Render(bind.desc)
			b.WriteString(fmt.Sprintf("  %s %s\n", k, d))
		}
		b.WriteString("\n")
	}

	b.WriteString(footerDescStyle.Render("Press ? or esc to close"))

	return b.String()
}

type footerBinding struct {
	key  string
	desc string
}

func (m Model) renderFooter(bindings []footerBinding) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	// Auto-wrap bindings into rows that fit the available width.
	// Each binding renders as " key desc"; two spaces separate entries.
	var rows []string
	var rowParts []string
	rowWidth := 0

	for _, b := range bindings {
		entryWidth := 1 + len(b.key) + 1 + len(b.desc) // " key desc"
		sep := 2                                       // "  " between entries
		if len(rowParts) == 0 {
			sep = 0
		}
		if rowWidth+sep+entryWidth > width && len(rowParts) > 0 {
			rows = append(rows, strings.Join(rowParts, ""))
			rowParts = nil
			rowWidth = 0
			sep = 0
		}
		entry := " " + footerKeyStyle.Render(b.key) + " " + footerDescStyle.Render(b.desc)
		if sep > 0 {
			entry = "  " + entry
		}
		rowParts = append(rowParts, entry)
		rowWidth += sep + entryWidth
	}
	if len(rowParts) > 0 {
		rows = append(rows, strings.Join(rowParts, ""))
	}

	body := strings.Join(rows, "\n")
	return footerStyle.Width(width).Render(body)
}

// yesNoBindings is the canonical footer-binding slice for every yes/no
// confirmation prompt — keeps the visual form identical across screens.
// Convention: question text ends with " [y/N]"; y/Y/enter confirm,
// n/N/esc/escape cancel. See CLAUDE.md → Prompt conventions.
func yesNoBindings(yesDesc string) []footerBinding {
	return []footerBinding{
		{"y", yesDesc},
		{"n", "cancel"},
	}
}

// withCancel appends a "[n] cancel" binding to a multi-option menu's
// binding list unless one is already present. Multi-option menus accept
// n/N as cancel alongside esc/escape; this helper guarantees the hint
// always shows in the footer.
func withCancel(binds []footerBinding) []footerBinding {
	for _, b := range binds {
		if b.key == "n" {
			return binds
		}
	}
	return append(binds, footerBinding{"n", "cancel"})
}

// agentPickerBindings builds footer bindings from the configured agents.
func (m Model) agentPickerBindings() []footerBinding {
	binds := make([]footerBinding, len(m.agents))
	for i, a := range m.agents {
		binds[i] = footerBinding{a.Key, a.Name}
	}
	return binds
}

// renderPrompt renders a two-row confirmation bar: the question on top, key
// hints on the bottom. Uses the same styling as the normal footer.
func (m Model) renderPrompt(question string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+" "+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + hints
	return footerStyle.Width(m.width).Render(body)
}

// renderInputPrompt renders a three-row input bar: the question, a text-input
// line, and key hints. Used by the new-worktree flow.
func (m Model) renderInputPrompt(question, inputView string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+" "+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + inputView + "\n" + hints
	return footerStyle.Width(m.width).Render(body)
}

// cleanupPrompt returns the stage-appropriate question + bindings for the
// `x` cleanup menu (kill-sessions confirmation first, then action menu).
// Follows the prompt convention: yes/no uses [y/N]; multi-option menus end
// with a [n] cancel hint.
func (m Model) cleanupPrompt() (string, []footerBinding) {
	switch m.cleanup.stage {
	case "kill":
		n := len(m.cleanup.sessions)
		q := fmt.Sprintf("⚠ %d live session(s) in %s — kill them? [y/N]", n, m.cleanup.branch)
		return q, yesNoBindings("kill + continue")
	case "action":
		if m.cleanup.worktreePath != "" {
			q := fmt.Sprintf("Remove worktree %s?", m.cleanup.branch)
			return q, withCancel([]footerBinding{
				{"w", "worktree only"},
				{"b", "worktree + branch"},
			})
		}
		q := fmt.Sprintf("Delete branch %s? [y/N]", m.cleanup.branch)
		return q, yesNoBindings("delete branch")
	}
	return "", nil
}

// newMenuPromptText renders the question shown above the s/p/c/esc menu.
// Names the primary target so the user knows what they're about to affect.
// If the menu was opened to resume a specific historical session, the
// prompt mentions that too so `p` / `c` semantics are unambiguous.
func (m Model) newMenuPromptText() string {
	target := m.newSession.primaryWin
	if target == "" {
		target = "this target"
	}
	if m.newSession.resumeSession != "" {
		return fmt.Sprintf("A different session is running in %s — how should the selected session be resumed?", target)
	}
	return fmt.Sprintf("A session is already running in %s — what would you like to do?", target)
}
