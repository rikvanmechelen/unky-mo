package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/tickets"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// Key handling after the prompts (docs/plans/review-remediation.md, step
// 2.4). Each handler reports whether it took the key; one it doesn't take
// falls through to the screen's navigation (updateNav), the way the single
// switch these came from fell out of its case. No two bindings share a key,
// so which handler a key reaches doesn't depend on their order.

// updateKeyGlobal handles the keys every screen shares: help, refresh,
// restart, suspend (start working on the ticket screen), quit and back.
func (m Model) updateKeyGlobal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, keys.Help):
		if m.screen == ScreenHelp {
			m.screen = ScreenDashboard
		} else {
			m.screen = ScreenHelp
		}
		return m, nil, true

	case key.Matches(msg, keys.Back):
		// Back leaves any screen but the dashboard, where the list gets it.
		if m.screen != ScreenDashboard {
			m.screen = ScreenDashboard
			// Reset ticket detail state so a stale ticket doesn't flash
			// next time the screen opens.
			m.detailTicket = nil
			m.detailTicketList = nil
			m.detailTicketErr = ""
			return m, nil, true
		}

	case key.Matches(msg, keys.Refresh):
		// Force-run the work that normally happens on the 5s poll. The
		// sessionRefreshMsg handler cascades into updateProjectStatuses
		// → writeStateFile (which re-reads tmux.ListWindows), so a single
		// refreshSessions call covers the dashboard side. On the project
		// detail screen we additionally emit branchesChangedMsg so branch
		// info (Merged / RemoteGone / worktree attribution) is re-fetched.
		// Also re-scan workspace dirs so newly cloned repos appear without
		// a TUI restart.
		cmds := []tea.Cmd{
			m.refreshProjects(),
			m.refreshSessions(),
			func() tea.Msg { return statusMsgEvent("Refreshed") },
		}
		if m.screen == ScreenProject && m.detailProject != nil {
			cmds = append(cmds, func() tea.Msg { return branchesChangedMsg{} })
		}
		return m, tea.Batch(cmds...), true

	case key.Matches(msg, keys.Restart):
		// Restart the sidebars, then quit; Run execs the freshly
		// installed binary in place once bubbletea has restored the
		// terminal. (Running it as a child via tea.ExecProcess left every
		// old TUI waiting underneath — a chain that grew with each
		// restart, and quitting resurrected the previous, older TUI.)
		m2, cmd := m.beginRestart()
		return m2, cmd, true

	case key.Matches(msg, keys.Suspend):
		// On the ticket detail screen, `s` means "start working" instead
		// of suspend — resolves the project mapping, opens the picker if
		// missing, or kicks off the branch/worktree flow otherwise.
		if m.screen == ScreenTicket {
			m2, cmd := m.handleTicketStartWorking()
			return m2, cmd, true
		}
		if m.tmux == nil {
			return m, func() tea.Msg { return statusMsgEvent("tmux not available") }, true
		}
		tc := m.tmux
		return m, func() tea.Msg {
			if err := tc.DetachClient(); err != nil {
				return statusMsgEvent(fmt.Sprintf("suspend failed: %v", err))
			}
			return nil
		}, true

	case key.Matches(msg, keys.Quit):
		if m.screen != ScreenDashboard {
			m.screen = ScreenDashboard
			return m, nil, true
		}
		if m.activeSessions > 0 {
			m.suspendQuit = &suspendQuitPrompt{}
			return m, nil, true
		}
		m.disableHomeRespawn()
		return m, tea.Quit, true
	}
	return m, nil, false
}

// updateKeyScreen handles the current screen's own keys.
func (m Model) updateKeyScreen(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch m.screen {
	case ScreenDashboard:
		return m.updateKeyDashboard(msg)
	case ScreenProject:
		return m.updateKeyProject(msg)
	case ScreenTicket:
		return m.updateKeyTicket(msg)
	}
	return m, nil, false
}

