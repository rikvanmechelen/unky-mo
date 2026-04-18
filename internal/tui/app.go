package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rvanmech/unky-mo/internal/claude"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/notify"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
)

// SessionStatus represents the state of a Claude session for a project.
type SessionStatus int

const (
	StatusNone       SessionStatus = iota
	StatusActive                   // Claude is processing
	StatusIdle                     // Waiting for user input
	StatusPermission               // Needs permission approval
	StatusExternal                 // Live claude running outside mo's tmux — not joinable, offer to import
)

// ProjectItem wraps a project for the list component.
type ProjectItem struct {
	project project.Project
	status  SessionStatus
	git     project.GitStatus
}

func (i ProjectItem) Title() string       { return i.project.Name }
func (i ProjectItem) Description() string { return i.project.Path }
func (i ProjectItem) FilterValue() string { return i.project.Name + " " + i.project.Language }

// Screen identifies which view is active.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenProject
	ScreenHelp
)

// sessionTickMsg triggers a session status refresh.
type sessionTickMsg time.Time

func sessionTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return sessionTickMsg(t)
	})
}

// usageTickMsg triggers a Claude usage refresh. 60s is well below the cache
// TTL of the endpoint but keeps the reset countdown in the dashboard fresh
// across the longer sessionTick cadence.
type usageTickMsg time.Time

func usageTick() tea.Cmd {
	return tea.Tick(60*time.Second, func(t time.Time) tea.Msg {
		return usageTickMsg(t)
	})
}

// usageRefreshMsg carries the result of a usage.Fetch call.
type usageRefreshMsg struct {
	snap    usage.Snapshot
	authErr bool
}

func (m Model) fetchUsage() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := usage.Fetch(ctx)
		if errors.Is(err, usage.ErrAuthExpired) {
			return usageRefreshMsg{authErr: true}
		}
		return usageRefreshMsg{snap: snap}
	}
}

// gitStatusMsg carries refreshed git statuses for all projects.
type gitStatusMsg map[string]project.GitStatus

func (m Model) refreshGitStatuses() tea.Cmd {
	return func() tea.Msg {
		statuses := make(map[string]project.GitStatus)
		for _, p := range m.projects {
			statuses[p.Path] = project.GetGitStatus(p.Path)
		}
		return gitStatusMsg(statuses)
	}
}

// WorktreeStatus tracks an active session in a git worktree.
type WorktreeStatus struct {
	Branch     string
	Path       string
	WindowName string // "project@branch"
	Status     SessionStatus
}

// sessionStateMap holds the notification-based status overrides for projects.
// Key is project path, value is the status from notifications.
type sessionStateMap map[string]SessionStatus

// notificationMsg wraps a notification received from the Unix socket.
type notificationMsg notify.Notification

// Model is the root Bubbletea model.
type Model struct {
	screen         Screen
	list           list.Model
	projects       []project.Project
	tmux           *ttmux.Client
	notifServer    *notify.Server
	notifState     sessionStateMap // status overrides from notification system
	statusMsg      string
	activeSessions int
	attentionCount int
	gitStatuses    map[string]project.GitStatus // project path → git status
	// Dashboard active sessions panel (right side)
	dashFocusLeft      bool // true = project list, false = sessions panel
	dashSessionItems   []dashSessionItem
	dashSessionCursor  int
	// Detail views
	detailProject   *project.Project
	detailSession   *claude.Session
	detailWorktrees []project.Worktree
	detailBranches  []project.Branch
	// detailRows is a flat list of navigable items in the project detail view:
	// one row per local branch, each followed by the sessions discovered at
	// that branch's checkout location (main path or worktree path).
	detailRows   []detailRow
	detailCursor int
	detailRecap  []claude.SessionMessage // last messages for currently selected session
	// Resume-confirmation prompt: non-empty means we're asking the user whether
	// to disconnect the currently-running session and resume this one instead.
	pendingResumeSessionID string
	pendingResumePath      string
	pendingResumeWindow    string
	// Import-external-session prompt: non-empty means we're asking the user
	// whether to take over a claude running outside mo (kill it + resume here).
	pendingImportSessionID string
	pendingImportPath      string
	pendingImportWindow    string
	pendingImportProject   string // project display name, for the prompt text
	pendingImportPID       int
	// New-session menu: active when the user pressed `n` on a target that
	// already has a live session. Presents s/p/c/esc options (switch /
	// park+new / concurrent / cancel). Captured per-field so the menu
	// survives cursor movement without re-querying.
	pendingNewMenuActive bool
	pendingNewProject    string
	pendingNewBranch     string // "" for main checkout
	pendingNewCwd        string
	pendingNewPrimaryWin string // composed primary window name (no suffix)
	pendingNewLivePID    int    // claude PID of the session to park on `p`
	pendingNewLiveID     string // claude session ID of the current primary
	// externalPIDs / externalSessions cache the orphan PID + sessionID for each
	// project path currently in StatusExternal, populated by refreshSessions.
	externalPIDs     map[string]int
	externalSessions map[string]string
	// Strays are live sessions whose CWD isn't a known project. Split by the
	// renderers into the "Projects" section (git-backed) and "External"
	// section (everything else).
	strays []strayLive
	// worktreeInput is non-nil when the user is entering a branch name for a
	// new worktree. While set, key events route to the text input.
	worktreeInput *textinput.Model
	// Pull requests panel (right side of project detail)
	detailPRs        []gh.PullRequest
	detailPRCursor   int
	detailPRErr      string         // error message if gh failed
	detailPRExpanded int            // index of expanded PR (-1 = none)
	detailPRDetail   *gh.PRDetail   // fetched detail for expanded PR
	detailFocusLeft  bool           // true = left panel (sessions/worktrees), false = right (PRs)
	syncedSessions map[string]moSync.SessionMeta // sync metadata keyed by session ID for the current detail project
	// remoteSynced holds synced sessions whose worktree does not exist on
	// this machine. Keyed by branch name (empty for main scope, unused here
	// since the main project always has a local path).
	remoteSynced map[string]moSync.SessionMeta
	// activeWorktrees tracks worktree sessions grouped by parent project path.
	activeWorktrees map[string][]WorktreeStatus
	// State file for sidebar instances
	stateFilePath string
	// Claude usage snapshot (5h + weekly rate-limit windows)
	usage      usage.Snapshot
	usageReady bool // false until the first fetch completes (success or cache hit)
	usageAuth  bool // true once a 401 is seen — surfaces an auth-expired banner
	width      int
	height     int
	ready      bool
}

func NewModel(projects []project.Project, tmuxClient *ttmux.Client, notifServer *notify.Server, stateFilePath string) Model {
	items := make([]list.Item, len(projects))
	for i, p := range projects {
		items[i] = ProjectItem{project: p, status: StatusNone}
	}

	l := list.New(items, projectDelegate{}, 0, 0)
	l.Title = "Unky Mo"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)        // We use our own footer
	l.InfiniteScrolling = true  // Circular navigation
	l.Styles.Title = titleStyle

	return Model{
		screen:        ScreenDashboard,
		list:          l,
		projects:      projects,
		tmux:          tmuxClient,
		notifServer:   notifServer,
		notifState:    make(sessionStateMap),
		dashFocusLeft: true,
		stateFilePath: stateFilePath,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		sessionTick(), usageTick(),
		m.refreshSessions(), m.refreshGitStatuses(),
		m.fetchUsage(),
	}
	if m.notifServer != nil {
		cmds = append(cmds, m.waitForNotification())
	}
	return tea.Batch(cmds...)
}

