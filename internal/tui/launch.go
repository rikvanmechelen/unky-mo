package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// agentResumeCmd builds the shell command for resuming a session with the
// given agent. If the agent has no ResumeCmd, falls back to a fresh launch.
func agentResumeCmd(agent config.AgentConfig, sessionID string) string {
	return ops.AgentShellCmd(&agent, sessionID)
}

// doCreateProject creates the project folder + git repo in the background and
// returns a newProjectCreatedMsg on success or a statusMsgEvent on failure.
func (m Model) doCreateProject(parentDir, name string) tea.Cmd {
	return func() tea.Msg {
		res, err := ops.CreateProject(ops.CreateProjectParams{
			ParentDir:  parentDir,
			FolderName: name,
		})
		if err != nil {
			return statusMsgEvent("create project: " + err.Error())
		}
		return newProjectCreatedMsg{name: name, path: res.Path}
	}
}

func (m Model) launchSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		windowName, cwd, ok := m.detailContext()
		if !ok {
			return statusMsgEvent("No project selected")
		}
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, cwd, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
	}
}

// parkAndLaunchPrimary is a thin TUI adapter over ops.ParkAndLaunch.
func (m Model) parkAndLaunchPrimary(pid int, primaryWindowName, cwd, resumeID string) tea.Cmd {
	agent := m.defaultAgent()
	shellCmd := agent.Cmd
	if resumeID != "" {
		shellCmd = agentResumeCmd(agent, resumeID)
	}
	return func() tea.Msg {
		_, err := ops.ParkAndLaunch(m.ops, ops.ParkParams{
			PID:               pid,
			PrimaryWindowName: primaryWindowName,
			Cwd:               cwd,
			ResumeID:          resumeID,
			ShellCmd:          shellCmd,
			AgentKey:          agent.Key,
		})
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Park failed: %v", err))
		}
		return statusMsgEvent("Launched " + agent.Name + " in " + primaryWindowName)
	}
}

// launchSiblingSession is a thin TUI adapter over ops.LaunchSibling.
func (m Model) launchSiblingSession(project, branch, cwd, resumeID string) tea.Cmd {
	agent := m.defaultAgent()
	shellCmd := agent.Cmd
	if resumeID != "" {
		shellCmd = agentResumeCmd(agent, resumeID)
	}
	return func() tea.Msg {
		res, err := ops.LaunchSibling(m.ops, ops.SiblingParams{
			ProjectName: project,
			Branch:      branch,
			Cwd:         cwd,
			ResumeID:    resumeID,
			ShellCmd:    shellCmd,
			AgentKey:    agent.Key,
		})
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Sibling launch failed: %v", err))
		}
		return statusMsgEvent("Launched " + agent.Name + " in " + res.Target)
	}
}

// launchAgentInWindow launches a coding agent session in a new tmux window.
func (m Model) launchAgentInWindow(windowName, cwd, shellCmd, agentName, agentKey string) tea.Msg {
	_, err := ops.LaunchSession(m.ops, ops.LaunchParams{
		WindowName:    windowName,
		Cwd:           cwd,
		ShellCmd:      shellCmd,
		AgentKey:      agentKey,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		return statusMsgEvent(fmt.Sprintf("Launch failed: %v", err))
	}
	return statusMsgEvent("Launched " + agentName + " in " + windowName)
}

func (m Model) resumeSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		project, branch, cwd, ok := m.detailLaunchTarget()
		if !ok {
			return statusMsgEvent("No project selected")
		}
		bareName := ttmux.ComposeWindowName(project, branch, "")
		// Resolve the real window for the current primary — it may have
		// been renamed to carry a custom title, in which case the bare
		// name doesn't exist and we must switch to the real one.
		realWin, session := m.primaryWindowForTarget(project, branch, cwd)
		if session == nil {
			return statusMsgEvent("No session to resume for " + bareName)
		}
		if existed, err := m.focusIfExists(realWin); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		// Live session reports existence but no tmux window (shouldn't
		// normally happen) — launch a fresh window that resumes it.
		return m.launchAgentInWindow(bareName, cwd, agentResumeCmd(m.defaultAgent(), session.SessionID), m.defaultAgent().Name, m.defaultAgent().Key)
	}
}

func (m Model) resumeSpecificSession(sessionID string) tea.Cmd {
	return m.resumeInDir(sessionID, m.detailProject.Path, m.detailProject.Name)
}

