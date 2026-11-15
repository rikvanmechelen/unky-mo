package sidebar

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
)

// execWithMouseRestore wraps tea.ExecProcess. In bubbletea v2, mouse mode
// is declarative (set in View()), so the next render cycle automatically
// re-enables mouse tracking after the popup closes.
func execWithMouseRestore(c *exec.Cmd, fn func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, fn)
}

func (m Model) showShellOutput(shell claude.ActiveShell) tea.Cmd {
	if shell.OutputFile == "" {
		return func() tea.Msg { return sidebarStatusMsg("no output file found") }
	}

	// Build a header script that shows status info then tails the output
	script := shellOutputScript(m.claude.FormatShellCommand(shell.Command, 60), shell.OutputFile)

	title := fmt.Sprintf(" Shell: %s ", m.claude.FormatShellCommand(shell.Command, 30))
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "95%", "-h", "95%",
		"-T", ttmux.EscapeFormat(title),
		"sh", "-c", script)
	return execWithMouseRestore(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("shell view err: %v", err))
		}
		return nil
	})
}

func (m Model) showDiffPopup(file string) tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	title := fmt.Sprintf(" diff: %s ", filepath.Base(file))
	diffCmd := diffScript(file)
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "95%", "-h", "95%",
		"-d", m.windowPath,
		"-T", ttmux.EscapeFormat(title),
		"sh", "-c", diffCmd)
	return execWithMouseRestore(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("diff err: %v", err))
		}
		return nil
	})
}

func (m Model) openFileInEditor(file string) tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	absPath := filepath.Join(m.windowPath, file)
	title := fmt.Sprintf(" %s ", filepath.Base(file))
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "90%", "-h", "90%",
		"-d", m.windowPath,
		"-T", ttmux.EscapeFormat(title),
		editor, absPath)
	return execWithMouseRestore(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("editor err: %v", err))
		}
		return nil
	})
}

// openFileExternal opens file in VS Code (the project folder, so the
// workspace opens or is reused, with the file focused), else the default.
func (m Model) openFileExternal(file string) tea.Cmd {
	return m.openExternal(file)
}

func (m Model) openProjectExternal() tea.Cmd {
	return m.openExternal("")
}

func (m Model) openExternal(file string) tea.Cmd {
	opener := m.opener
	if opener == nil {
		opener = defaultOpener{}
	}
	dir := m.windowPath
	return func() tea.Msg {
		where, err := opener.Open(dir, file)
		if err != nil {
			return sidebarStatusMsg(err.Error())
		}
		return sidebarStatusMsg("opened in " + where)
	}
}

// openPopup opens a floating popup that attaches to the mo-terms session so
// the drawer's terminal tabs are visible. Backtick inside the popup is
// bound (in the popup-keys key table on mo-terms) to detach-client, so
// closing the popup preserves the shells. If the drawer is currently open,
// the active tab is parked in mo-terms first so it shows up in the popup.
func (m *Model) openPopup() tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	if m.drawerOpen && m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		if err := m.hidePane(m.terminals[m.activeTermIdx].PaneID); err != nil {
			return func() tea.Msg { return sidebarStatusMsg(fmt.Sprintf("err: %v", err)) }
		}
		m.drawerOpen = false
	}
	ghost, err := m.ensureMoTerms()
	if err != nil {
		return func() tea.Msg { return sidebarStatusMsg(fmt.Sprintf("err: %v", err)) }
	}
	// First-popup path: the session we just created came with a default
	// shell window. Track it as a terminal tab so it shows up in the
	// sidebar's terminal list; without this the user would see a shell in
	// the popup that the drawer doesn't know about.
	if ghost != "" {
		m.termCounter++
		m.terminals = append(m.terminals, TerminalTab{
			PaneID: ghost,
			Name:   fmt.Sprintf("term-%d", m.termCounter),
		})
		m.activeTermIdx = len(m.terminals) - 1
	}
	if m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		_ = m.tmux.SelectWindowByPane(m.terminals[m.activeTermIdx].PaneID)
	}
	title := fmt.Sprintf(" %s ", m.windowName)
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "80%", "-h", "80%",
		"-d", m.windowPath,
		"-T", ttmux.EscapeFormat(title),
		"tmux", "attach-session", "-t", m.termSession())
	return execWithMouseRestore(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("err: %v", err))
		}
		return nil
	})
}

// shQuote quotes s as one word for sh.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shellOutputScript is the sh script the shell popup runs: a header naming
// the command, then the output file followed. Both are quoted: the command
// is Claude's own Bash command, and inside double quotes its $(…) or
// backticks would run again just by being looked at.
func shellOutputScript(command, outputFile string) string {
	return `echo "Shell details"; echo ""; echo "Status: running"; printf 'Command: %s\n' ` + shQuote(command) +
		`; echo "Output:"; echo ""; tail -f -- ` + shQuote(outputFile)
}

// diffScript is the sh script the diff popup runs for file. The name is
// quoted: a file in the checkout can be called anything, a;rm -rf ~ too.
func diffScript(file string) string {
	return "git diff --color=always HEAD -- " + shQuote(file) + " | less -R"
}