func (m Model) waitForNotification() tea.Cmd {
	return func() tea.Msg {
		n := <-m.notifServer.Messages()
		return notificationMsg(n)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		footerHeight := 3
		headerHeight := 0
		m.list.SetSize(msg.Width, msg.Height-footerHeight-headerHeight)
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		// Clear sticky error messages on any keypress
		if m.statusMsg != "" {
			lower := strings.ToLower(m.statusMsg)
			if strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "err:") {
				m.statusMsg = ""
			}
		}

		// Don't intercept keys when filtering
		if m.list.FilterState() == list.Filtering {
			break
		}

		// New-worktree input captures all input while active.
		if m.worktreeInput != nil && m.screen == ScreenProject {
			switch msg.String() {
			case "enter":
				branch := strings.TrimSpace(m.worktreeInput.Value())
				m.worktreeInput = nil
				if branch == "" {
					return m, nil
				}
				return m, m.createWorktreeAndLaunch(branch)
			case "esc", "escape":
				m.worktreeInput = nil
				return m, nil
			}
			updated, cmd := m.worktreeInput.Update(msg)
			m.worktreeInput = &updated
			return m, cmd
		}

		// Resume-confirmation prompt captures all input while active.
		if m.pendingResumeSessionID != "" && m.screen == ScreenProject {
			switch msg.String() {
			case "y", "Y", "enter":
				sessionID := m.pendingResumeSessionID
				resumePath := m.pendingResumePath
				resumeWindow := m.pendingResumeWindow
				m.pendingResumeSessionID = ""
				m.pendingResumePath = ""
				m.pendingResumeWindow = ""
				return m, m.disconnectAndResumeInDir(sessionID, resumePath, resumeWindow)
			case "n", "N", "esc", "escape":
				m.pendingResumeSessionID = ""
				m.pendingResumePath = ""
				m.pendingResumeWindow = ""
				return m, nil
			}
			return m, nil
		}

		// New-session menu captures all input while active.
		if m.pendingNewMenuActive {
			switch msg.String() {
			case "s", "S":
				primary := m.pendingNewPrimaryWin
				m.clearPendingNewMenu()
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
				pid := m.pendingNewLivePID
				primary := m.pendingNewPrimaryWin
				cwd := m.pendingNewCwd
				m.clearPendingNewMenu()
				return m, m.parkAndLaunchPrimary(pid, primary, cwd)
			case "c", "C":
				m.clearPendingNewMenu()
				return m, m.launchSiblingSession()
			case "esc", "escape":
				m.clearPendingNewMenu()
				return m, nil
			}
			return m, nil
		}

		// Import-external-session prompt captures all input while active.
		if m.pendingImportSessionID != "" {
			switch msg.String() {
			case "y", "Y", "enter":
				sessionID := m.pendingImportSessionID
				pid := m.pendingImportPID
				path := m.pendingImportPath
				window := m.pendingImportWindow
				m.pendingImportSessionID = ""
				m.pendingImportPID = 0
				m.pendingImportPath = ""
				m.pendingImportWindow = ""
				m.pendingImportProject = ""
				return m, m.importExternalSession(pid, sessionID, path, window)
			case "n", "N", "esc", "escape":
				m.pendingImportSessionID = ""
				m.pendingImportPID = 0
				m.pendingImportPath = ""
				m.pendingImportWindow = ""
				m.pendingImportProject = ""
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, keys.Help):
			if m.screen == ScreenHelp {
				m.screen = ScreenDashboard
			} else {
				m.screen = ScreenHelp
			}
			return m, nil

		case key.Matches(msg, keys.Enter):
			if m.screen == ScreenDashboard && !m.dashFocusLeft {
				// Sessions panel: switch to the selected session's tmux window —
				// or, if it's external, prompt to import it instead of failing.
				if m.dashSessionCursor >= 0 && m.dashSessionCursor < len(m.dashSessionItems) {
					item := m.dashSessionItems[m.dashSessionCursor]
					if item.Status == StatusExternal {
						m.pendingImportSessionID = m.externalSessions[item.ProjectPath]
						m.pendingImportPID = m.externalPIDs[item.ProjectPath]
						m.pendingImportPath = item.ProjectPath
						m.pendingImportWindow = item.WindowName
						m.pendingImportProject = item.Name
						return m, nil
					}
					if m.tmux != nil {
						target := m.tmux.SessionName + ":" + item.WindowName
						m.tmux.SwitchToWindow(target)
					}
				}
				return m, nil
			}
			if m.screen == ScreenDashboard {
				p := m.currentProject()
				if p != nil {
					m.detailProject = p
					m.detailSession = claude.SessionForPath(p.Path)
					m.detailWorktrees, _ = project.ListWorktrees(p.Path)
					m.detailBranches, _ = project.ListBranches(p.Path)
					m.buildDetailRows()
					m.detailCursor = 0
					m.loadRecap()
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
						m.fetchPRs(p.Path),
						m.autoSyncPull(p.Name, p.Path),
					)
				}
				return m, nil
			}
			if m.screen == ScreenProject {
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
						return m, m.fetchPRDetail(m.detailPRs[m.detailPRCursor].Number)
					}
					return m, nil
				}

				if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
					return m, nil
				}
				row := m.detailRows[m.detailCursor]
				switch row.kind {
				case "br-session":
					if row.session == nil || row.branch == nil {
						return m, nil
					}
					selectedID := row.session.SessionID
					// If this session is already live in a tmux window, switch
					// straight to it — no prompt, nothing to disconnect.
					if row.tmuxWindow != "" {
						return m, m.resumeInDir(selectedID, row.path, row.tmuxWindow)
					}
					// Historical resume: spawn in the primary window, with a
					// disconnect-confirm if the primary is running a different
					// session.
					windowName := m.detailProject.Name
					if !row.branch.IsMain {
						windowName = m.detailProject.Name + "@" + row.branch.Name
					}
					if m.tmux != nil && m.tmux.WindowExists(windowName) {
						if m.detailSession == nil || m.detailSession.SessionID != selectedID {
							m.pendingResumeSessionID = selectedID
							m.pendingResumePath = row.path
							m.pendingResumeWindow = windowName
							return m, nil
						}
					}
					return m, m.resumeInDir(selectedID, row.path, windowName)
				case "branch", "br-empty":
					if row.branch == nil {
						return m, nil
					}
					return m, m.resumeBranchSmart(*row.branch)
				case "br-remote":
					if row.branch == nil || row.remoteMeta == nil {
						return m, nil
					}
					return m, m.pullRemoteSessionAndLaunch(row.branch.Name, *row.remoteMeta)
				}
				return m, nil
			}

		case key.Matches(msg, keys.Back):
			if m.screen != ScreenDashboard {
				m.screen = ScreenDashboard
				return m, nil
			}

		case key.Matches(msg, keys.New):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				// If a live session already exists at the target, open the
				// s/p/c/esc menu. Otherwise launch directly.
				project, branch, cwd, ok := m.detailLaunchTarget()
				if !ok {
					return m, m.launchSession()
				}
				existing := claude.SessionForPath(cwd)
				if existing == nil {
					return m, m.launchSession()
				}
				m.pendingNewMenuActive = true
				m.pendingNewProject = project
				m.pendingNewBranch = branch
				m.pendingNewCwd = cwd
				m.pendingNewPrimaryWin = ttmux.ComposeWindowName(project, branch, "")
				m.pendingNewLivePID = existing.PID
				m.pendingNewLiveID = existing.SessionID
				return m, nil
			}

		case key.Matches(msg, keys.Attach):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				return m, m.attachSession()
			}

		case key.Matches(msg, keys.Resume):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				return m, m.resumeSession()
			}

		case key.Matches(msg, keys.Tab):
			if m.screen == ScreenDashboard {
				m.dashFocusLeft = !m.dashFocusLeft
				return m, nil
			}
			if m.screen == ScreenProject {
				m.detailFocusLeft = !m.detailFocusLeft
				return m, nil
			}

		case msg.String() == "right" || msg.String() == "l":
			if m.screen == ScreenDashboard {
				m.dashFocusLeft = false
				return m, nil
			}
			if m.screen == ScreenProject {
				m.detailFocusLeft = false
				return m, nil
			}

		case msg.String() == "left" || msg.String() == "h":
			if m.screen == ScreenDashboard {
				if !m.dashFocusLeft {
					m.dashFocusLeft = true
					return m, nil
				}
			}
			if m.screen == ScreenProject {
				if !m.detailFocusLeft {
					m.detailFocusLeft = true
					return m, nil
				}
				// If already on left panel, go back to dashboard
				m.screen = ScreenDashboard
				return m, nil
			}

		case key.Matches(msg, keys.OpenInBrowser):
			if m.screen == ScreenProject && !m.detailFocusLeft && len(m.detailPRs) > 0 {
				pr := m.detailPRs[m.detailPRCursor]
				return m, func() tea.Msg {
					gh.OpenPRInBrowser(m.detailProject.Path, pr.Number)
					return statusMsgEvent(fmt.Sprintf("Opened PR #%d in browser", pr.Number))
				}
			}

		case key.Matches(msg, keys.Checkout):
			if m.screen == ScreenProject && !m.detailFocusLeft && m.detailPRExpanded >= 0 && m.detailPRExpanded < len(m.detailPRs) {
				pr := m.detailPRs[m.detailPRExpanded]
				projectPath := m.detailProject.Path
				return m, func() tea.Msg {
					if err := gh.CheckoutPRBranch(projectPath, pr.Number); err != nil {
						return statusMsgEvent(fmt.Sprintf("Checkout failed: %v", err))
					}
					return statusMsgEvent(fmt.Sprintf("Checked out branch: %s", pr.Branch))
				}
			}

		case key.Matches(msg, keys.NewWorktree):
			if m.screen == ScreenProject && m.detailProject != nil {
				// If a PR is expanded on the right panel, create worktree from its branch.
				if !m.detailFocusLeft && m.detailPRExpanded >= 0 && m.detailPRExpanded < len(m.detailPRs) {
					pr := m.detailPRs[m.detailPRExpanded]
					return m, m.createWorktreeFromPR(pr)
				}
				// Left panel: create/open a worktree for the branch under cursor.
				if b := m.currentBranchRow(); b != nil {
					return m, m.createWorktreeAndLaunch(b.Name)
				}
			}

		case key.Matches(msg, keys.NewBranchPrompt):
			if m.screen == ScreenProject && m.detailProject != nil {
				ti := textinput.New()
				ti.Placeholder = "new branch name"
				ti.Focus()
				ti.CharLimit = 128
				ti.Width = 40
				m.worktreeInput = &ti
				return m, textinput.Blink
			}

		case key.Matches(msg, keys.OpenInMain):
			if m.screen == ScreenProject && m.detailFocusLeft {
				if b := m.currentBranchRow(); b != nil {
					return m, m.openBranchInMain(b.Name, false)
				}
			}

		case key.Matches(msg, keys.OpenInMainForce):
			if m.screen == ScreenProject && m.detailFocusLeft {
				if b := m.currentBranchRow(); b != nil {
					return m, m.openBranchInMain(b.Name, true)
				}
			}

		case key.Matches(msg, keys.Restart):
			self, err := os.Executable()
			if err == nil {
				// Restart all sidebar panes first
				m.restartSidebars()
				return m, tea.ExecProcess(exec.Command(self), nil)
			}

		case key.Matches(msg, keys.Suspend):
			if m.tmux == nil {
				return m, func() tea.Msg { return statusMsgEvent("tmux not available") }
			}
			tc := m.tmux
			return m, func() tea.Msg {
				if err := tc.DetachClient(); err != nil {
					return statusMsgEvent(fmt.Sprintf("suspend failed: %v", err))
				}
				return nil
			}

		case key.Matches(msg, keys.Quit):
			if m.screen != ScreenDashboard {
				m.screen = ScreenDashboard
				return m, nil
			}
			return m, tea.Quit
		}

	case sessionTickMsg:
		return m, tea.Batch(sessionTick(), m.refreshSessions(), m.refreshGitStatuses())

	case sessionRefreshMsg:
		m.updateProjectStatuses(msg)
		return m, nil

	case usageTickMsg:
		return m, tea.Batch(usageTick(), m.fetchUsage())

	case usageRefreshMsg:
		if msg.authErr {
			m.usageAuth = true
		} else {
			m.usage = msg.snap
			m.usageAuth = false
		}
		m.usageReady = true
		m.writeStateFile()
		return m, nil

	case gitStatusMsg:
		m.gitStatuses = map[string]project.GitStatus(msg)
		// Update ProjectItems with git info
		items := m.list.Items()
		for i, item := range items {
			pi, ok := item.(ProjectItem)
			if !ok {
				continue
			}
			if gs, ok := m.gitStatuses[pi.project.Path]; ok {
				pi.git = gs
				items[i] = pi
			}
		}
		m.list.SetItems(items)
		return m, nil

	case notificationMsg:
		m.handleNotification(notify.Notification(msg))
		cmds := []tea.Cmd{m.refreshSessions()}
		if m.notifServer != nil {
			cmds = append(cmds, m.waitForNotification())
		}
		return m, tea.Batch(cmds...)

	case sessionImportedMsg:
		m.statusMsg = msg.status
		m.list.NewStatusMessage(m.statusMsg)
		// Refresh immediately so the newly-imported session appears in the
		// dashboard / sidebar without waiting for the next 5s tick, and so the
		// state file the sidebar polls is up to date within a cycle.
		return m, tea.Batch(
			m.refreshSessions(),
			tea.Tick(4*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			}),
		)

	case statusMsgEvent:
		m.statusMsg = string(msg)
		m.list.NewStatusMessage(m.statusMsg)
		// Auto-clear success messages after 4s; errors stay until user presses a key
		lower := strings.ToLower(m.statusMsg)
		isError := strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "err:")
		if !isError {
			return m, tea.Tick(4*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			})
		}
		return m, nil

	case clearStatusMsg:
		m.statusMsg = ""
		return m, nil

	case prFetchMsg:
		if msg.err != nil {
			m.detailPRErr = msg.err.Error()
		} else {
			m.detailPRs = msg.prs
		}
		return m, nil

	case prDetailMsg:
		if msg.err == nil {
			m.detailPRDetail = msg.detail
		}
		return m, nil

	case syncPullMsg:
		m.syncedSessions = msg.synced
		m.remoteSynced = msg.remoteOnly
		if m.detailProject != nil {
			// Rebuild rows to reflect newly-pulled sessions AND remote-only
			// entries that the detail view surfaces as "pull + create worktree"
			// rows under their matching branch.
			m.buildDetailRows()
			m.loadRecap()
		}
		if msg.err != nil {
			return m, func() tea.Msg {
				return statusMsgEvent("sync failed: " + msg.err.Error())
			}
		}
		return m, nil

	case worktreeCreatedMsg:
		if m.detailProject != nil {
			m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
			m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
			m.buildDetailRows()
		}
		status := msg.status
		return m, func() tea.Msg { return statusMsgEvent(status) }

	case branchesChangedMsg:
		if m.detailProject != nil {
			m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
			m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
			m.buildDetailRows()
		}
		if msg.status != "" {
			return m, func() tea.Msg { return statusMsgEvent(msg.status) }
		}
		return m, nil
	}

	switch m.screen {
	case ScreenDashboard:
		// Right panel (active sessions): handle up/down ourselves
		if !m.dashFocusLeft {
			if msg, ok := msg.(tea.KeyMsg); ok {
				n := len(m.dashSessionItems)
				if n > 0 {
					switch msg.String() {
					case "up", "k":
						m.dashSessionCursor = (m.dashSessionCursor - 1 + n) % n
					case "down", "j":
						m.dashSessionCursor = (m.dashSessionCursor + 1) % n
					}
				}
			}
			return m, nil
		}
		// Left panel: delegate to bubbles list
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	case ScreenProject:
		if msg, ok := msg.(tea.KeyMsg); ok {
			switch msg.String() {
			case "up", "k":
				if m.detailFocusLeft {
					n := m.detailCombinedLen()
					if n > 0 {
						m.detailCursor = (m.detailCursor - 1 + n) % n
						m.loadRecap()
					}
				} else {
					n := len(m.detailPRs)
					if n > 0 {
						m.detailPRCursor = (m.detailPRCursor - 1 + n) % n
					}
				}
			case "down", "j":
				if m.detailFocusLeft {
					n := m.detailCombinedLen()
					if n > 0 {
						m.detailCursor = (m.detailCursor + 1) % n
						m.loadRecap()
					}
				} else {
					n := len(m.detailPRs)
					if n > 0 {
						m.detailPRCursor = (m.detailPRCursor + 1) % n
					}
				}
			}
		}
	}

	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}

	switch m.screen {
	case ScreenProject:
		return m.projectDetailView()
	case ScreenHelp:
		return m.helpView()
	default:
		return m.dashboardView()
	}
}