func (m Model) updateKeyDashboard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, keys.Enter):
		return m.enterDashboard()

	case key.Matches(msg, keys.AgentLaunch):
		// Shift+enter on the project list: pick an agent, then launch on
		// the main branch.
		if m.dashFocusLeft {
			if sel, ok := m.list.SelectedItem().(ProjectItem); ok {
				m.agentPicker = &agentPickerPrompt{branch: project.Branch{Name: "main", IsMain: true}}
				m.detailProject = &sel.project
				return m, nil, true
			}
		}

	case key.Matches(msg, keys.New):
		m2, cmd := m.newSessionKey()
		return m2, cmd, true

	case key.Matches(msg, keys.NewProject):
		if m.dashFocusLeft {
			if len(m.workspaceDirs) == 0 {
				m.statusMsg = "no workspace_dirs configured"
				return m, nil, true
			}
			ti := textinput.New()
			ti.Placeholder = "folder-name"
			ti.Focus()
			m.newProject = &newProjectPrompt{input: ti, parentDir: m.workspaceDirs[0]}
			return m, nil, true
		}

	case key.Matches(msg, keys.Attach):
		return m, m.attachSession(), true

	case key.Matches(msg, keys.Resume):
		return m, m.resumeSession(), true

	case key.Matches(msg, keys.Tab):
		m.dashFocusLeft = !m.dashFocusLeft
		return m, nil, true

	case msg.String() == "right" || msg.String() == "l":
		m.dashFocusLeft = false
		// Default to sessions; fall through to tickets when no sessions exist.
		if len(m.dashSessionItems) == 0 && m.ticketsVisibleLen() > 0 {
			m.dashRightFocus = dashRightTickets
		} else {
			m.dashRightFocus = dashRightSessions
		}
		return m, nil, true

	case msg.String() == "left" || msg.String() == "h":
		if !m.dashFocusLeft {
			m.dashFocusLeft = true
			return m, nil, true
		}

	case key.Matches(msg, keys.OpenInBrowser):
		if !m.dashFocusLeft && m.dashRightFocus == dashRightTickets {
			if t := m.ticketAtCursor(); t != nil && t.URL != "" {
				url := t.URL
				id := t.ID
				return m, func() tea.Msg {
					if err := openInBrowser(url); err != nil {
						return statusMsgEvent(fmt.Sprintf("Open failed: %v", err))
					}
					return statusMsgEvent(fmt.Sprintf("Opened %s in browser", id))
				}, true
			}
		}
	}
	return m, nil, false
}

// enterDashboard opens what the dashboard's cursor is on: a ticket (or an
// overflow row's toggle), a live session's window (or the import prompt for
// an external one), or a project's detail screen.
func (m Model) enterDashboard() (tea.Model, tea.Cmd, bool) {
	if !m.dashFocusLeft && m.dashRightFocus == dashRightTickets {
		// Overflow row: toggle expansion for that bucket. Cursor stays
		// on the same row so consecutive enters flip between states.
		if bucket, ok := m.bucketAtOverflowCursor(); ok {
			if m.ticketsExpanded == nil {
				m.ticketsExpanded = map[tickets.Bucket]bool{}
			}
			m.ticketsExpanded[bucket] = !m.ticketsExpanded[bucket]
			// Clamp cursor to the new visible length (expanding grows it,
			// collapsing can shrink it below the current position).
			if n := m.ticketsVisibleLen(); n > 0 && m.ticketsCursor >= n {
				m.ticketsCursor = n - 1
			}
			return m, nil, true
		}
		// Ticket row: open the detail screen.
		if t := m.ticketAtCursor(); t != nil {
			selected := *t // copy so the list can safely change
			m.screen = ScreenTicket
			m.detailTicket = nil
			m.detailTicketErr = ""
			m.detailTicketLoading = true
			m.detailTicketList = &selected
			return m, m.fetchTicketDetail(selected.ID, selected.Provider), true
		}
		return m, nil, true
	}
	if !m.dashFocusLeft {
		// Sessions panel: switch to the selected session's tmux window —
		// or, if it's external, prompt to import it instead of failing.
		if m.dashSessionCursor >= 0 && m.dashSessionCursor < len(m.dashSessionItems) {
			item := m.dashSessionItems[m.dashSessionCursor]
			if item.Status == StatusExternal {
				if id := m.externalSessions[item.ProjectPath]; id != "" {
					m.importExternal = &importPrompt{
						sessionID: id,
						pid:       m.externalPIDs[item.ProjectPath],
						path:      item.ProjectPath,
						window:    item.WindowName,
						project:   item.Name,
					}
				}
				return m, nil, true
			}
			if m.tmux != nil {
				target := m.safeWindowTarget(item.WindowName, item.WindowID)
				m.tmux.SwitchToWindow(target)
			}
		}
		return m, nil, true
	}
	p := m.currentProject()
	if p != nil {
		m.detailProject = p
		m.detailSession = m.claude.SessionForPath(p.Path)
		// Branches and worktrees load in the background (loadDetail): git
		// on a large repo would freeze the TUI here.
		m.detailWorktrees, m.detailBranches = nil, nil
		m.detailLoading = true
		m.buildDetailRows()
		m.detailCursor = 0
		m.detailRecap = nil
		m.detailPRs = nil
		m.detailPRCursor = 0
		m.detailPRErr = ""
		m.detailPRExpanded = -1
		m.detailPRDetail = nil
		m.syncedSessions = nil
		m.remoteSynced = nil
		m.detailFocusLeft = true
		m.screen = ScreenProject
		return m, tea.Batch(
			m.loadDetail(p.Path, true),
			m.fetchPRs(p.Path),
			m.autoSyncPull(p.Name, p.Path),
		), true
	}
	return m, nil, true
}

