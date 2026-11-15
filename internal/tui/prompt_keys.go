package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// promptKeys is the key handler of the open prompt that takes keys now, or
// nil when none does. When several are open the first one here wins: the
// order is the prompts' priority, pinned by keyrouting_test.go. The
// worktree and lift inputs only take keys on the project screen.
func (m Model) promptKeys() func(Model, tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.newProject != nil:
		return Model.keyNewProject
	case m.worktree != nil && m.screen == ScreenProject:
		return Model.keyWorktree
	case m.lift != nil && !m.lift.dirty && m.screen == ScreenProject:
		return Model.keyLiftInput
	case m.lift != nil && m.lift.dirty:
		return Model.keyLiftDirty
	case m.newSession != nil:
		return Model.keyNewSession
	case m.cleanup != nil:
		return Model.keyCleanup
	case m.worktreeExists != nil:
		return Model.keyWorktreeExists
	case m.agentPicker != nil:
		return Model.keyAgentPicker
	case m.suspendQuit != nil:
		return Model.keySuspendQuit
	case m.importExternal != nil:
		return Model.keyImport
	}
	return nil
}

// keyNewProject takes keys while the new-project folder name is asked for.
func (m Model) keyNewProject(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := strings.TrimSpace(m.newProject.input.Value())
		parentDir := m.newProject.parentDir
		m.newProject = nil
		if name == "" {
			return m, nil
		}
		return m, m.doCreateProject(parentDir, name)
	case "esc", "escape":
		m.newProject = nil
		return m, nil
	}
	p := *m.newProject
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	m.newProject = &p
	return m, cmd
}

// keyWorktree takes keys while a new worktree's branch name is asked for.
func (m Model) keyWorktree(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		branch := strings.TrimSpace(m.worktree.input.Value())
		m.worktree = nil
		if branch == "" {
			return m, nil
		}
		return m, m.createWorktreeAndLaunch(branch)
	case "esc", "escape":
		m.worktree = nil
		return m, nil
	}
	p := *m.worktree
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	m.worktree = &p
	return m, cmd
}

// keyLiftInput takes keys while the lift's new branch name is asked for.
func (m Model) keyLiftInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		branch := strings.TrimSpace(m.lift.input.Value())
		if branch == "" {
			m.lift = nil
			return m, nil
		}
		return m.decideLiftDirty(branch)
	case "esc", "escape":
		m.lift = nil
		return m, nil
	}
	p := *m.lift
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	m.lift = &p
	return m, cmd
}

// keyLiftDirty takes keys on the lift's dirty-source menu: [s]tash+pop,
// [l]eave, [n] cancel. Non-destructive (a new worktree deletes nothing), so
// enter takes s.
func (m Model) keyLiftDirty(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s", "S", "enter":
		cmd := m.runLiftSession(true)
		m.lift = nil
		return m, cmd
	case "l", "L":
		cmd := m.runLiftSession(false)
		m.lift = nil
		return m, cmd
	case "n", "N", "esc", "escape":
		m.lift = nil
		return m, nil
	}
	return m, nil
}