// dashSessionItem represents an active session in the dashboard's right panel.
type dashSessionItem struct {
	Name        string
	WindowName  string
	Status      SessionStatus
	ProjectPath string              // cwd — used to resolve external PID/sessionID on import
	Section     string              // "projects" (default) or "external"
	Git         *project.GitStatus  // set for git-backed strays; nil otherwise
}

// refreshDashSessions rebuilds the active sessions list for the dashboard panel.
// The list is split into two sections:
//   - "projects": known workspace projects (and their worktrees) with a live
//     session, plus git-backed strays — claudes running in a git repo we
//     haven't scanned. These render as project rows with branch/dirty info.
//   - "external": strays whose CWD isn't inside any git repo (e.g. ~ or /tmp).
//     Shown separately so they don't clutter project-centric views.
func (m *Model) refreshDashSessions() {
	// Fetch tmux windows once so we can append sibling rows beneath each
	// primary (matching the state-file writer).
	var allWindows []ttmux.Window
	if m.tmux != nil {
		allWindows, _ = m.tmux.ListWindows()
	}

	var items []dashSessionItem
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok || pi.status == StatusNone {
			continue
		}
		items = append(items, dashSessionItem{
			Name:        pi.project.Name,
			WindowName:  pi.project.Name,
			Status:      pi.status,
			ProjectPath: pi.project.Path,
			Section:     "projects",
		})
		items = appendSiblingDashItems(items, allWindows, pi.project.Name, "", pi.project.Path)
	}
	// Active worktree sessions belong in the projects section too.
	for parentPath, wtList := range m.activeWorktrees {
		var projectName string
		for _, it := range m.list.Items() {
			if pi, ok := it.(ProjectItem); ok && pi.project.Path == parentPath {
				projectName = pi.project.Name
				break
			}
		}
		for _, wt := range wtList {
			items = append(items, dashSessionItem{
				Name:        wt.WindowName,
				WindowName:  wt.WindowName,
				Status:      wt.Status,
				ProjectPath: wt.Path,
				Section:     "projects",
			})
			if projectName != "" {
				items = appendSiblingDashItems(items, allWindows, projectName, wt.Branch, wt.Path)
			}
		}
	}
	// Strays: git-backed go into projects with git info, non-git into external.
	// ProjectPath is always the session's CWD so the import flow can find the
	// right PID/sessionID (both maps are CWD-keyed) and launch the new tmux
	// window at exactly where claude was running.
	for _, sr := range m.strays {
		if sr.RepoRoot != "" {
			gs := project.GetGitStatus(sr.RepoRoot)
			items = append(items, dashSessionItem{
				Name:        sr.Name,
				WindowName:  sr.Name,
				Status:      sr.Status,
				ProjectPath: sr.CWD,
				Section:     "projects",
				Git:         &gs,
			})
		} else {
			items = append(items, dashSessionItem{
				Name:        sr.Name,
				WindowName:  sr.Name,
				Status:      sr.Status,
				ProjectPath: sr.CWD,
				Section:     "external",
			})
		}
	}
	m.dashSessionItems = items
	if m.dashSessionCursor >= len(items) {
		m.dashSessionCursor = max(0, len(items)-1)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
	sessions, _ := claude.LiveSessions()
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
			if claude.IsDescendantOf(sessions[i].PID, panePIDs) {
				result[sessions[i].SessionID] = w.Name
			}
		}
	}
	return result
}

// appendSiblingDashItems walks the tmux window list for siblings of a given
// project/branch and appends a dashSessionItem per sibling. Sibling status
// is "active" until a richer per-window status map is introduced.
func appendSiblingDashItems(items []dashSessionItem, windows []ttmux.Window, project, branch, path string) []dashSessionItem {
	for _, w := range windows {
		p, b, suffix, ok := ttmux.ParseWindowName(w.Name)
		if !ok || p != project || b != branch || suffix == "" {
			continue
		}
		items = append(items, dashSessionItem{
			Name:        w.Name,
			WindowName:  w.Name,
			Status:      StatusActive,
			ProjectPath: path,
			Section:     "projects",
		})
	}
	return items
}

func (m Model) dashboardView() string {
	// Build status summary for the title bar
	var statusParts []string
	if m.activeSessions > 0 {
		statusParts = append(statusParts, statusActive.Render(fmt.Sprintf("%d active", m.activeSessions)))
	}
	if m.attentionCount > 0 {
		statusParts = append(statusParts, notifBadgeStyle.Render(fmt.Sprintf("▲%d", m.attentionCount)))
	}
	if len(statusParts) > 0 {
		summary := strings.Join(statusParts, "  ")
		_ = summary
		m.list.Title = "Unky Mo  " + strings.Join(statusParts, "  ")
	}

	// Calculate panel widths
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 120
	}
	rightWidth := 35
	dividerWidth := 3
	leftWidth := totalWidth - rightWidth - dividerWidth
	if leftWidth < 40 {
		leftWidth = 40
	}

	// Resize list to fit the left panel
	footerHeight := 3
	m.list.SetSize(leftWidth, m.height-footerHeight)

	// === Left panel: project list ===
	leftStr := m.list.View()

	// === Right panel: active sessions ===
	var right strings.Builder

	focusIndicator := ""
	if !m.dashFocusLeft {
		focusIndicator = " ◀"
	}
	// Pad top to align with the first project row in the list
	// (list has: title, blank, status line, blank = 4 lines before items)
	right.WriteString("\n\n\n")
	right.WriteString(headerStyle.Render("Active Sessions"+focusIndicator) + "\n")

	if len(m.dashSessionItems) == 0 {
		right.WriteString("  " + footerDescStyle.Render("No active sessions") + "\n")
	} else {
		prevSection := ""
		for i, item := range m.dashSessionItems {
			section := item.Section
			if section == "" {
				section = "projects"
			}
			if section != prevSection {
				label := "Projects"
				if section == "external" {
					label = "External"
				}
				// Blank line between sections; label above the first row.
				if prevSection != "" {
					right.WriteString("\n")
				}
				right.WriteString("  " + footerDescStyle.Render(label) + "\n")
				prevSection = section
			}

			selected := !m.dashFocusLeft && m.dashSessionCursor == i
			cursor := "  "
			if selected {
				cursor = "▸ "
			}

			var dot string
			switch item.Status {
			case StatusActive:
				dot = statusActive.Render(symbolActive)
			case StatusIdle:
				dot = statusIdle.Render(symbolIdle)
			case StatusPermission:
				dot = statusPermission.Render(symbolPermission)
			case StatusExternal:
				dot = statusExternal.Render(symbolActive)
			default:
				dot = statusNone.Render(symbolNone)
			}

			name := item.Name
			maxName := rightWidth - 6
			if maxName > 0 && len(name) > maxName {
				name = name[:maxName-3] + "..."
			}

			var styledName string
			if selected {
				styledName = selectedItemStyle.Render(name)
			} else {
				styledName = normalItemStyle.Render(name)
			}

			suffix := ""
			if item.Status == StatusIdle {
				suffix = " " + statusIdle.Render("idle")
			} else if item.Status == StatusPermission {
				suffix = " " + statusPermission.Render("perm")
			} else if item.Status == StatusExternal {
				suffix = " " + statusExternal.Render("external")
			}
			// Branch/dirty info for git-backed strays.
			if item.Git != nil && item.Git.Branch != "" {
				gitInfo := item.Git.Branch
				if item.Git.Dirty > 0 {
					gitInfo += fmt.Sprintf(" *%d", item.Git.Dirty)
				}
				suffix = " " + footerDescStyle.Render(gitInfo) + suffix
			}

			right.WriteString(cursor + dot + " " + styledName + suffix + "\n")
		}
	}

	rightStr := right.String()

	// Pad panels to same height
	leftLines := strings.Count(leftStr, "\n")
	rightLines := strings.Count(rightStr, "\n")
	maxLines := leftLines
	if rightLines > maxLines {
		maxLines = rightLines
	}
	for i := rightLines; i < maxLines; i++ {
		rightStr += "\n"
	}

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(leftStr)
	divider := lipgloss.NewStyle().Foreground(colorMuted).Render(" │ ")
	rightPanel := lipgloss.NewStyle().Width(rightWidth).Render(rightStr)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, divider, rightPanel)

	usageStrip := m.renderUsageStrip(totalWidth)

	var footer string
	if m.pendingNewMenuActive {
		footer = m.renderPrompt(m.newMenuPromptText(), []footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
			{"esc", "cancel"},
		})
	} else if m.pendingImportSessionID != "" {
		question := fmt.Sprintf("%q is running outside Unky Mo. Import it? (kills the external claude and resumes here)", m.pendingImportProject)
		footer = m.renderPrompt(question, []footerBinding{
			{"y", "yes"},
			{"n", "no"},
		})
	} else {
		footer = m.renderFooter([]footerBinding{
			{"↑↓", "navigate"},
			{"←→", "switch panel"},
			{"enter", "open"},
			{"n", "new session"},
			{"a", "attach"},
			{"/", "filter"},
			{"?", "help"},
			{"s", "suspend"},
			{"q", "quit"},
		})
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		body,
		usageStrip,
		footer,
	)
}