func (m Model) updateKeyProject(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, keys.Enter):
		return m.enterProject()

	case key.Matches(msg, keys.AgentLaunch):
		// Shift+enter on a branch row: pick an agent for it.
		if m.detailFocusLeft && m.detailCursor >= 0 && m.detailCursor < len(m.detailRows) {
			row := m.detailRows[m.detailCursor]
			if (row.kind == "branch" || row.kind == "br-empty") && row.branch != nil {
				m.agentPicker = &agentPickerPrompt{branch: *row.branch}
				return m, nil, true
			}
		}

	case key.Matches(msg, keys.New):
		m2, cmd := m.newSessionKey()
		return m2, cmd, true

	case key.Matches(msg, keys.Attach):
		return m, m.attachSession(), true

	case key.Matches(msg, keys.Resume):
		return m, m.resumeSession(), true

	case key.Matches(msg, keys.Tab):
		m.detailFocusLeft = !m.detailFocusLeft
		return m, nil, true

	case msg.String() == "right" || msg.String() == "l":
		m.detailFocusLeft = false
		return m, nil, true

	case msg.String() == "left" || msg.String() == "h":
		if !m.detailFocusLeft {
			m.detailFocusLeft = true
			return m, nil, true
		}
		// Already on the left panel: back to the dashboard.
		m.screen = ScreenDashboard
		return m, nil, true

	case key.Matches(msg, keys.OpenInBrowser):
		if !m.detailFocusLeft && len(m.detailPRs) > 0 {
			pr := m.detailPRs[m.detailPRCursor]
			return m, func() tea.Msg {
				gh.OpenPRInBrowser(m.detailProject.Path, pr.Number)
				return statusMsgEvent(fmt.Sprintf("Opened PR #%d in browser", pr.Number))
			}, true
		}

	case key.Matches(msg, keys.Checkout):
		if !m.detailFocusLeft && len(m.detailPRs) > 0 && m.detailPRCursor >= 0 && m.detailPRCursor < len(m.detailPRs) {
			pr := m.detailPRs[m.detailPRCursor]
			projectPath := m.detailProject.Path
			return m, func() tea.Msg {
				if err := gh.CheckoutPRBranch(projectPath, pr.Number); err != nil {
					return statusMsgEvent(fmt.Sprintf("Checkout failed: %v", err))
				}
				return statusMsgEvent(fmt.Sprintf("Checked out branch: %s", pr.Branch))
			}, true
		}

	case key.Matches(msg, keys.NewWorktree):
		if m.detailProject != nil {
			// If focus is on the right panel, create worktree from the highlighted PR.
			if !m.detailFocusLeft && len(m.detailPRs) > 0 && m.detailPRCursor >= 0 && m.detailPRCursor < len(m.detailPRs) {
				pr := m.detailPRs[m.detailPRCursor]
				return m, m.createWorktreeFromPR(pr), true
			}
			// Session row: open the lift-to-new-worktree flow. Captures the
			// source session info up front so later cursor moves can't
			// change the target mid-flow.
			if row := m.currentDetailRow(); row != nil && row.kind == "br-session" && row.session != nil {
				m2, cmd := m.startLiftSessionPrompt(row)
				return m2, cmd, true
			}
			// Left panel, plain branch row: today's behavior — worktree for that branch.
			if b := m.currentBranchRow(); b != nil {
				return m, m.createWorktreeAndLaunch(b.Name), true
			}
		}

	case key.Matches(msg, keys.NewBranchPrompt):
		if m.detailProject != nil {
			ti := textinput.New()
			ti.Placeholder = "new branch name"
			ti.Focus()
			ti.CharLimit = 128
			ti.SetWidth(40)
			m.worktree = &worktreePrompt{input: ti}
			return m, textinput.Blink, true
		}

	case key.Matches(msg, keys.OpenInMain):
		if b := m.currentBranchRow(); m.detailFocusLeft && b != nil {
			return m, m.openBranchInMain(b.Name, false), true
		}

	case key.Matches(msg, keys.OpenInMainForce):
		if b := m.currentBranchRow(); m.detailFocusLeft && b != nil {
			return m, m.openBranchInMain(b.Name, true), true
		}

	case key.Matches(msg, keys.Cleanup):
		if m.detailFocusLeft && m.detailProject != nil {
			b := m.currentBranchRow()
			if b == nil {
				return m, nil, true
			}
			if b.IsMain && b.WorktreePath == "" {
				return m, func() tea.Msg { return statusMsgEvent("Cannot delete the main branch") }, true
			}
			var sessions []claude.Session
			if b.WorktreePath != "" {
				live, _ := m.claude.LiveSessions()
				for _, s := range live {
					if claude.PathWithin(s.CWD, b.WorktreePath) {
						sessions = append(sessions, s)
					}
				}
			}
			stage := "action"
			if len(sessions) > 0 {
				stage = "kill"
			}
			m.cleanup = &cleanupMenu{
				stage:        stage,
				projectPath:  m.detailProject.Path,
				projectName:  m.detailProject.Name,
				branch:       b.Name,
				worktreePath: b.WorktreePath,
				sessions:     sessions,
			}
			return m, nil, true
		}
	}
	return m, nil, false
}