func (m Model) resumeInDir(sessionID, cwd, windowName string) tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		// If the session is live, its current window may differ from the
		// caller's windowName (e.g. detail rows built before a rename tick).
		// Prefer the live lookup to avoid spawning a duplicate.
		if sessionID != "" {
			if realName := m.sessionToWindowMap()[sessionID]; realName != "" {
				windowName = realName
			}
		}
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchResumeInWindow(windowName, cwd, sessionID)
	}
}

// importExternalSession is a thin TUI adapter over ops.ImportExternalSession.
func (m Model) importExternalSession(pid int, sessionID, cwd, windowName string) tea.Cmd {
	return func() tea.Msg {
		_, err := ops.ImportExternalSession(m.ops, ops.ImportParams{
			PID:        pid,
			SessionID:  sessionID,
			Cwd:        cwd,
			WindowName: windowName,
		})
		if err != nil {
			return sessionImportedMsg{status: fmt.Sprintf("Import failed: %v", err)}
		}
		return sessionImportedMsg{status: "Resumed session in " + windowName}
	}
}

// executeSuspendAll builds the session-to-stop list from sessionViews,
// calls ops.SuspendAll, and returns a suspendCompleteMsg.
func (m Model) executeSuspendAll() tea.Cmd {
	sessions := make([]ops.SessionToStop, 0, len(m.sessionViews))
	for _, v := range m.sessionViews {
		if v.External {
			continue
		}
		sessions = append(sessions, ops.SessionToStop{
			SuspendedSession: ops.SuspendedSession{
				WindowName:  v.WindowName,
				Cwd:         v.CWD,
				SessionID:   v.SessionID,
				AgentKey:    v.AgentKey,
				ProjectName: v.ProjectName,
				Parent:      v.Parent,
			},
			PID: v.PID,
		})
	}

	opsCtx := m.ops
	tmuxSession := ""
	if m.tmux != nil {
		tmuxSession = m.tmux.SessionName()
	}

	return func() tea.Msg {
		path := ops.SuspendedStatePath()
		_, err := ops.SuspendAll(opsCtx, path, ops.SuspendParams{
			Sessions:    sessions,
			TmuxSession: tmuxSession,
		})
		return suspendCompleteMsg{err: err}
	}
}

// launchResumeInWindow is a thin TUI adapter over ops.LaunchSession that
// passes `claude --resume <id>` as the shell command.
func (m Model) launchResumeInWindow(windowName, projectPath, sessionID string) tea.Msg {
	agent := m.defaultAgent()
	_, err := ops.LaunchSession(m.ops, ops.LaunchParams{
		WindowName:    windowName,
		Cwd:           projectPath,
		ShellCmd:      agentResumeCmd(agent, sessionID),
		AgentKey:      agent.Key,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		return statusMsgEvent(fmt.Sprintf("Resume failed: %v", err))
	}
	return statusMsgEvent("Resumed session in " + windowName)
}

// launchBranchWithAgent is like resumeBranchSmart but launches the given
// coding agent instead of the default. Used by the shift+enter agent picker.
func (m Model) launchBranchWithAgent(b project.Branch, agent config.AgentConfig) tea.Cmd {
	p := m.detailProject
	if p == nil {
		return func() tea.Msg { return statusMsgEvent("No project selected") }
	}
	switch {
	case b.WorktreePath != "":
		// Existing worktree: focus if live, else launch with chosen agent.
		return m.launchWorktreeSessionWithAgent(project.Worktree{Path: b.WorktreePath, Branch: b.Name}, agent)
	case b.IsMain:
		return func() tea.Msg { return m.launchAgentInWindow(p.Name, p.Path, agent.Cmd, agent.Name, agent.Key) }
	default:
		return m.createWorktreeAndLaunchWithAgent(b.Name, agent)
	}
}

func (m Model) attachSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		project, branch, cwd, ok := m.detailLaunchTarget()
		if !ok {
			return statusMsgEvent("No project selected")
		}
		bareName := ttmux.ComposeWindowName(project, branch, "")
		// Prefer the real window name for the current primary (handles
		// renamed primaries). If no live session exists, fall back to the
		// composed bare name so the "no session" error stays stable.
		targetWin, session := m.primaryWindowForTarget(project, branch, cwd)
		if session == nil {
			return statusMsgEvent("No session for " + bareName)
		}
		existed, err := m.focusIfExists(targetWin)
		if !existed {
			return statusMsgEvent("No session for " + bareName)
		}
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to attach: %v", err))
		}
		return statusMsgEvent("Attached to " + targetWin)
	}
}