// renderUsageStrip renders the dashboard footer line showing 5h + weekly
// Claude rate-limit-window utilization. Width is clamped so bars never
// underflow on very narrow terminals.
func (m Model) renderUsageStrip(width int) string {
	if width <= 0 {
		width = 80
	}
	if !m.usageReady {
		return usageStripStyle.Width(width).Render("usage: computing…")
	}
	if m.usageAuth {
		return usageStripStyle.Width(width).Render("usage: auth expired — run `claude`")
	}

	fivePct := usage.PctFromUtil(m.usage.FiveHour.Utilization)
	sevenPct := usage.PctFromUtil(m.usage.SevenDay.Utilization)
	now := time.Now()
	fiveResets := usage.FormatResetIn(now, m.usage.FiveHour.ResetsAt)

	// Reserve non-bar characters: "5h  NN% (NhNNm)  ·  Week  NN%" ≈ 36 chars
	// plus borders/padding (~4). Leave the rest for two equal bars.
	fixed := 40
	barWidth := (width - fixed) / 2
	if barWidth < 6 {
		barWidth = 6
	}
	if barWidth > 24 {
		barWidth = 24
	}

	fiveBar := colorBar(m.usage.FiveHour.Utilization, barWidth, fivePct)
	sevenBar := colorBar(m.usage.SevenDay.Utilization, barWidth, sevenPct)

	left := fmt.Sprintf("%s %s %d%% (%s)",
		usageLabelStyle.Render("5h"), fiveBar, fivePct, fiveResets)
	right := fmt.Sprintf("%s %s %d%%",
		usageLabelStyle.Render("Week"), sevenBar, sevenPct)

	line := left + "  ·  " + right
	if m.usage.Source == "stale" {
		line += "  " + barEmptyStyle.Render("(stale)")
	}
	return usageStripStyle.Width(width).Render(line)
}

// colorBar splits the bar into filled/empty segments and colors them
// independently so the filled portion can switch to warn/danger styles
// at high utilization without tinting the empty portion.
func colorBar(util float64, width, pct int) string {
	filled, empty := usage.SplitBar(util, width)
	return pickBarStyle(pct).Render(filled) + barEmptyStyle.Render(empty)
}

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
			{"w", "Open branch under cursor as a worktree"},
			{"W", "Prompt for a new branch name + worktree"},
			{"m", "Check out branch in main repo (refuse if dirty)"},
			{"M", "Stash first, then check out in main"},
		}},
		{"Other", []footerBinding{
			{"s", "Suspend (leaves tmux session running; re-launch mo to resume)"},
			{"?", "Toggle this help"},
			{"ctrl+r", "Restart (reload new binary)"},
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
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	line := strings.Join(parts, "  ")
	return footerStyle.Width(m.width).Render(line)
}

// strayLive describes a live claude session whose CWD doesn't match any
// scanned workspace project. Git-backed strays render in the "Projects"
// section (with a synthetic project row); non-git strays render in the
// "External" section of the dashboard and sidebar.
type strayLive struct {
	SessionID string
	PID       int
	CWD       string
	RepoRoot  string // "" means non-git (CWD isn't inside any git repo)
	Name      string // repo basename if git-backed, else CWD basename
	Status    SessionStatus
}

// sessionRefreshMsg carries the detected status for each project path with a
// live session. External (outside-tmux) sessions also carry their PID + sessionID
// so the "import into mo" flow can kill the orphan and resume the conversation.
// Strays are sessions whose CWD doesn't map to any known project.
type sessionRefreshMsg struct {
	statuses         map[string]SessionStatus
	externalPIDs     map[string]int    // path → orphan PID to kill on import
	externalSessions map[string]string // path → sessionID to resume
	strays           []strayLive
}

func (m Model) refreshSessions() tea.Cmd {
	knownPaths := make(map[string]bool)
	for _, item := range m.list.Items() {
		if pi, ok := item.(ProjectItem); ok {
			knownPaths[pi.project.Path] = true
		}
	}
	return func() tea.Msg {
		sessions, _ := claude.LiveSessions()
		var hostPIDs map[int]bool
		if m.tmux != nil {
			hostPIDs, _ = m.tmux.PanePIDs()
		}
		statuses := make(map[string]SessionStatus)
		externalPIDs := make(map[string]int)
		externalSessions := make(map[string]string)
		var strays []strayLive
		for _, s := range sessions {
			isExternal := len(hostPIDs) > 0 && !claude.IsDescendantOf(s.PID, hostPIDs)

			status := StatusActive
			if isExternal {
				status = StatusExternal
			} else if claude.IsSessionIdle(s.CWD, s.SessionID) {
				status = StatusIdle
			}

			// Known project or worktree dir → record status by CWD so
			// updateProjectStatuses can attach it to the right row.
			if _, known := knownPaths[s.CWD]; known || strings.Contains(s.CWD, ".worktrees/") {
				statuses[s.CWD] = status
				if isExternal {
					externalPIDs[s.CWD] = s.PID
					externalSessions[s.CWD] = s.SessionID
				}
				continue
			}

			// Stray session: classify as git-backed or not and emit a row.
			// Key externalPIDs/Sessions by the session's CWD so the import
			// flow launches the new tmux window at the exact directory the
			// original claude was running in (not the repo root).
			repoRoot := project.FindGitRoot(s.CWD)
			// If the repo root matches a scanned project, attribute to that
			// project rather than duplicating it as a stray row.
			if repoRoot != "" {
				if _, known := knownPaths[repoRoot]; known {
					statuses[repoRoot] = status
					if isExternal {
						externalPIDs[repoRoot] = s.PID
						externalSessions[repoRoot] = s.SessionID
					}
					continue
				}
			}
			name := filepath.Base(s.CWD)
			if repoRoot != "" {
				name = filepath.Base(repoRoot)
			}
			// A user-chosen session label (via `claude --name` or `/name`)
			// lives in the PID file's `name` field — prefer it over the
			// directory-derived fallback so renames show up in the dashboard
			// and sidebar.
			if s.Name != "" {
				name = s.Name
			}
			strays = append(strays, strayLive{
				SessionID: s.SessionID,
				PID:       s.PID,
				CWD:       s.CWD,
				RepoRoot:  repoRoot,
				Name:      name,
				Status:    status,
			})
			if isExternal {
				externalPIDs[s.CWD] = s.PID
				externalSessions[s.CWD] = s.SessionID
			}
		}
		return sessionRefreshMsg{
			statuses:         statuses,
			externalPIDs:     externalPIDs,
			externalSessions: externalSessions,
			strays:           strays,
		}
	}
}

func (m *Model) updateProjectStatuses(polled sessionRefreshMsg) {
	items := m.list.Items()
	activeCount := 0
	attentionCount := 0

	// Build set of known project paths for worktree matching
	projectPaths := make(map[string]string) // projectPath → projectName
	for _, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		projectPaths[pi.project.Path] = pi.project.Name
	}

	// Stash external-session metadata so the import prompt can reach it later.
	m.externalPIDs = polled.externalPIDs
	m.externalSessions = polled.externalSessions
	m.strays = polled.strays

	// Detect worktree sessions: CWDs containing ".worktrees/" that map to a known project
	worktrees := make(map[string][]WorktreeStatus)
	for cwd, status := range polled.statuses {
		if idx := strings.Index(cwd, ".worktrees/"); idx >= 0 {
			parentPath := cwd[:idx+len(".worktrees/")-1] // strip trailing "/"
			parentPath = strings.TrimSuffix(parentPath, ".worktrees")
			if projectName, ok := projectPaths[parentPath]; ok {
				branch := filepath.Base(cwd)
				worktrees[parentPath] = append(worktrees[parentPath], WorktreeStatus{
					Branch:     branch,
					Path:       cwd,
					WindowName: projectName + "@" + branch,
					Status:     status,
				})
			}
		}
	}
	m.activeWorktrees = worktrees

	for i, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}

		// Notification-based status takes priority for permission prompts
		if notifStatus, ok := m.notifState[pi.project.Path]; ok && notifStatus == StatusPermission {
			pi.status = StatusPermission
			attentionCount++
			activeCount++
		} else if polledStatus, ok := polled.statuses[pi.project.Path]; ok {
			// Use poll-based status (detects idle from JSONL, or external marker)
			pi.status = polledStatus
			switch polledStatus {
			case StatusIdle:
				attentionCount++
				activeCount++
			case StatusExternal:
				// External sessions aren't "mo sessions" — don't count as active.
			default:
				activeCount++
			}
		} else {
			pi.status = StatusNone
		}

		// Count active worktree sessions for this project
		for _, wt := range worktrees[pi.project.Path] {
			activeCount++
			if wt.Status == StatusIdle {
				attentionCount++
			}
		}

		items[i] = pi
	}

	m.activeSessions = activeCount
	m.attentionCount = attentionCount
	m.list.SetItems(items)
	m.refreshDashSessions()
	m.syncSiblingTitles()
	m.writeStateFile()
}