// enterProject expands or collapses the PR under the cursor, or acts on the
// branch row: switch to or resume its session (offering the new-session
// menu when another session holds the primary window), open the branch, or
// pull a session another machine pushed.
func (m Model) enterProject() (tea.Model, tea.Cmd, bool) {
	// PR panel: toggle expand/collapse
	if !m.detailFocusLeft && len(m.detailPRs) > 0 {
		if m.detailPRExpanded == m.detailPRCursor {
			// Collapse
			m.detailPRExpanded = -1
			m.detailPRDetail = nil
		} else {
			// Expand and fetch detail
			m.detailPRExpanded = m.detailPRCursor
			m.detailPRDetail = nil
			return m, m.fetchPRDetail(m.detailPRs[m.detailPRCursor].Number), true
		}
		return m, nil, true
	}

	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return m, nil, true
	}
	row := m.detailRows[m.detailCursor]
	switch row.kind {
	case "br-session":
		if row.session == nil || row.branch == nil {
			return m, nil, true
		}
		selectedID := row.session.SessionID
		// If this session is already live in a tmux window, switch
		// straight to it — no prompt, nothing to disconnect.
		if row.tmuxWindow != "" {
			return m, m.resumeInDir(selectedID, row.path, row.tmuxWindow), true
		}
		// Historical resume. If the primary window is free, just
		// launch there. If it's occupied by a different session,
		// open the same switch/park+new/concurrent menu `n` uses,
		// with the selected session set as the resume target.
		branchName := ""
		if !row.branch.IsMain {
			branchName = row.branch.Name
		}
		windowName := ttmux.ComposeWindowName(m.detailProject.Name, branchName, "")
		if m.tmux == nil || !m.tmux.WindowExists(windowName) {
			return m, m.resumeInDir(selectedID, row.path, windowName), true
		}
		existing := m.claude.SessionForPath(row.path)
		if existing == nil || existing.SessionID == selectedID {
			return m, m.resumeInDir(selectedID, row.path, windowName), true
		}
		m.newSession = &newSessionPrompt{
			project:       m.detailProject.Name,
			branch:        branchName,
			cwd:           row.path,
			primaryWin:    windowName,
			livePID:       existing.PID,
			liveID:        existing.SessionID,
			resumeSession: selectedID,
		}
		return m, nil, true
	case "branch", "br-empty":
		if row.branch == nil {
			return m, nil, true
		}
		return m, m.resumeBranchSmart(*row.branch), true
	case "br-remote":
		if row.branch == nil || row.remoteMeta == nil {
			return m, nil, true
		}
		return m, m.pullRemoteSessionAndLaunch(row.branch.Name, *row.remoteMeta), true
	}
	return m, nil, true
}

func (m Model) updateKeyTicket(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, keys.OpenInBrowser) {
		var url, id string
		if m.detailTicket != nil {
			url, id = m.detailTicket.URL, m.detailTicket.ID
		} else if m.detailTicketList != nil {
			url, id = m.detailTicketList.URL, m.detailTicketList.ID
		}
		if url != "" {
			return m, func() tea.Msg {
				if err := openInBrowser(url); err != nil {
					return statusMsgEvent(fmt.Sprintf("Open failed: %v", err))
				}
				return statusMsgEvent(fmt.Sprintf("Opened %s in browser", id))
			}, true
		}
	}
	return m, nil, false
}

// newSessionKey is n on the dashboard or the project screen: launch at the
// target, or, when a session already runs there, open the new-session menu.
func (m Model) newSessionKey() (tea.Model, tea.Cmd) {
	// If a live session already exists at the target, open the
	// s/p/c/esc menu. Otherwise launch directly.
	project, branch, cwd, ok := m.detailLaunchTarget()
	if !ok {
		return m, m.launchSession()
	}
	primaryWin, existing := m.primaryWindowForTarget(project, branch, cwd)
	if existing == nil {
		return m, m.launchSession()
	}
	m.newSession = &newSessionPrompt{
		project:    project,
		branch:     branch,
		cwd:        cwd,
		primaryWin: primaryWin,
		livePID:    existing.PID,
		liveID:     existing.SessionID,
	}
	return m, nil
}