// keyNewSession takes keys on the new-session menu: switch, park+new,
// concurrent. enter takes switch, the least destructive.
func (m Model) keyNewSession(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Multi-option menu. Primary = switch (least destructive: no kill,
	// no spawn). enter fires the primary; n/N/esc cancel.
	switch msg.String() {
	case "s", "S", "enter":
		primary := m.newSession.primaryWin
		m.newSession = nil
		return m, func() tea.Msg {
			if existed, err := m.focusIfExists(primary); existed {
				if err != nil {
					return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
				}
				return statusMsgEvent("Switched to " + primary)
			}
			return statusMsgEvent("No primary window to switch to")
		}
	case "p", "P":
		pid := m.newSession.livePID
		primary := m.newSession.primaryWin
		cwd := m.newSession.cwd
		resumeID := m.newSession.resumeSession
		m.newSession = nil
		return m, m.parkAndLaunchPrimary(pid, primary, cwd, resumeID)
	case "c", "C":
		resumeID := m.newSession.resumeSession
		project := m.newSession.project
		branch := m.newSession.branch
		cwd := m.newSession.cwd
		m.newSession = nil
		return m, m.launchSiblingSession(project, branch, cwd, resumeID)
	case "n", "N", "esc", "escape":
		m.newSession = nil
		return m, nil
	}
	return m, nil
}

// keyCleanup takes keys on the cleanup menu: stop the worktree's sessions,
// then remove the worktree and/or the branch. Destructive: enter cancels.
func (m Model) keyCleanup(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.cleanup.stage {
	case "kill":
		// Destructive [y/N] confirm: only y/Y confirms. Enter matches
		// the default (N) and cancels, same as n/esc.
		switch msg.String() {
		case "y", "Y":
			sessions := m.cleanup.sessions
			c := *m.cleanup
			c.stage = "action"
			m.cleanup = &c
			return m, m.killCleanupSessions(sessions)
		case "n", "N", "enter", "esc", "escape":
			m.cleanup = nil
			return m, nil
		}
		return m, nil
	case "action":
		if m.cleanup.worktreePath != "" {
			// Destructive multi-option: no enter-as-primary. Every
			// action requires an explicit letter; enter cancels.
			switch msg.String() {
			case "w", "W":
				projectPath := m.cleanup.projectPath
				branch := m.cleanup.branch
				m.cleanup = nil
				return m, m.runCleanup(projectPath, branch, false)
			case "b", "B":
				projectPath := m.cleanup.projectPath
				branch := m.cleanup.branch
				m.cleanup = nil
				return m, m.runCleanup(projectPath, branch, true)
			case "n", "N", "enter", "esc", "escape":
				m.cleanup = nil
				return m, nil
			}
			return m, nil
		}
		// Plain-branch row: destructive [y/N] confirm.
		switch msg.String() {
		case "y", "Y":
			projectPath := m.cleanup.projectPath
			branch := m.cleanup.branch
			m.cleanup = nil
			return m, m.runCleanup(projectPath, branch, true)
		case "n", "N", "enter", "esc", "escape":
			m.cleanup = nil
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// keyWorktreeExists takes keys on the worktree-exists menu: focus it, or
// remove and recreate it.
func (m Model) keyWorktreeExists(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "f", "F":
		projectName := m.worktreeExists.projectName
		branch := m.worktreeExists.branch
		wtPath := m.worktreeExists.wtPath
		m.worktreeExists = nil
		return m, m.focusExistingWorktree(projectName, branch, wtPath)
	case "r", "R":
		projectPath := m.worktreeExists.projectPath
		projectName := m.worktreeExists.projectName
		branch := m.worktreeExists.branch
		prBranch := m.worktreeExists.prBranch
		m.worktreeExists = nil
		return m, m.removeAndRecreateWorktree(projectPath, projectName, branch, prBranch)
	case "n", "N", "enter", "esc", "escape":
		m.worktreeExists = nil
		return m, nil
	}
	return m, nil
}

// keyAgentPicker takes keys on the agent picker: one per configured agent.
func (m Model) keyAgentPicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	// Check each configured agent mnemonic.
	for _, a := range m.agents {
		if k == a.Key || k == strings.ToUpper(a.Key) {
			branch := m.agentPicker.branch
			m.agentPicker = nil
			return m, m.launchBranchWithAgent(branch, a)
		}
	}
	// Cancel keys.
	switch k {
	case "n", "N", "esc", "escape", "enter":
		m.agentPicker = nil
		return m, nil
	}
	return m, nil
}

// keySuspendQuit takes keys on suspend-and-quit: s suspends every session
// and quits, any other key cancels.
func (m Model) keySuspendQuit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.suspendQuit = nil
	if s := msg.String(); s == "s" || s == "S" {
		return m, m.executeSuspendAll()
	}
	return m, nil
}

// keyImport takes keys on the import prompt [y/N]: destructive (it kills
// the external claude), so only y confirms.
func (m Model) keyImport(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		p := m.importExternal
		m.importExternal = nil
		return m, m.importExternalSession(p.pid, p.sessionID, p.path, p.window)
	case "n", "N", "enter", "esc", "escape":
		m.importExternal = nil
		return m, nil
	}
	return m, nil
}