// syncSiblingTitles reads the latest custom-title entry for each live sibling
// session (tmux windows whose parsed name has a non-empty suffix) and renames
// the tmux window to match. Primary windows (no suffix) are never renamed so
// existing code that looks windows up by "project" or "project@branch" keeps
// working. Skips renames that would collide with another window's name.
func (m *Model) syncSiblingTitles() {
	if m.tmux == nil {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	sessions, _ := claude.LiveSessions()
	if len(sessions) == 0 {
		return
	}
	existingNames := make(map[string]bool, len(windows))
	for _, w := range windows {
		existingNames[w.Name] = true
	}
	for _, w := range windows {
		project, branch, suffix, ok := ttmux.ParseWindowName(w.Name)
		if !ok || suffix == "" {
			continue
		}
		panePIDs, err := m.tmux.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		var sess *claude.Session
		for i := range sessions {
			if claude.IsDescendantOf(sessions[i].PID, panePIDs) {
				sess = &sessions[i]
				break
			}
		}
		if sess == nil {
			continue
		}
		title := claude.CustomTitleFor(sess.CWD, sess.SessionID)
		if title == "" || title == suffix {
			continue
		}
		desired := ttmux.ComposeWindowName(project, branch, title)
		if desired == w.Name || existingNames[desired] {
			continue
		}
		if err := m.tmux.RenameWindow(w.ID, desired); err == nil {
			delete(existingNames, w.Name)
			existingNames[desired] = true
		}
	}
}

func (m *Model) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}

	// Fetch all tmux windows once so we can append per-project/per-worktree
	// sibling entries (windows whose parsed name has a non-empty suffix).
	// In the single-session world this loop produces nothing and the output
	// is identical to before.
	var allWindows []ttmux.Window
	if m.tmux != nil {
		allWindows, _ = m.tmux.ListWindows()
	}

	var projects []state.ProjectState
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		var statusStr string
		switch pi.status {
		case StatusActive:
			statusStr = "active"
		case StatusIdle:
			statusStr = "idle"
		case StatusPermission:
			statusStr = "permission"
		case StatusExternal:
			statusStr = "external"
		default:
			statusStr = "none"
		}
		projects = append(projects, state.ProjectState{
			Name:       pi.project.Name,
			Path:       pi.project.Path,
			WindowName: pi.project.Name,
			Status:     statusStr,
			Section:    "projects",
		})

		// Sibling sessions for this project's main checkout (no branch).
		projects = appendSiblingEntries(projects, allWindows, pi.project.Name, "", pi.project.Path, pi.project.Name, "")

		// Append worktree entries for this project
		for _, wt := range m.activeWorktrees[pi.project.Path] {
			wtStatus := "active"
			switch wt.Status {
			case StatusIdle:
				wtStatus = "idle"
			case StatusPermission:
				wtStatus = "permission"
			}
			projects = append(projects, state.ProjectState{
				Name:       "@" + wt.Branch,
				Path:       wt.Path,
				WindowName: wt.WindowName,
				Status:     wtStatus,
				Parent:     pi.project.Name,
				Section:    "projects",
			})

			// Sibling sessions for this worktree's branch.
			projects = appendSiblingEntries(projects, allWindows, pi.project.Name, wt.Branch, wt.Path, "@"+wt.Branch, pi.project.Name)
		}
	}

	// Strays: git-backed go into the projects section with branch info;
	// non-git strays go into a dedicated external section so the sidebar
	// can group them below known projects.
	for _, sr := range m.strays {
		status := "active"
		switch sr.Status {
		case StatusIdle:
			status = "idle"
		case StatusPermission:
			status = "permission"
		case StatusExternal:
			status = "external"
		}
		if sr.RepoRoot != "" {
			gs := project.GetGitStatus(sr.RepoRoot)
			projects = append(projects, state.ProjectState{
				Name:       sr.Name,
				Path:       sr.CWD, // launch path on import, not the repo root
				WindowName: sr.Name,
				Status:     status,
				Section:    "projects",
				Branch:     gs.Branch,
				Dirty:      gs.Dirty,
			})
		} else {
			projects = append(projects, state.ProjectState{
				Name:       sr.Name,
				Path:       sr.CWD,
				WindowName: sr.Name,
				Status:     status,
				Section:    "external",
			})
		}
	}

	sessionName := ""
	if m.tmux != nil {
		sessionName = m.tmux.SessionName
	}

	sf := &state.StateFile{
		TmuxSession: sessionName,
		Projects:    projects,
	}
	if m.usageReady {
		sf.Usage = &state.UsageState{
			FiveHourPct:      usage.PctFromUtil(m.usage.FiveHour.Utilization),
			FiveHourResetsAt: m.usage.FiveHour.ResetsAt,
			SevenDayPct:      usage.PctFromUtil(m.usage.SevenDay.Utilization),
			SevenDayResetsAt: m.usage.SevenDay.ResetsAt,
			FetchedAt:        m.usage.FetchedAt,
			Stale:            m.usage.Source == "stale",
			AuthError:        m.usageAuth,
		}
	}
	state.Write(m.stateFilePath, sf)
}

// appendSiblingEntries walks the tmux window list for siblings of a given
// project/branch (windows whose parsed name has the same project+branch
// but a non-empty suffix) and appends one state.ProjectState per sibling.
// rowName + parent mirror the fields used by the primary entry so the
// sidebar groups siblings under the same header. The suffix is folded into
// the display Name (e.g. "unky-mo [2]", "@feature [debug-oauth]") so the
// sidebar can distinguish siblings from the primary without parsing window
// names itself.
func appendSiblingEntries(projects []state.ProjectState, windows []ttmux.Window, project, branch, path, rowName, parent string) []state.ProjectState {
	for _, w := range windows {
		p, b, suffix, ok := ttmux.ParseWindowName(w.Name)
		if !ok || p != project || b != branch || suffix == "" {
			continue
		}
		idx := 0
		if n, err := strconv.Atoi(suffix); err == nil {
			idx = n
		}
		projects = append(projects, state.ProjectState{
			Name:       rowName + " [" + suffix + "]",
			Path:       path,
			WindowName: w.Name,
			Status:     "active",
			Parent:     parent,
			Section:    "projects",
			Index:      idx,
		})
	}
	return projects
}

func (m Model) projectDetailView() string {
	if m.detailProject == nil {
		return "No project selected"
	}
	p := m.detailProject

	// Header (full width)
	title := titleStyle.Render(" ← " + p.Name + " ")
	lang := p.Language
	if lang == "" {
		lang = "unknown"
	}
	header := title + "  " + langStyle.Render("["+lang+"]") + "  " + footerDescStyle.Render(p.Path) + "\n\n"

	// Calculate panel widths
	dividerWidth := 3 // " │ "
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 80
	}
	rightWidth := totalWidth * 2 / 5
	if rightWidth < 25 {
		rightWidth = 25
	}
	leftWidth := totalWidth - rightWidth - dividerWidth

	// === LEFT PANEL: Sessions + Worktrees with nested sessions ===
	var left strings.Builder

	focusIndicator := ""
	if m.detailFocusLeft {
		focusIndicator = " ◀"
	}

	// Render using detailRows
	lastKind := ""
	for i, row := range m.detailRows {
		selected := m.detailFocusLeft && m.detailCursor == i

		switch row.kind {
		case "branch":
			if lastKind == "" {
				left.WriteString(headerStyle.Render("Branches"+focusIndicator) + "\n")
			} else if lastKind != "branch" {
				left.WriteString("\n")
			}
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			marker := "·"
			switch {
			case row.branch.IsMain:
				marker = "●"
			case row.branch.WorktreePath != "":
				marker = "⎇"
			}
			age := formatAge(time.Since(row.branch.LastCommit))
			label := fmt.Sprintf("%s%s %s  %s", cursor, marker, row.branch.Name, age)
			if selected {
				left.WriteString(selectedItemStyle.Render(label) + "\n")
			} else {
				left.WriteString(headerStyle.Render(label) + "\n")
			}

		case "br-session":
			left.WriteString("  " + m.renderSessionRow(row.session, row.path, selected, leftWidth-2) + "\n")

		case "br-empty":
			left.WriteString("    " + footerDescStyle.Render("(no sessions)") + "\n")

		case "br-remote":
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			title := row.remoteMeta.Title
			if title == "" {
				title = "(untitled)"
			}
			age := formatAge(time.Since(row.remoteMeta.PushedAt))
			host := row.remoteMeta.Hostname
			if host == "" {
				host = "?"
			}
			line := fmt.Sprintf("%s⇅ %s %s  %s%s  %s",
				cursor, statusNone.Render("○"), age, title,
				"  "+footerDescStyle.Render(host),
				footerDescStyle.Render("(remote only)"))
			if selected {
				left.WriteString("  " + selectedItemStyle.Render(line) + "\n")
			} else {
				left.WriteString("  " + normalItemStyle.Render(line) + "\n")
			}
		}

		lastKind = row.kind
	}

	if len(m.detailRows) == 0 {
		left.WriteString(headerStyle.Render("Branches"+focusIndicator) + "\n")
		left.WriteString("  " + footerDescStyle.Render("No branches found") + "\n")
	}

	// Session recap preview
	if len(m.detailRecap) > 0 && m.detailFocusLeft {
		left.WriteString("\n" + headerStyle.Render("Last messages") + "\n")
		for _, msg := range m.detailRecap {
			role := footerKeyStyle.Render("You:")
			if msg.Role == "assistant" {
				role = statusActive.Render("Claude:")
			}
			content := msg.Content
			maxLen := leftWidth - 12
			if maxLen > 0 && len(content) > maxLen {
				content = content[:maxLen-3] + "..."
			}
			left.WriteString(fmt.Sprintf("  %s %s\n", role, footerDescStyle.Render(content)))
		}
	}

	// === RIGHT PANEL: Pull Requests ===
	var right strings.Builder

	prFocusIndicator := ""
	if !m.detailFocusLeft {
		prFocusIndicator = " ◀"
	}
	right.WriteString(headerStyle.Render("Pull Requests"+prFocusIndicator) + "\n")

	if m.detailPRErr != "" {
		right.WriteString("  " + footerDescStyle.Render(m.detailPRErr) + "\n")
	} else if m.detailPRs == nil {
		right.WriteString("  " + footerDescStyle.Render("Loading...") + "\n")
	} else if len(m.detailPRs) == 0 {
		right.WriteString("  " + footerDescStyle.Render("No open pull requests") + "\n")
	} else {
		for i, pr := range m.detailPRs {
			selected := !m.detailFocusLeft && m.detailPRCursor == i
			expanded := m.detailPRExpanded == i
			cursor := "  "
			if selected {
				cursor = "▸ "
			}

			num := fmt.Sprintf("#%d", pr.Number)
			prTitle := pr.Title
			maxTitle := rightWidth - len(num) - 5
			if maxTitle < 10 {
				maxTitle = 10
			}
			if len(prTitle) > maxTitle {
				prTitle = prTitle[:maxTitle-3] + "..."
			}

			line := fmt.Sprintf("%s%s %s", cursor, langStyle.Render(num), prTitle)
			if selected {
				right.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				right.WriteString(normalItemStyle.Render(line) + "\n")
			}

			// Expanded detail view
			if expanded {
				right.WriteString(m.renderPRDetail(pr, rightWidth) + "\n")
			}
		}
	}

	// === Combine panels ===
	// Pad both panels to the same height
	leftStr := left.String()
	rightStr := right.String()
	leftLines := strings.Count(leftStr, "\n")
	rightLines := strings.Count(rightStr, "\n")
	maxLines := leftLines
	if rightLines > maxLines {
		maxLines = rightLines
	}
	for i := leftLines; i < maxLines; i++ {
		leftStr += "\n"
	}
	for i := rightLines; i < maxLines; i++ {
		rightStr += "\n"
	}

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(leftStr)
	divider := lipgloss.NewStyle().Foreground(colorMuted).Render(" │ ")
	rightPanel := lipgloss.NewStyle().Width(rightWidth).Render(rightStr)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, divider, rightPanel)

	// Status message
	if m.statusMsg != "" {
		body += "\n" + notifBadgeStyle.Render(m.statusMsg)
	}

	// Footer
	var footer string
	switch {
	case m.worktreeInput != nil:
		question := fmt.Sprintf("Create new worktree for %s — branch name:", p.Name)
		footer = m.renderInputPrompt(question, m.worktreeInput.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	case m.pendingNewMenuActive:
		footer = m.renderPrompt(m.newMenuPromptText(), []footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
			{"esc", "cancel"},
		})
	case m.pendingResumeSessionID != "":
		question := fmt.Sprintf("A session is already running for %s. Disconnect it and start the selected session?", p.Name)
		footer = m.renderPrompt(question, []footerBinding{
			{"y", "yes"},
			{"n", "no"},
		})
	default:
		var bindings []footerBinding
		if !m.detailFocusLeft && m.detailPRExpanded >= 0 {
			bindings = []footerBinding{
				{"o", "github"},
				{"w", "worktree"},
				{"c", "checkout"},
				{"enter", "close"},
				{"←→", "switch panel"},
				{"esc", "back"},
			}
		} else {
			bindings = []footerBinding{
				{"↑↓", "select"},
				{"←→", "panel"},
				{"enter", "resume"},
				{"w", "worktree"},
				{"m", "main"},
				{"M", "stash+main"},
				{"W", "new branch"},
				{"n", "session"},
				{"o", "PR"},
				{"s", "suspend"},
				{"esc", "back"},
			}
		}
		footer = m.renderFooter(bindings)
	}

	// Pad content to fill screen, then add footer
	content := header + body
	contentLines := strings.Count(content, "\n")
	footerLines := 3
	if contentLines < m.height-footerLines {
		content += strings.Repeat("\n", m.height-footerLines-contentLines)
	}

	return content + footer
}

func (m Model) renderSessionRow(rs *claude.RecentSession, launchPath string, selected bool, maxWidth int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}

	var statusStr string
	if rs.IsLive {
		statusStr = statusActive.Render("●")
	} else {
		statusStr = statusNone.Render("○")
	}

	age := formatAge(time.Since(rs.LastActive))
	name := rs.DisplayName()

	tokStr := ""
	if launchPath != "" && rs.SessionID != "" {
		jsonl := filepath.Join(claude.ProjectsDirForPath(launchPath), rs.SessionID+".jsonl")
		tokStr = usage.FormatTokensShort(usage.SessionTokens(jsonl))
	}

	maxName := maxWidth - 24
	if maxName < 10 {
		maxName = 10
	}
	if len(name) > maxName {
		name = name[:maxName-3] + "..."
	}

	// Sync indicator lives as a leading column so synced and non-synced rows
	// align. Host + push age hang off the end as secondary metadata.
	syncPrefix := "  "
	syncTrailer := ""
	if meta, ok := m.syncedSessions[rs.SessionID]; ok {
		syncPrefix = "⇅ "
		if meta.Hostname != "" {
			syncTrailer = "  " + footerDescStyle.Render(meta.Hostname)
		}
		if !meta.PushedAt.IsZero() {
			syncTrailer += "  " + footerDescStyle.Render(formatAge(time.Since(meta.PushedAt)))
		}
	}

	tokCol := ""
	if tokStr != "" {
		tokCol = "  " + footerDescStyle.Render(tokStr)
	}

	line := fmt.Sprintf("%s%s%s %s  %s%s%s", cursor, syncPrefix, statusStr, age, name, tokCol, syncTrailer)
	if selected {
		return selectedItemStyle.Render(line)
	}
	return normalItemStyle.Render(line)
}

// detailRow represents one navigable item in the project detail view. Rows
// are organized as: one "branch" header per local branch, followed by
// "br-session" rows for each session discovered at that branch's checkout
// location. Branches that are neither the main checkout nor a worktree show
// a single "br-empty" row, or — when a synced session exists for the branch
// on another machine — a "br-remote" row.
type detailRow struct {
	kind       string                // "branch", "br-session", "br-empty", "br-remote"
	session    *claude.RecentSession // non-nil for br-session
	branch     *project.Branch       // non-nil for all branch-scoped rows
	path       string                // cwd to launch/resume in ("" if branch has no local checkout)
	remoteMeta *moSync.SessionMeta   // non-nil for br-remote
	// tmuxWindow is the real tmux window name currently running this row's
	// session, if any. Populated by buildDetailRows from a live windows ×
	// sessions cross-reference. Empty when the session has no live window
	// (historical resume). Lets the `enter` handler switch directly to the
	// correct sibling instead of recomputing project@branch.
	tmuxWindow string
}

// buildDetailRows constructs the flat list of navigable rows for the project
// detail view. Main-project sessions live under the main branch's row.
func (m *Model) buildDetailRows() {
	p := m.detailProject
	if p == nil {
		m.detailRows = nil
		return
	}

	// Build sessionID → tmux window name so each br-session row points at
	// the correct live window (primary or sibling). An empty result means
	// the session is historical-only and `enter` will launch a fresh window.
	windowBySession := m.sessionToWindowMap()

	var rows []detailRow
	for i := range m.detailBranches {
		b := &m.detailBranches[i]
		launchPath := ""
		switch {
		case b.WorktreePath != "":
			launchPath = b.WorktreePath
		case b.IsMain:
			launchPath = p.Path
		}
		rows = append(rows, detailRow{
			kind:   "branch",
			branch: b,
			path:   launchPath,
		})

		if launchPath == "" {
			if meta, ok := m.remoteSynced[b.Name]; ok {
				metaCopy := meta
				rows = append(rows, detailRow{
					kind:       "br-remote",
					branch:     b,
					remoteMeta: &metaCopy,
				})
			} else {
				rows = append(rows, detailRow{
					kind:   "br-empty",
					branch: b,
				})
			}
			continue
		}

		sessions := claude.RecentSessions(launchPath, 5)
		if len(sessions) == 0 {
			rows = append(rows, detailRow{
				kind:   "br-empty",
				branch: b,
				path:   launchPath,
			})
			continue
		}
		for j := range sessions {
			rows = append(rows, detailRow{
				kind:       "br-session",
				session:    &sessions[j],
				branch:     b,
				path:       launchPath,
				tmuxWindow: windowBySession[sessions[j].SessionID],
			})
		}
	}

	// Surface remote-only entries whose branch isn't in the local branch
	// list at all (e.g. the branch was never fetched). Group them under a
	// synthetic "Remote branches" header so the user can still pull them.
	known := map[string]bool{}
	for _, b := range m.detailBranches {
		known[b.Name] = true
	}
	var orphanBranches []string
	for branch := range m.remoteSynced {
		if branch == "" || known[branch] {
			continue
		}
		orphanBranches = append(orphanBranches, branch)
	}
	if len(orphanBranches) > 0 {
		sort.Strings(orphanBranches)
		for _, branch := range orphanBranches {
			meta := m.remoteSynced[branch]
			metaCopy := meta
			b := &project.Branch{Name: branch}
			rows = append(rows, detailRow{
				kind:   "branch",
				branch: b,
			})
			rows = append(rows, detailRow{
				kind:       "br-remote",
				branch:     b,
				remoteMeta: &metaCopy,
			})
		}
	}

	m.detailRows = rows
}

// detailCombinedLen returns the number of navigable rows.
func (m Model) detailCombinedLen() int {
	return len(m.detailRows)
}

// loadRecap loads the last few messages for the session at the current cursor.
func (m *Model) loadRecap() {
	m.detailRecap = nil
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return
	}
	row := m.detailRows[m.detailCursor]
	if row.session == nil {
		return
	}
	m.detailRecap = claude.LastMessages(row.path, row.session.SessionID, 6)
}

// renderPrompt renders a two-row confirmation bar: the question on top, key
// hints on the bottom. Uses the same styling as the normal footer.
func (m Model) renderPrompt(question string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + footerDescStyle.Render(hints)
	return footerStyle.Width(m.width).Render(body)
}

// renderInputPrompt renders a three-row input bar: the question, a text-input
// line, and key hints. Used by the new-worktree flow.
func (m Model) renderInputPrompt(question, inputView string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + inputView + "\n" + footerDescStyle.Render(hints)
	return footerStyle.Width(m.width).Render(body)
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m *Model) handleNotification(n notify.Notification) {
	switch n.Type {
	case notify.NotifyIdlePrompt:
		m.notifState[n.ProjectPath] = StatusIdle
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs input", filepath.Base(n.ProjectPath)))
	case notify.NotifyPermissionPrompt:
		m.notifState[n.ProjectPath] = StatusPermission
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs permission", filepath.Base(n.ProjectPath)))
	case notify.NotifySessionStop:
		// Clear notification status when session completes a turn
		delete(m.notifState, n.ProjectPath)
	}
}

// restartSidebars sends ctrl+r to all sidebar panes (pane .1 in each window)
// so they reload the new binary.
func (m Model) restartSidebars() {
	if m.tmux == nil {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	for _, w := range windows {
		if w.Index == "0" {
			continue // skip the TUI window itself
		}
		target := fmt.Sprintf("%s:%s.1", m.tmux.SessionName, w.Index)
		// Send ctrl+r to the sidebar pane — its own handler will exec the new binary
		m.tmux.SendRawKeys(target, "C-r")
	}
}

// addSidebarPane splits off a sidebar pane in the given window target
// with the given cwd, then refocuses back to the main (left) pane.
func (m Model) addSidebarPane(target, cwd string) {
	if m.tmux == nil {
		return
	}
	moPath, err := os.Executable()
	if err != nil {
		return
	}
	sidebarCmd := fmt.Sprintf("%s sidebar", moPath)
	m.tmux.SplitWindow(target, 42, cwd, sidebarCmd)
	// Refocus to the main pane (left/first pane)
	m.tmux.SelectPane(target + ".0")
}

type statusMsgEvent string
type clearStatusMsg struct{}

// worktreeCreatedMsg signals that a new worktree was created; Update refreshes
// detailWorktrees and surfaces the carried status string.
type worktreeCreatedMsg struct{ status string }

// branchesChangedMsg signals that local branch state (checkouts, stashes,
// new branches) has moved and the detail view's branch list should be
// rebuilt. A non-empty status is surfaced to the user.
type branchesChangedMsg struct{ status string }

func (m Model) renderPRDetail(pr gh.PullRequest, maxWidth int) string {
	var b strings.Builder
	indent := "  │ "

	if m.detailPRDetail != nil && m.detailPRDetail.Number == pr.Number {
		d := m.detailPRDetail

		// Title + author + branch
		b.WriteString(indent + selectedItemStyle.Render(d.Title) + "\n")
		b.WriteString(indent + footerDescStyle.Render(fmt.Sprintf("by %s  %s → %s", d.Author.Login, d.Branch, d.BaseBranch)) + "\n")

		// Stats + review
		stats := fmt.Sprintf("+%d -%d", d.Additions, d.Deletions)
		review := d.ReviewDecision
		if review == "" {
			review = "no reviews"
		}
		b.WriteString(indent + statusActive.Render(stats) + "  " + footerDescStyle.Render(review) + "\n")

		// Body (first few lines)
		if d.Body != "" {
			bodyLines := strings.Split(d.Body, "\n")
			maxLines := 4
			if len(bodyLines) > maxLines {
				bodyLines = bodyLines[:maxLines]
			}
			for _, line := range bodyLines {
				line = strings.TrimSpace(line)
				maxLen := maxWidth - len(indent) - 1
				if maxLen > 0 && len(line) > maxLen {
					line = line[:maxLen-3] + "..."
				}
				b.WriteString(indent + footerDescStyle.Render(line) + "\n")
			}
		}

		b.WriteString(indent + "\n")
		b.WriteString(indent + footerKeyStyle.Render("o") + ":" + footerDescStyle.Render("github") + "  ")
		b.WriteString(footerKeyStyle.Render("w") + ":" + footerDescStyle.Render("worktree") + "  ")
		b.WriteString(footerKeyStyle.Render("c") + ":" + footerDescStyle.Render("checkout") + "  ")
		b.WriteString(footerKeyStyle.Render("⏎") + ":" + footerDescStyle.Render("close") + "\n")
	} else {
		b.WriteString(indent + footerDescStyle.Render("Loading...") + "\n")
	}

	return b.String()
}

func (m Model) createWorktreeFromPR(pr gh.PullRequest) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		// Fetch the PR branch from the remote first, then create worktree
		_ = exec.Command("git", "-C", p.Path, "fetch", "origin", pr.Branch).Run()

		// Create the worktree for the PR branch
		wtPath, err := project.CreateWorktree(p.Path, pr.Branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}

		// Reset the worktree to the remote branch to ensure it's up to date
		_ = exec.Command("git", "-C", wtPath, "reset", "--hard", "origin/"+pr.Branch).Run()

		windowName := p.Name + "@" + pr.Branch
		var status string
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Worktree ready but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchClaudeInWindow(windowName, wtPath, "claude")
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
	}
}

// prFetchMsg carries the result of an async PR fetch.
type prFetchMsg struct {
	prs []gh.PullRequest
	err error
}

func (m Model) fetchPRs(projectPath string) tea.Cmd {
	return func() tea.Msg {
		prs, err := gh.ListPRs(projectPath)
		return prFetchMsg{prs: prs, err: err}
	}
}

// prDetailMsg carries the result of fetching a single PR's detail.
type prDetailMsg struct {
	detail *gh.PRDetail
	err    error
}

func (m Model) fetchPRDetail(number int) tea.Cmd {
	return func() tea.Msg {
		if m.detailProject == nil {
			return prDetailMsg{err: fmt.Errorf("no project")}
		}
		detail, err := gh.GetPRDetail(m.detailProject.Path, number)
		return prDetailMsg{detail: detail, err: err}
	}
}

// syncPullMsg carries the result of an auto-sync-pull when opening a project.
type syncPullMsg struct {
	synced     map[string]moSync.SessionMeta // sync metadata for sessions pulled into a local path, keyed by session ID
	remoteOnly map[string]moSync.SessionMeta // sessions for branches with no local worktree, keyed by branch name
	pulled     bool                          // true if a new session was pulled
	err        error                         // non-nil when sync is configured but failed
}

func (m Model) autoSyncPull(projectName, projectPath string) tea.Cmd {
	return func() tea.Msg {
		syncDir := moSync.DefaultSyncDir()
		result := syncPullMsg{
			synced:     make(map[string]moSync.SessionMeta),
			remoteOnly: make(map[string]moSync.SessionMeta),
		}

		// Stay quiet when the user hasn't set up sync on this machine.
		if !moSync.IsConfigured(syncDir) {
			return result
		}

		// List what's in the sync repo for this project
		sessions, err := moSync.List(syncDir)
		if err != nil {
			result.err = err
			return result
		}

		// Map each sync project name → local filesystem path. Main-project
		// entries map to projectPath; worktree entries ("<project>@<branch>")
		// resolve to a local worktree whose branch matches the suffix.
		pathFor := map[string]string{projectName: projectPath}
		worktrees, _ := project.ListWorktrees(projectPath)
		prefix := projectName + "@"
		for _, wt := range worktrees {
			if wt.Path != projectPath && wt.Branch != "" {
				pathFor[prefix+wt.Branch] = wt.Path
			}
		}

		for _, s := range sessions {
			// Only care about entries belonging to this project (main + worktrees).
			if s.ProjectName != projectName && !strings.HasPrefix(s.ProjectName, prefix) {
				continue
			}

			localPath, ok := pathFor[s.ProjectName]
			if !ok {
				// Worktree doesn't exist locally — remember it so the detail
				// view can surface a "pull + create worktree" action.
				branch := strings.TrimPrefix(s.ProjectName, prefix)
				result.remoteOnly[branch] = s
				continue
			}
			result.synced[s.SessionID] = s

			// Pull the session if we don't have it locally
			localDir := claude.ProjectsDirForPath(localPath)
			jsonlPath := filepath.Join(localDir, s.SessionID+".jsonl")
			if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
				if _, err := moSync.Pull(s.ProjectName, localPath, syncDir); err != nil {
					if result.err == nil {
						result.err = fmt.Errorf("%s: %w", s.ProjectName, err)
					}
					continue
				}
				result.pulled = true
			}
		}

		return result
	}
}

// currentProject returns the project for the current context —
// detailProject on the project detail screen, list selection on the dashboard.
func (m Model) currentProject() *project.Project {
	if m.detailProject != nil && m.screen == ScreenProject {
		return m.detailProject
	}
	item, ok := m.list.SelectedItem().(ProjectItem)
	if !ok {
		return nil
	}
	return &item.project
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
	return true, m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName)
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
		return m.launchClaudeInWindow(windowName, cwd, "claude")
	}
}

// newMenuPromptText renders the question shown above the s/p/c/esc menu.
// Names the primary target so the user knows what they're about to affect.
func (m Model) newMenuPromptText() string {
	target := m.pendingNewPrimaryWin
	if target == "" {
		target = "this target"
	}
	return fmt.Sprintf("A session is already running in %s — what would you like to do?", target)
}

// clearPendingNewMenu resets all pendingNew* fields so the menu is dismissed.
func (m *Model) clearPendingNewMenu() {
	m.pendingNewMenuActive = false
	m.pendingNewProject = ""
	m.pendingNewBranch = ""
	m.pendingNewCwd = ""
	m.pendingNewPrimaryWin = ""
	m.pendingNewLivePID = 0
	m.pendingNewLiveID = ""
}

// parkAndLaunchPrimary signals the given Claude PID to exit, waits briefly
// for it to die, explicitly kills its tmux window so the sidebar and any
// terminal-drawer panes go with it, and launches a fresh Claude session in
// a new window with the same primary name.
func (m Model) parkAndLaunchPrimary(pid int, primaryWindowName, cwd string) tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		if pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGINT)
			}
			// Wait up to ~2s for claude to flush its JSONL and exit.
			for i := 0; i < 20; i++ {
				if !claude.IsAlive(pid) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			// Fall back to SIGTERM if claude is still alive — JSONL is
			// append-only so the transcript stays readable either way.
			if claude.IsAlive(pid) {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Signal(syscall.SIGTERM)
				}
				for i := 0; i < 10; i++ {
					if !claude.IsAlive(pid) {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
		// Kill the window explicitly so the sidebar + any terminal-drawer
		// panes are torn down alongside the claude pane. The pane-exited
		// hook would usually handle this when claude exits, but we don't
		// want to race the hook before creating the replacement window.
		_ = m.tmux.KillWindow(m.tmux.SessionName + ":" + primaryWindowName)
		return m.launchClaudeInWindow(primaryWindowName, cwd, "claude")
	}
}

// launchSiblingSession always creates a new concurrent sibling window for
// the current target, even if a primary window already exists. The sibling
// is named "project [N]" (or "project@branch [N]") where N is the lowest
// unused ordinal. Used today via the temporary shift-N binding; step 6
// folds this into the `n`-key menu.
func (m Model) launchSiblingSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		project, branch, cwd, ok := m.detailLaunchTarget()
		if !ok {
			return statusMsgEvent("No project selected")
		}
		windows, err := m.tmux.ListWindows()
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to list windows: %v", err))
		}
		names := make([]string, 0, len(windows))
		for _, w := range windows {
			names = append(names, w.Name)
		}
		ordinal := ttmux.NextAvailableOrdinal(names, project, branch)
		windowName := ttmux.ComposeWindowName(project, branch, ordinal)
		return m.launchClaudeInWindow(windowName, cwd, "claude")
	}
}

// launchClaudeInWindow creates a new tmux window for the given name + cwd, sends
// the given shell command (typically "claude" or "claude --resume <id>"),
// attaches the sidebar pane, and switches focus. Returns a statusMsgEvent.
func (m Model) launchClaudeInWindow(windowName, cwd, shellCmd string) tea.Msg {
	target, err := m.tmux.CreateWindow(windowName, cwd)
	if err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
	}
	// Use "exec" so the shell is replaced — when Claude exits, the pane
	// closes immediately instead of leaving a lingering shell prompt.
	if err := m.tmux.SendKeys(target, "exec "+shellCmd); err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to launch claude: %v", err))
	}
	// Auto-close the whole window (including sidebar) when any pane exits.
	m.tmux.SetWindowHook(target, "pane-exited", "kill-window")
	m.addSidebarPane(target, cwd)
	if err := m.tmux.SwitchToWindow(target); err != nil {
		return statusMsgEvent(fmt.Sprintf("Launched but failed to switch: %v", err))
	}
	return statusMsgEvent("Launched Claude in " + windowName)
}

// launchWorktreeSession opens a Claude session in the given worktree's directory.
// Uses `<project>@<branch>` as the tmux window name. If the window already
// exists, switches to it rather than launching a duplicate.
func (m Model) launchWorktreeSession(wt project.Worktree) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		branch := wt.Branch
		if branch == "" {
			if len(wt.HEAD) >= 8 {
				branch = wt.HEAD[:8]
			} else {
				branch = "detached"
			}
		}
		windowName := p.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchClaudeInWindow(windowName, wt.Path, "claude")
	}
}

// pullRemoteSessionAndLaunch handles a "br-remote" row: create the worktree
// for the branch (if needed), decrypt the synced JSONL into that worktree's
// Claude projects dir, and launch a resumed Claude session there.
func (m Model) pullRemoteSessionAndLaunch(branch string, meta moSync.SessionMeta) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		wtPath, err := project.CreateWorktree(p.Path, branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		syncDir := moSync.DefaultSyncDir()
		if _, err := moSync.Pull(meta.ProjectName, wtPath, syncDir); err != nil {
			return statusMsgEvent(fmt.Sprintf("Pull failed: %v", err))
		}
		windowName := p.Name + "@" + branch
		var status string
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Pulled but failed to switch: %v", err)
			} else {
				status = "Resumed " + windowName
			}
		} else {
			launch := m.launchResumeInWindow(windowName, wtPath, meta.SessionID)
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
	}
}

// createWorktreeAndLaunch creates a new git worktree for the branch (creating
// the branch if needed) and launches a Claude session in it. Emits a
// worktreeCreatedMsg so Update can refresh detailWorktrees.
func (m Model) createWorktreeAndLaunch(branch string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		wtPath, err := project.CreateWorktree(p.Path, branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		windowName := p.Name + "@" + branch
		var status string
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Worktree created but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchClaudeInWindow(windowName, wtPath, "claude")
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
	}
}

func (m Model) resumeSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		windowName, cwd, ok := m.detailContext()
		if !ok {
			return statusMsgEvent("No project selected")
		}

		session := claude.SessionForPath(cwd)
		if session == nil {
			return statusMsgEvent("No session to resume for " + windowName)
		}

		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchClaudeInWindow(windowName, cwd, fmt.Sprintf("claude --resume %s", session.SessionID))
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
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchResumeInWindow(windowName, cwd, sessionID)
	}
}

// sessionImportedMsg signals that an external claude was successfully pulled
// into mo's tmux. Update handles it by reporting the status *and* kicking
// off an immediate sessionRefresh so the main TUI + state file (and the
// sidebar that polls it) pick the new window up without waiting for the
// next 5s tick.
type sessionImportedMsg struct{ status string }

// importExternalSession terminates an orphan claude running outside mo's tmux
// session (e.g. started from a VS Code terminal) and resumes its conversation
// in a fresh tmux window. The SIGTERM gives claude a moment to flush its
// JSONL before the new process attaches to the same sessionID.
func (m Model) importExternalSession(pid int, sessionID, cwd, windowName string) tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return sessionImportedMsg{status: "tmux not available"}
		}
		if pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
			// Wait up to ~2s for the orphan to exit cleanly; fall through regardless.
			for i := 0; i < 20; i++ {
				if !claude.IsAlive(pid) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		// Give the newly-spawned claude a moment to write its PID file so
		// the refresh that follows actually sees it.
		msg := m.launchResumeInWindow(windowName, cwd, sessionID)
		for i := 0; i < 30; i++ {
			if claude.SessionForPath(cwd) != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		status := ""
		if se, ok := msg.(statusMsgEvent); ok {
			status = string(se)
		}
		return sessionImportedMsg{status: status}
	}
}

// disconnectAndResumeInDir kills the existing tmux window and starts the
// selected session fresh in the given directory.
func (m Model) disconnectAndResumeInDir(sessionID, cwd, windowName string) tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		_ = m.tmux.KillWindow(m.tmux.SessionName + ":" + windowName)
		return m.launchResumeInWindow(windowName, cwd, sessionID)
	}
}

// launchResumeInWindow creates a fresh tmux window for the project, runs
// `claude --resume <id>` in it, attaches a sidebar pane, and switches focus.
// Returns a statusMsgEvent describing the outcome.
func (m Model) launchResumeInWindow(windowName, projectPath, sessionID string) tea.Msg {
	target, err := m.tmux.CreateWindow(windowName, projectPath)
	if err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
	}

	cmd := fmt.Sprintf("claude --resume %s", sessionID)
	if err := m.tmux.SendKeys(target, cmd); err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to resume: %v", err))
	}

	m.addSidebarPane(target, projectPath)

	if err := m.tmux.SwitchToWindow(target); err != nil {
		return statusMsgEvent(fmt.Sprintf("Resumed but failed to switch: %v", err))
	}

	return statusMsgEvent("Resumed session in " + windowName)
}

// detailContext resolves the tmux window name and cwd for the row the detail
// cursor is currently on. When the cursor is on a branch row whose checkout
// is a worktree, returns that worktree's window (<project>@<branch>) and
// path; otherwise returns the main project's. Returns (nil) if no project is
// in context.
func (m Model) detailContext() (windowName, cwd string, ok bool) {
	project, branch, cwd, ok := m.detailLaunchTarget()
	if !ok {
		return "", "", false
	}
	return ttmux.ComposeWindowName(project, branch, ""), cwd, true
}

// detailLaunchTarget returns the (project, branch, cwd) tuple for the row
// the detail cursor is currently on. Like detailContext but without
// pre-composing the window name, so callers that need to build sibling
// window names (e.g. "project [2]") can call ComposeWindowName themselves.
// branch is "" when the target is the main checkout.
func (m Model) detailLaunchTarget() (project, branch, cwd string, ok bool) {
	p := m.currentProject()
	if p == nil {
		return "", "", "", false
	}
	if m.screen == ScreenProject && m.detailCursor >= 0 && m.detailCursor < len(m.detailRows) {
		row := m.detailRows[m.detailCursor]
		if row.branch != nil && !row.branch.IsMain && row.branch.WorktreePath != "" {
			return p.Name, row.branch.Name, row.branch.WorktreePath, true
		}
	}
	return p.Name, "", p.Path, true
}

// currentBranchRow returns the Branch for the row under the detail cursor,
// or nil if the cursor isn't on a branch-scoped row.
func (m Model) currentBranchRow() *project.Branch {
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return nil
	}
	return m.detailRows[m.detailCursor].branch
}

// resumeBranchSmart picks the most natural action for the given branch:
// switch to the worktree window if one exists, launch in main if the branch
// is the main checkout, otherwise create a worktree and launch.
func (m Model) resumeBranchSmart(b project.Branch) tea.Cmd {
	p := m.detailProject
	if p == nil {
		return func() tea.Msg { return statusMsgEvent("No project selected") }
	}
	switch {
	case b.WorktreePath != "":
		wt := project.Worktree{Path: b.WorktreePath, Branch: b.Name}
		return m.launchWorktreeSession(wt)
	case b.IsMain:
		return func() tea.Msg { return m.launchClaudeInWindow(p.Name, p.Path, "claude") }
	default:
		return m.createWorktreeAndLaunch(b.Name)
	}
}

// openBranchInMain checks out the given branch in the main project repo and
// launches a Claude session there. Refuses when the main path has a live
// Claude session or when the working tree is dirty (unless force, which
// stashes first).
func (m Model) openBranchInMain(branch string, force bool) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		if claude.SessionForPath(p.Path) != nil {
			return statusMsgEvent("Main has an active Claude session; close it first")
		}
		dirty, err := project.IsDirty(p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("git status failed: %v", err))
		}
		if dirty && !force {
			return statusMsgEvent("Main checkout is dirty; press M to stash and switch, or commit/stash manually")
		}
		if dirty && force {
			if err := project.StashMain(p.Path, branch); err != nil {
				return statusMsgEvent(fmt.Sprintf("stash failed: %v", err))
			}
		}
		if err := project.CheckoutInMain(p.Path, branch); err != nil {
			return statusMsgEvent(fmt.Sprintf("checkout failed: %v", err))
		}
		status := "Switched main to " + branch
		if existed, err := m.focusIfExists(p.Name); existed {
			if err != nil {
				status = fmt.Sprintf("checked out %s but failed to switch window: %v", branch, err)
			}
		} else {
			if se, ok := m.launchClaudeInWindow(p.Name, p.Path, "claude").(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return branchesChangedMsg{status: status}
	}
}

func (m Model) attachSession() tea.Cmd {
	return func() tea.Msg {
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		windowName, _, ok := m.detailContext()
		if !ok {
			return statusMsgEvent("No project selected")
		}
		existed, err := m.focusIfExists(windowName)
		if !existed {
			return statusMsgEvent("No session for " + windowName)
		}
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to attach: %v", err))
		}
		return statusMsgEvent("Attached to " + windowName)
	}
}

func Run(projects []project.Project, tmuxSession, socketPath, stateFilePath string) error {
	var tc *ttmux.Client
	if ttmux.IsInsideTmux() {
		// Use the actual current session — the user may be in a session
		// with a different name than the configured one.
		session := ttmux.CurrentSessionName()
		if session == "" {
			session = tmuxSession
		}
		tc = ttmux.NewClient(session)
		// Ensure mouse support is enabled on this session every launch.
		// Covers fresh Linux installs where no global `set -g mouse on` exists.
		tc.EnableMouse()
	}

	// Start notification server
	ns := notify.NewServer(socketPath)
	if err := ns.Start(); err != nil {
		// Non-fatal: continue without notifications
		ns = nil
	}
	if ns != nil {
		defer ns.Stop()
	}

	m := NewModel(projects, tc, ns, stateFilePath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()

	// Clean up state file on exit
	state.Remove(stateFilePath)

	return err
}
