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
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/status"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
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
	ScreenTicket
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

// ticketsTickMsg fires on the user-configurable refresh cadence (default 5m).
type ticketsTickMsg time.Time

func ticketsTick(period time.Duration) tea.Cmd {
	return tea.Tick(period, func(t time.Time) tea.Msg {
		return ticketsTickMsg(t)
	})
}

// ticketsRefreshMsg carries the outcome of a tickets fetch across all
// providers. Errors are kept per-provider so one unhealthy provider doesn't
// hide a healthy one.
type ticketsRefreshMsg struct {
	groups  []tickets.BucketGroup
	results []tickets.FetchResult
}

func (m Model) fetchTickets() tea.Cmd {
	providers := m.ticketsProviders
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		all, results := tickets.FetchAll(ctx, providers)
		return ticketsRefreshMsg{
			groups:  tickets.Group(all),
			results: results,
		}
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

// projectsRefreshMsg carries a freshly-scanned project list. Handled in
// Update to merge new projects into the dashboard without losing status dots.
type projectsRefreshMsg struct {
	projects []project.Project
}

func (m Model) refreshProjects() tea.Cmd {
	dirs := m.workspaceDirs
	manual := m.manualProjects
	if len(dirs) == 0 {
		return nil
	}
	return func() tea.Msg {
		discovered, _ := project.ScanWorkspace(dirs)
		merged := project.MergeWithManual(discovered, manual)
		return projectsRefreshMsg{projects: merged}
	}
}

// notificationMsg wraps a notification received from the Unix socket.
type notificationMsg notify.Notification

// statusChangeMsg wraps a status change from the status manager.
type statusChangeMsg status.StatusChange

// suspendCompleteMsg signals that SuspendAll finished.
type suspendCompleteMsg struct {
	err error
}

// Model is the root Bubbletea model.
type Model struct {
	screen         Screen
	list           list.Model
	projects       []project.Project
	tmux           ops.TmuxClient
	claude         ops.ClaudeReader
	ops            *ops.Context
	agents         []config.AgentConfig
	agentChoices   map[string]string // project:branch → agent key (persisted preferences)
	notifServer    *notify.Server
	statusMgr      *status.Manager  // central source of truth for session statuses
	statusWatcher  *status.Watcher  // fsnotify watcher for JSONL reconciliation
	statusSub      <-chan status.StatusChange // subscription to status changes
	statusMsg      string
	activeSessions     int
	attentionCount     int
	gitStatuses    map[string]project.GitStatus // project path → git status
	// Dashboard active sessions panel (right side)
	dashFocusLeft      bool // true = project list, false = sessions/tickets panel
	dashSessionItems   []dashSessionItem
	dashSessionCursor  int
	// Dashboard right-panel focus: sessions (top) vs tickets (bottom). Only
	// relevant when dashFocusLeft is false.
	dashRightFocus   dashRightSection
	// Tickets state (populated by background ticketsFetch).
	ticketsDisabled  bool             // explicit opt-out via [tickets] enabled = false
	ticketsProviders []tickets.Provider
	// Ticket detail screen state (ScreenTicket).
	detailTicket        *tickets.TicketDetail
	detailTicketLoading bool
	detailTicketErr     string
	// detailTicketList holds the Ticket struct the user selected on the
	// dashboard — used for render (bucket, priority, sprint) while the
	// detail fetch is in flight.
	detailTicketList *tickets.Ticket
	// Project picker overlay (sub-state of ScreenTicket). Drives both the
	// first-time "which project does OP map to?" flow and the override
	// flow. When active, key events route to the picker list.
	pickerActive         bool
	pickerList           list.Model
	pickerForProvider    string // "jira"
	pickerForJiraKey     string // e.g. "OP"
	pickerRememberActive bool   // true while we're asking r/n to persist the pick
	pickerPendingMo      string // mo project name user just selected
	// Project map companion-file overlay (loaded once, refreshed after save).
	ticketProjectMap map[string]map[string]string
	ticketsInstances []jira.Instance  // retained for NeedsConfig re-check on token env changes
	ticketsGroups    []tickets.BucketGroup
	ticketsErrors    []tickets.FetchResult
	ticketsLoaded    bool             // false until first fetch completes
	ticketsLoading   bool             // true while a fetch is in flight
	ticketsLastFetch time.Time
	ticketsCursor    int              // index into the flat visible list below
	ticketsPerBucket int              // config-driven overflow cap
	ticketsRefreshPeriod time.Duration
	// Buckets the user has expanded via enter on the overflow row. Expansion
	// is session-scoped (not persisted) — the default per-bucket cap applies
	// again on next launch, which is the behavior the user most often wants.
	ticketsExpanded  map[tickets.Bucket]bool
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
	// Import-external-session prompt: non-empty means we're asking the user
	// whether to take over a claude running outside mo (kill it + resume here).
	pendingImportSessionID string
	pendingImportPath      string
	pendingImportWindow    string
	pendingImportProject   string // project display name, for the prompt text
	pendingImportPID       int
	// New-session menu: active when the user pressed `n` on a target that
	// already has a live session, or `enter` on a historical session row
	// whose primary window is occupied by a different session. Presents
	// s/p/c/esc options (switch / park+new / concurrent / cancel). Captured
	// per-field so the menu survives cursor movement without re-querying.
	pendingNewMenuActive     bool
	pendingNewProject        string
	pendingNewBranch         string // "" for main checkout
	pendingNewCwd            string
	pendingNewPrimaryWin     string // composed primary window name (no suffix)
	pendingNewLivePID        int    // claude PID of the session to park on `p`
	pendingNewLiveID         string // claude session ID of the current primary
	pendingNewResumeSession  string // non-empty ⇒ `p`/`c` launch `claude --resume <id>` instead of fresh
	// Agent picker menu: shown when shift+enter is pressed on a branch row.
	pendingAgentPickerActive bool
	pendingAgentPickerBranch *project.Branch
	// Cleanup menu: active when the user pressed `x` on a branch row.
	// Two stages: "kill" (one or more sessions live in the target; user
	// must confirm SIGINT) then "action" (choose [w] worktree only / [b]
	// worktree + branch / [esc]). Entry skips "kill" when no sessions.
	pendingCleanupActive       bool
	pendingCleanupStage        string // "kill" | "action"
	pendingCleanupProjectPath  string
	pendingCleanupProjectName  string
	pendingCleanupBranch       string
	pendingCleanupWorktreePath string            // "" for plain-branch rows
	pendingCleanupSessions     []claude.Session  // live sessions to kill, captured at entry
	// Worktree-exists prompt: shown when creating a worktree for a branch
	// that already has one. Options: focus existing, remove + recreate, cancel.
	pendingWTExistsActive      bool
	pendingWTExistsBranch      string
	pendingWTExistsProjectPath string
	pendingWTExistsProjectName string
	pendingWTExistsWTPath      string
	pendingWTExistsPRBranch    bool   // true when triggered from a PR (needs git fetch + reset on recreate)
	// Suspend-and-quit prompt: active when q pressed with active sessions.
	pendingSuspendQuitActive bool
	// externalPIDs / externalSessions cache the orphan PID + sessionID for each
	// CWD currently in StatusExternal, populated by refreshSessions.
	externalPIDs     map[string]int
	externalSessions map[string]string
	// sessionViews is the source of truth for dashboard rows + state file
	// entries: one entry per live Claude session. Populated by
	// updateProjectStatuses from the poll result (status manager is source of truth).
	sessionViews []sessionView
	// worktreeInput is non-nil when the user is entering a branch name for a
	// new worktree. While set, key events route to the text input.
	worktreeInput *textinput.Model
	// Lift-session flow: `w` on a br-session row opens a text input for the
	// new branch name, then (if the source is dirty) a multi-option menu to
	// decide whether to carry the dirty state into the new worktree. The
	// source session info is captured at prompt entry so cursor movement
	// can't invalidate the target.
	liftSessionInput        *textinput.Model
	liftSessionSessionID    string
	liftSessionSourcePath   string
	liftSessionSourcePID    int    // 0 when the source session is historical
	liftSessionSourceWindow string // tmux window to kill after SIGTERM; "" skips
	pendingLiftDirtyActive  bool
	pendingLiftBranch       string // branch name from the input prompt
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
	// State file for sidebar instances
	stateFilePath string
	// Claude usage snapshot (5h + weekly rate-limit windows)
	usage      usage.Snapshot
	usageReady bool // false until the first fetch completes (success or cache hit)
	usageAuth  bool // true once a 401 is seen — surfaces an auth-expired banner
	// rawTmux is the concrete *ttmux.Client, kept alongside the ops
	// interface so disableHomeRespawn can call methods that aren't on
	// the TmuxClient interface (SetWindowRemainOnExit, UnsetSessionHook).
	rawTmux *ttmux.Client
	// workspaceDirs + manualProjects are retained so ctrl+r can re-scan for
	// new projects without restarting the TUI.
	workspaceDirs  []string
	manualProjects []project.Project
	width          int
	height         int
	ready          bool
}

func NewModel(projects []project.Project, tmuxClient *ttmux.Client, notifServer *notify.Server, stateFilePath string, ticketsCfg config.TicketsConfig, agents []config.AgentConfig, workspaceDirs []string, manualProjects []project.Project) Model {
	opsCtx := ops.NewContext(tmuxClient)
	m := NewModelWithDeps(projects, opsCtx.Tmux, opsCtx.Claude, opsCtx, notifServer, nil, stateFilePath, ticketsCfg, agents, workspaceDirs, manualProjects)
	m.rawTmux = tmuxClient
	return m
}

// NewModelWithDeps is the test-friendly constructor — accepts ops interface
// implementations so tests can inject mocks directly. Production code calls
// NewModel, which wraps the concrete *ttmux.Client and claude package via
// ops.NewContext.
func NewModelWithDeps(projects []project.Project, tmuxClient ops.TmuxClient, claudeReader ops.ClaudeReader, opsCtx *ops.Context, notifServer *notify.Server, statusMgr *status.Manager, stateFilePath string, ticketsCfg config.TicketsConfig, agents []config.AgentConfig, workspaceDirs []string, manualProjects []project.Project) Model {
	if opsCtx == nil {
		opsCtx = &ops.Context{
			Tmux:         tmuxClient,
			Claude:       claudeReader,
			SidebarWidth: 42,
		}
	}
	items := make([]list.Item, len(projects))
	for i, p := range projects {
		items[i] = ProjectItem{project: p, status: StatusNone}
	}

	l := list.New(items, projectDelegate{}, 0, 0)
	l.Title = "Unky Mo"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)        // We use our own footer
	l.DisableQuitKeybindings()  // We handle q/esc ourselves
	l.InfiniteScrolling = true  // Circular navigation
	l.Styles.Title = titleStyle

	instances := ticketsInstancesFromConfig(ticketsCfg)
	providers := jira.BuildProviders(instances)
	perBucket := ticketsCfg.PerBucketLimit
	if perBucket <= 0 {
		perBucket = 5
	}
	refresh := time.Duration(ticketsCfg.RefreshSeconds) * time.Second
	if refresh <= 0 {
		refresh = 5 * time.Minute
	}

	projectMap, _ := tickets.LoadCompanionProjectMap()
	if projectMap == nil {
		projectMap = map[string]map[string]string{}
	}

	agentChoices, _ := config.LoadAgentChoices()

	if statusMgr == nil {
		statusMgr = status.NewManager()
	}
	statusSub := statusMgr.Subscribe()

	// Create the fsnotify JSONL watcher for reconciliation.
	var statusWatcher *status.Watcher
	statusWatcher, _ = status.NewWatcher(func(sessionID, path string) {
		statusMgr.ProcessJSONLChange(sessionID, path)
	})

	return Model{
		screen:             ScreenDashboard,
		list:               l,
		projects:           projects,
		tmux:               tmuxClient,
		claude:             claudeReader,
		ops:                opsCtx,
		agents:             agents,
		agentChoices:       agentChoices,
		notifServer:        notifServer,
		statusMgr:          statusMgr,
		statusWatcher:      statusWatcher,
		statusSub:          statusSub,
		dashFocusLeft:      true,
		stateFilePath:      stateFilePath,
		workspaceDirs:      workspaceDirs,
		manualProjects:     manualProjects,
		ticketsDisabled:    ticketsCfg.Disabled,
		ticketsProviders:   providers,
		ticketProjectMap:   projectMap,
		ticketsExpanded:    map[tickets.Bucket]bool{},
		ticketsInstances:   instances,
		ticketsPerBucket:   perBucket,
		ticketsRefreshPeriod: refresh,
	}
}

// ticketsInstancesFromConfig bridges the config shape to the jira package's
// flat Instance type, converting the TOML status map to tickets.StatusMap
// and carrying over the hand-authored project_map.
func ticketsInstancesFromConfig(cfg config.TicketsConfig) []jira.Instance {
	out := make([]jira.Instance, 0, len(cfg.Jira))
	for _, j := range cfg.Jira {
		out = append(out, jira.Instance{
			Name:          j.Name,
			BaseURL:       j.BaseURL,
			Email:         j.Email,
			SprintFieldID: j.SprintFieldID,
			StatusMap: tickets.StatusMap{
				InProgress: j.StatusMap.InProgress,
				Blocked:    j.StatusMap.Blocked,
				Review:     j.StatusMap.Review,
				Todo:       j.StatusMap.Todo,
			},
			ProjectMap: j.ProjectMap,
		})
	}
	return out
}

// dashRightSection identifies the focused section in the dashboard right
// panel when the right panel itself is focused.
type dashRightSection int

const (
	dashRightSessions dashRightSection = iota
	dashRightTickets
)

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		sessionTick(), usageTick(),
		m.refreshSessions(), m.refreshGitStatuses(),
		m.fetchUsage(),
	}
	if m.notifServer != nil {
		cmds = append(cmds, m.waitForNotification())
	}
	if m.statusSub != nil {
		cmds = append(cmds, m.waitForStatusChange())
	}
	// Kick off the first tickets fetch immediately so the panel populates on
	// first paint. Subsequent fetches come from ticketsTick.
	if len(m.ticketsProviders) > 0 {
		cmds = append(cmds, m.fetchTickets(), ticketsTick(m.ticketsRefreshPeriod))
	}
	return tea.Batch(cmds...)
}

func (m Model) waitForNotification() tea.Cmd {
	return func() tea.Msg {
		n := <-m.notifServer.Messages()
		return notificationMsg(n)
	}
}

func (m Model) waitForStatusChange() tea.Cmd {
	return func() tea.Msg {
		change, ok := <-m.statusSub
		if !ok {
			return nil
		}
		return statusChangeMsg(change)
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

	case mouseEnterMsg:
		// Sent by mouse click handlers after selecting an item. Dispatches the
		// same enter-key logic without duplicating it.
		return m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m.handleMouseClick(msg.X, msg.Y)
		}
		return m, nil

	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)

	case tea.KeyPressMsg:
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

		// Ticket-screen picker + remember-prompt capture all input while active
		// so normal key bindings don't fire.
		if m.screen == ScreenTicket && m.pickerRememberActive {
			newModel, cmd := m.handleTicketPickerRemember(msg)
			return newModel, cmd
		}
		if m.screen == ScreenTicket && m.pickerActive {
			newModel, cmd := m.handleTicketPickerActive(msg)
			return newModel, cmd
		}
		// `y` on ticket detail yanks the derived branch name.
		if m.screen == ScreenTicket && msg.String() == "y" {
			return m.handleTicketYank()
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

		// Lift-session branch-name input captures all input while active.
		if m.liftSessionInput != nil && m.screen == ScreenProject {
			switch msg.String() {
			case "enter":
				branch := strings.TrimSpace(m.liftSessionInput.Value())
				m.liftSessionInput = nil
				if branch == "" {
					m.clearLiftSessionState()
					return m, nil
				}
				return m.decideLiftDirty(branch)
			case "esc", "escape":
				m.liftSessionInput = nil
				m.clearLiftSessionState()
				return m, nil
			}
			updated, cmd := m.liftSessionInput.Update(msg)
			m.liftSessionInput = &updated
			return m, cmd
		}

		// Lift-session dirty-state menu: [s]tash+pop / [l]eave / [n]cancel.
		// Non-destructive (creating a new worktree doesn't delete anything), so
		// enter binds to the recommended primary `s` (carry the changes).
		if m.pendingLiftDirtyActive {
			switch msg.String() {
			case "s", "S", "enter":
				cmd := m.runLiftSession(true)
				m.clearLiftSessionState()
				return m, cmd
			case "l", "L":
				cmd := m.runLiftSession(false)
				m.clearLiftSessionState()
				return m, cmd
			case "n", "N", "esc", "escape":
				m.clearLiftSessionState()
				return m, nil
			}
			return m, nil
		}

		// New-session menu captures all input while active.
		if m.pendingNewMenuActive {
			// Multi-option menu. Primary = switch (least destructive: no kill,
			// no spawn). enter fires the primary; n/N/esc cancel.
			switch msg.String() {
			case "s", "S", "enter":
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
				resumeID := m.pendingNewResumeSession
				m.clearPendingNewMenu()
				return m, m.parkAndLaunchPrimary(pid, primary, cwd, resumeID)
			case "c", "C":
				resumeID := m.pendingNewResumeSession
				project := m.pendingNewProject
				branch := m.pendingNewBranch
				cwd := m.pendingNewCwd
				m.clearPendingNewMenu()
				return m, m.launchSiblingSession(project, branch, cwd, resumeID)
			case "n", "N", "esc", "escape":
				m.clearPendingNewMenu()
				return m, nil
			}
			return m, nil
		}

		// Cleanup menu captures all input while active.
		if m.pendingCleanupActive {
			switch m.pendingCleanupStage {
			case "kill":
				// Destructive [y/N] confirm: only y/Y confirms. Enter matches
				// the default (N) and cancels, same as n/esc.
				switch msg.String() {
				case "y", "Y":
					sessions := m.pendingCleanupSessions
					m.pendingCleanupStage = "action"
					return m, m.killCleanupSessions(sessions)
				case "n", "N", "enter", "esc", "escape":
					m.clearPendingCleanupMenu()
					return m, nil
				}
				return m, nil
			case "action":
				if m.pendingCleanupWorktreePath != "" {
					// Destructive multi-option: no enter-as-primary. Every
					// action requires an explicit letter; enter cancels.
					switch msg.String() {
					case "w", "W":
						projectPath := m.pendingCleanupProjectPath
						branch := m.pendingCleanupBranch
						m.clearPendingCleanupMenu()
						return m, m.runCleanup(projectPath, branch, false)
					case "b", "B":
						projectPath := m.pendingCleanupProjectPath
						branch := m.pendingCleanupBranch
						m.clearPendingCleanupMenu()
						return m, m.runCleanup(projectPath, branch, true)
					case "n", "N", "enter", "esc", "escape":
						m.clearPendingCleanupMenu()
						return m, nil
					}
					return m, nil
				}
				// Plain-branch row: destructive [y/N] confirm.
				switch msg.String() {
				case "y", "Y":
					projectPath := m.pendingCleanupProjectPath
					branch := m.pendingCleanupBranch
					m.clearPendingCleanupMenu()
					return m, m.runCleanup(projectPath, branch, true)
				case "n", "N", "enter", "esc", "escape":
					m.clearPendingCleanupMenu()
					return m, nil
				}
				return m, nil
			}
			return m, nil
		}

		// Worktree-exists prompt: non-destructive multi-option menu.
		if m.pendingWTExistsActive {
			switch msg.String() {
			case "f", "F":
				projectName := m.pendingWTExistsProjectName
				branch := m.pendingWTExistsBranch
				wtPath := m.pendingWTExistsWTPath
				m.clearPendingWTExists()
				return m, m.focusExistingWorktree(projectName, branch, wtPath)
			case "r", "R":
				projectPath := m.pendingWTExistsProjectPath
				projectName := m.pendingWTExistsProjectName
				branch := m.pendingWTExistsBranch
				prBranch := m.pendingWTExistsPRBranch
				m.clearPendingWTExists()
				return m, m.removeAndRecreateWorktree(projectPath, projectName, branch, prBranch)
			case "n", "N", "enter", "esc", "escape":
				m.clearPendingWTExists()
				return m, nil
			}
			return m, nil
		}

		// Agent picker menu: non-destructive multi-option.
		if m.pendingAgentPickerActive {
			k := msg.String()
			// Check each configured agent mnemonic.
			for _, a := range m.agents {
				if k == a.Key || k == strings.ToUpper(a.Key) {
					branch := m.pendingAgentPickerBranch
					agent := a
					m.clearAgentPicker()
					if branch == nil {
						return m, nil
					}
					return m, m.launchBranchWithAgent(*branch, agent)
				}
			}
			// Cancel keys.
			switch k {
			case "n", "N", "esc", "escape", "enter":
				m.clearAgentPicker()
				return m, nil
			}
			return m, nil
		}

		// Suspend-and-quit prompt: destructive, enter cancels.
		if m.pendingSuspendQuitActive {
			switch msg.String() {
			case "s", "S":
				m.pendingSuspendQuitActive = false
				return m, m.executeSuspendAll()
			default:
				m.pendingSuspendQuitActive = false
				return m, nil
			}
		}

		// Import-external-session prompt captures all input while active.
		// Destructive [y/N] (kills the external claude): only y/Y confirms.
		if m.pendingImportSessionID != "" {
			switch msg.String() {
			case "y", "Y":
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
			case "n", "N", "enter", "esc", "escape":
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
			if m.screen == ScreenDashboard && !m.dashFocusLeft && m.dashRightFocus == dashRightTickets {
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
					return m, nil
				}
				// Ticket row: open the detail screen.
				if t := m.ticketAtCursor(); t != nil {
					selected := *t // copy so the list can safely change
					m.screen = ScreenTicket
					m.detailTicket = nil
					m.detailTicketErr = ""
					m.detailTicketLoading = true
					m.detailTicketList = &selected
					return m, m.fetchTicketDetail(selected.ID, selected.Provider)
				}
				return m, nil
			}
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
						target := m.safeWindowTarget(item.WindowName, item.WindowID)
						m.tmux.SwitchToWindow(target)
					}
				}
				return m, nil
			}
			if m.screen == ScreenDashboard {
				p := m.currentProject()
				if p != nil {
					m.detailProject = p
					m.detailSession = m.claude.SessionForPath(p.Path)
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
						return m, m.resumeInDir(selectedID, row.path, windowName)
					}
					existing := m.claude.SessionForPath(row.path)
					if existing == nil || existing.SessionID == selectedID {
						return m, m.resumeInDir(selectedID, row.path, windowName)
					}
					m.pendingNewMenuActive = true
					m.pendingNewProject = m.detailProject.Name
					m.pendingNewBranch = branchName
					m.pendingNewCwd = row.path
					m.pendingNewPrimaryWin = windowName
					m.pendingNewLivePID = existing.PID
					m.pendingNewLiveID = existing.SessionID
					m.pendingNewResumeSession = selectedID
					return m, nil
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

		case key.Matches(msg, keys.AgentLaunch):
			// Shift+enter opens the agent picker menu.
			if m.screen == ScreenDashboard && m.dashFocusLeft {
				// Dashboard project list: pick agent, then launch on main branch.
				if sel, ok := m.list.SelectedItem().(ProjectItem); ok {
					m.pendingAgentPickerActive = true
					main := project.Branch{Name: "main", IsMain: true}
					m.pendingAgentPickerBranch = &main
					m.detailProject = &sel.project
					return m, nil
				}
			}
			if m.screen == ScreenProject && m.detailFocusLeft {
				if m.detailCursor >= 0 && m.detailCursor < len(m.detailRows) {
					row := m.detailRows[m.detailCursor]
					if (row.kind == "branch" || row.kind == "br-empty") && row.branch != nil {
						m.pendingAgentPickerActive = true
						b := *row.branch
						m.pendingAgentPickerBranch = &b
						return m, nil
					}
				}
			}

		case key.Matches(msg, keys.Back):
			// On ScreenTicket, esc unwinds one layer at a time: close the
			// remember-prompt first, then the picker, then the screen itself.
			if m.screen == ScreenTicket && m.pickerRememberActive {
				m.pickerRememberActive = false
				m.pickerPendingMo = ""
				return m, nil
			}
			if m.screen == ScreenTicket && m.pickerActive {
				m.pickerActive = false
				return m, nil
			}
			if m.screen != ScreenDashboard {
				m.screen = ScreenDashboard
				// Reset ticket detail state so a stale ticket doesn't flash
				// next time the screen opens.
				m.detailTicket = nil
				m.detailTicketList = nil
				m.detailTicketErr = ""
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
				primaryWin, existing := m.primaryWindowForTarget(project, branch, cwd)
				if existing == nil {
					return m, m.launchSession()
				}
				m.pendingNewMenuActive = true
				m.pendingNewProject = project
				m.pendingNewBranch = branch
				m.pendingNewCwd = cwd
				m.pendingNewPrimaryWin = primaryWin
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
				// Default to sessions; fall through to tickets when no sessions exist.
				if len(m.dashSessionItems) == 0 && m.ticketsVisibleLen() > 0 {
					m.dashRightFocus = dashRightTickets
				} else {
					m.dashRightFocus = dashRightSessions
				}
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
			if m.screen == ScreenDashboard && !m.dashFocusLeft && m.dashRightFocus == dashRightTickets {
				if t := m.ticketAtCursor(); t != nil && t.URL != "" {
					url := t.URL
					id := t.ID
					return m, func() tea.Msg {
						if err := openInBrowser(url); err != nil {
							return statusMsgEvent(fmt.Sprintf("Open failed: %v", err))
						}
						return statusMsgEvent(fmt.Sprintf("Opened %s in browser", id))
					}
				}
			}
			if m.screen == ScreenTicket {
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
					}
				}
			}

		case key.Matches(msg, keys.Checkout):
			if m.screen == ScreenProject && !m.detailFocusLeft && len(m.detailPRs) > 0 && m.detailPRCursor >= 0 && m.detailPRCursor < len(m.detailPRs) {
				pr := m.detailPRs[m.detailPRCursor]
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
				// If focus is on the right panel, create worktree from the highlighted PR.
				if !m.detailFocusLeft && len(m.detailPRs) > 0 && m.detailPRCursor >= 0 && m.detailPRCursor < len(m.detailPRs) {
					pr := m.detailPRs[m.detailPRCursor]
					return m, m.createWorktreeFromPR(pr)
				}
				// Session row: open the lift-to-new-worktree flow. Captures the
				// source session info up front so later cursor moves can't
				// change the target mid-flow.
				if row := m.currentDetailRow(); row != nil && row.kind == "br-session" && row.session != nil {
					return m.startLiftSessionPrompt(row)
				}
				// Left panel, plain branch row: today's behavior — worktree for that branch.
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
				ti.SetWidth(40)
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

		case key.Matches(msg, keys.Cleanup):
			if m.screen == ScreenProject && m.detailFocusLeft && m.detailProject != nil {
				b := m.currentBranchRow()
				if b == nil {
					return m, nil
				}
				if b.IsMain && b.WorktreePath == "" {
					return m, func() tea.Msg { return statusMsgEvent("Cannot delete the main branch") }
				}
				sessions := m.claude.SessionsForPath(b.WorktreePath)
				m.pendingCleanupActive = true
				m.pendingCleanupProjectPath = m.detailProject.Path
				m.pendingCleanupProjectName = m.detailProject.Name
				m.pendingCleanupBranch = b.Name
				m.pendingCleanupWorktreePath = b.WorktreePath
				m.pendingCleanupSessions = sessions
				if len(sessions) > 0 {
					m.pendingCleanupStage = "kill"
				} else {
					m.pendingCleanupStage = "action"
				}
				return m, nil
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
			return m, tea.Batch(cmds...)

		case key.Matches(msg, keys.Restart):
			self, err := os.Executable()
			if err == nil {
				// Restart all sidebar panes first
				m.restartSidebars()
				return m, tea.ExecProcess(exec.Command(self), nil)
			}

		case key.Matches(msg, keys.Suspend):
			// On the ticket detail screen, `s` means "start working" instead
			// of suspend — resolves the project mapping, opens the picker if
			// missing, or kicks off the branch/worktree flow otherwise.
			if m.screen == ScreenTicket && !m.pickerActive && !m.pickerRememberActive {
				return m.handleTicketStartWorking()
			}
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
			if m.activeSessions > 0 {
				m.pendingSuspendQuitActive = true
				return m, nil
			}
			m.disableHomeRespawn()
			return m, tea.Quit
		}

	case sessionTickMsg:
		m.pruneOrphanedTermSessions()
		return m, tea.Batch(sessionTick(), m.refreshSessions(), m.refreshGitStatuses())

	case projectsRefreshMsg:
		items := mergeRefreshedProjects(m.list.Items(), msg.projects)
		m.list.SetItems(items)
		// Update the flat projects slice so refreshGitStatuses and other
		// paths that iterate m.projects see the new set.
		updated := make([]project.Project, len(items))
		for i, item := range items {
			updated[i] = item.(ProjectItem).project
		}
		m.projects = updated
		return m, nil

	case suspendCompleteMsg:
		if msg.err != nil {
			return m, func() tea.Msg {
				return statusMsgEvent(fmt.Sprintf("suspend failed: %v", msg.err))
			}
		}
		m.disableHomeRespawn()
		return m, tea.Quit

	case sessionRefreshMsg:
		m.updateProjectStatuses(msg)
		// Rebuild the detail view so session rows reflect fresh live data
		// (needed e.g. after a lift, where a refresh kicked off by the handler
		// must land without the user leaving + re-entering the screen).
		if m.screen == ScreenProject && m.detailProject != nil {
			m.buildDetailRows()
		}
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

	case ticketsTickMsg:
		if len(m.ticketsProviders) == 0 {
			return m, nil
		}
		m.ticketsLoading = true
		return m, tea.Batch(ticketsTick(m.ticketsRefreshPeriod), m.fetchTickets())

	case ticketsRefreshMsg:
		m.ticketsGroups = msg.groups
		m.ticketsErrors = msg.results
		m.ticketsLoaded = true
		m.ticketsLoading = false
		m.ticketsLastFetch = time.Now()
		if m.ticketsCursor >= m.ticketsVisibleLen() {
			m.ticketsCursor = max(0, m.ticketsVisibleLen()-1)
		}
		return m, nil

	case ticketDetailMsg:
		m.detailTicketLoading = false
		if msg.err != nil {
			m.detailTicketErr = msg.err.Error()
			return m, nil
		}
		m.detailTicket = msg.detail
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
		m.routeNotificationToStatusMgr(notify.Notification(msg))
		cmds := []tea.Cmd{m.refreshSessions()}
		if m.notifServer != nil {
			cmds = append(cmds, m.waitForNotification())
		}
		return m, tea.Batch(cmds...)

	case statusChangeMsg:
		// Status manager detected a change — refresh immediately.
		cmds := []tea.Cmd{m.refreshSessions()}
		if m.statusSub != nil {
			cmds = append(cmds, m.waitForStatusChange())
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

	case worktreeExistsMsg:
		if m.detailProject != nil {
			m.pendingWTExistsActive = true
			m.pendingWTExistsBranch = msg.branch
			m.pendingWTExistsProjectPath = m.detailProject.Path
			m.pendingWTExistsProjectName = m.detailProject.Name
			m.pendingWTExistsWTPath = msg.worktreePath
			m.pendingWTExistsPRBranch = msg.prBranch
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

	case sessionLiftedMsg:
		// Rebuild branches + worktrees + rows so the new session row lands on
		// the new branch, and kick off a session refresh so live-status data
		// (the old PID is now gone) is up to date.
		if m.detailProject != nil {
			m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
			m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
			m.buildDetailRows()
		}
		status := msg.status
		return m, tea.Batch(
			m.refreshSessions(),
			func() tea.Msg { return statusMsgEvent(status) },
		)

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
		// Right panel: sessions (top) + optional tickets (bottom). Up/down
		// moves within the focused section and crosses the boundary at the
		// ends (down past the last session → first ticket; up past the first
		// ticket → last session). Wraps within each section if tickets isn't
		// rendered.
		if !m.dashFocusLeft {
			if msg, ok := msg.(tea.KeyPressMsg); ok {
				sess := len(m.dashSessionItems)
				ticketsLen := 0
				if m.ticketsShouldRender() {
					ticketsLen = m.ticketsVisibleLen()
				}
				switch msg.String() {
				case "up", "k":
					if m.dashRightFocus == dashRightSessions {
						if sess > 0 {
							if m.dashSessionCursor == 0 && ticketsLen > 0 {
								m.dashRightFocus = dashRightTickets
								m.ticketsCursor = ticketsLen - 1
							} else {
								m.dashSessionCursor = (m.dashSessionCursor - 1 + sess) % sess
							}
						}
					} else {
						if ticketsLen > 0 {
							if m.ticketsCursor == 0 && sess > 0 {
								m.dashRightFocus = dashRightSessions
								m.dashSessionCursor = sess - 1
							} else {
								m.ticketsCursor = (m.ticketsCursor - 1 + ticketsLen) % ticketsLen
							}
						}
					}
				case "down", "j":
					if m.dashRightFocus == dashRightSessions {
						if sess > 0 {
							if m.dashSessionCursor == sess-1 && ticketsLen > 0 {
								m.dashRightFocus = dashRightTickets
								m.ticketsCursor = 0
							} else {
								m.dashSessionCursor = (m.dashSessionCursor + 1) % sess
							}
						} else if ticketsLen > 0 {
							m.dashRightFocus = dashRightTickets
							m.ticketsCursor = 0
						}
					} else {
						if ticketsLen > 0 {
							if m.ticketsCursor == ticketsLen-1 && sess > 0 {
								m.dashRightFocus = dashRightSessions
								m.dashSessionCursor = 0
							} else {
								m.ticketsCursor = (m.ticketsCursor + 1) % ticketsLen
							}
						}
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
		if msg, ok := msg.(tea.KeyPressMsg); ok {
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

func (m Model) View() tea.View {
	var content string
	if !m.ready {
		content = "Loading..."
	} else {
		switch m.screen {
		case ScreenProject:
			content = m.projectDetailView()
		case ScreenHelp:
			content = m.helpView()
		case ScreenTicket:
			content = m.renderTicketDetailScreen()
		default:
			content = m.dashboardView()
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// dashSessionItem represents an active session in the dashboard's right panel.
type dashSessionItem struct {
	Name        string
	WindowName  string
	WindowID    string              // stable tmux window ID (@N); used for safe target construction
	AgentKey    string              // coding agent mnemonic; empty = default (Claude)
	Status      SessionStatus
	ProjectPath string              // cwd — used to resolve external PID/sessionID on import
	Section     string              // "projects" (default) or "external"
	Git         *project.GitStatus  // set for git-backed strays; nil otherwise
}

// refreshDashSessions rebuilds the active sessions list for the dashboard
// panel by walking m.sessionViews. Unlike the state file, the dashboard
// does not emit placeholders for projects without live sessions — only
// projects that actually have a session get a row.
//
// Ordering: ProjectItem order drives the top-level grouping. Within each
// project, main-checkout sessions render first (primary then siblings),
// then each worktree's sessions. Strays (git-backed) follow the projects
// section; non-git strays go in the external section.
func (m *Model) refreshDashSessions() {
	mainByProject := map[string][]sessionView{}
	worktreeByKey := map[string][]sessionView{}
	var stray, external []sessionView

	for _, v := range m.sessionViews {
		if v.IsStray {
			if v.Section == "external" {
				external = append(external, v)
			} else {
				stray = append(stray, v)
			}
			continue
		}
		if v.IsWorktree {
			key := v.ProjectPath + "|" + strings.TrimPrefix(v.ProjectName, "@")
			worktreeByKey[key] = append(worktreeByKey[key], v)
			continue
		}
		mainByProject[v.ProjectPath] = append(mainByProject[v.ProjectPath], v)
	}
	for k := range mainByProject {
		sortViewsForDisplay(mainByProject[k])
	}
	for k := range worktreeByKey {
		sortViewsForDisplay(worktreeByKey[k])
	}

	var items []dashSessionItem
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		for _, v := range mainByProject[pi.project.Path] {
			items = append(items, viewToDashItem(v))
		}

		branchesSeen := map[string]bool{}
		var branchOrder []string
		for key := range worktreeByKey {
			if !strings.HasPrefix(key, pi.project.Path+"|") {
				continue
			}
			branch := strings.TrimPrefix(key, pi.project.Path+"|")
			if !branchesSeen[branch] {
				branchesSeen[branch] = true
				branchOrder = append(branchOrder, branch)
			}
		}
		sort.Strings(branchOrder)
		for _, branch := range branchOrder {
			for _, v := range worktreeByKey[pi.project.Path+"|"+branch] {
				items = append(items, viewToDashItem(v))
			}
		}
	}

	// Strays: git-backed go into projects with git info, non-git into
	// external. ProjectPath is always the session's CWD so the import flow
	// can find the right PID/sessionID (maps are CWD-keyed) and launch the
	// new tmux window at exactly where claude was running.
	for _, v := range stray {
		items = append(items, viewToDashItem(v))
	}
	for _, v := range external {
		items = append(items, viewToDashItem(v))
	}

	m.dashSessionItems = items
	if m.dashSessionCursor >= len(items) {
		m.dashSessionCursor = max(0, len(items)-1)
	}
}

// viewToDashItem converts one sessionView into a dashSessionItem. Name
// keeps the real tmux window name so the sidebar + dashboard show the same
// label (including custom-title suffixes).
func viewToDashItem(v sessionView) dashSessionItem {
	item := dashSessionItem{
		Name:        v.WindowName,
		WindowName:  v.WindowName,
		WindowID:    v.WindowID,
		AgentKey:    v.AgentKey,
		Status:      v.Status,
		ProjectPath: v.CWD,
		Section:     v.Section,
	}
	if v.IsStray && v.Branch != "" {
		gs := project.GitStatus{Branch: v.Branch, Dirty: v.Dirty}
		item.Git = &gs
	}
	return item
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

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

	// Split the dashboard 50/50 between the project list and the
	// sessions+tickets panel. On narrow terminals, clamp the left side so
	// the list stays usable; on very wide terminals the even split keeps
	// each half readable without stretching.
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 120
	}
	dividerWidth := 3
	rightWidth := (totalWidth - dividerWidth) / 2
	leftWidth := totalWidth - rightWidth - dividerWidth
	if leftWidth < 40 {
		leftWidth = 40
		rightWidth = totalWidth - leftWidth - dividerWidth
		if rightWidth < 20 {
			rightWidth = 20
		}
	}

	// Resize list to fit the left panel
	footerHeight := 3
	m.list.SetSize(leftWidth, m.height-footerHeight)

	// === Left panel: project list ===
	leftStr := m.list.View()

	// === Right panel: active sessions (top) + tickets (bottom) ===
	var right strings.Builder

	sessionsFocused := !m.dashFocusLeft && m.dashRightFocus == dashRightSessions
	focusIndicator := ""
	if sessionsFocused {
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

			selected := sessionsFocused && m.dashSessionCursor == i
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
			if tag := m.agentTagForKey(item.AgentKey); tag != "" {
				suffix = " " + footerDescStyle.Render(tag)
			}
			if item.Status == StatusIdle {
				suffix += " " + statusIdle.Render("idle")
			} else if item.Status == StatusPermission {
				suffix += " " + statusPermission.Render("perm")
			} else if item.Status == StatusExternal {
				suffix += " " + statusExternal.Render("external")
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

	if panel := m.renderTicketsPanel(rightWidth); panel != "" {
		right.WriteString(panel)
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
	if m.pendingCleanupActive {
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	} else if m.pendingNewMenuActive {
		footer = m.renderPrompt(m.newMenuPromptText(), withCancel([]footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
		}))
	} else if m.pendingSuspendQuitActive {
		question := fmt.Sprintf("%d active session(s) — suspend all and quit?", m.activeSessions)
		footer = m.renderPrompt(question, withCancel([]footerBinding{
			{"S", "suspend + quit"},
		}))
	} else if m.pendingImportSessionID != "" {
		question := fmt.Sprintf("%q is running outside Unky Mo. Import it? (kills the external claude and resumes here) [y/N]", m.pendingImportProject)
		footer = m.renderPrompt(question, yesNoBindings("import"))
	} else if m.pendingAgentPickerActive {
		footer = m.renderPrompt("Launch with:", withCancel(m.agentPickerBindings()))
	} else {
		footer = m.renderFooter([]footerBinding{
			{"↑↓", "navigate"},
			{"←→", "switch panel"},
			{"enter", "open"},
			{"A", "agent"},
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
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	line := strings.Join(parts, "  ")
	return footerStyle.Width(m.width).Render(line)
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

// agentTagForKey returns a short display tag for the given agent key,
// e.g. "(gemini)" for key "g". Returns "" for the default agent (Claude)
// since it's the implied default and doesn't need a tag.
func (m Model) agentTagForKey(key string) string {
	if key == "" {
		return ""
	}
	def := m.defaultAgent()
	if key == def.Key {
		return ""
	}
	for _, a := range m.agents {
		if a.Key == key {
			return "(" + strings.ToLower(a.Name) + ")"
		}
	}
	return "(" + key + ")"
}

// agentResumeCmd builds the shell command for resuming a session with the
// given agent. If the agent has no ResumeCmd, falls back to a fresh launch.
func agentResumeCmd(agent config.AgentConfig, sessionID string) string {
	if agent.ResumeCmd != "" {
		return agent.ResumeCmd + " " + sessionID
	}
	return agent.Cmd
}

// agentForBranch returns the agent to use for a project+branch. Resolution
// order: saved choice > default agent.
func (m Model) agentForBranch(projectName, branch string) config.AgentConfig {
	if key := config.LookupAgentChoice(m.agentChoices, projectName, branch); key != "" {
		if a := m.agentByKey(key); a != nil {
			return *a
		}
	}
	return m.defaultAgent()
}

// agentByKey looks up a configured agent by key, or nil.
func (m Model) agentByKey(key string) *config.AgentConfig {
	for i := range m.agents {
		if m.agents[i].Key == key {
			return &m.agents[i]
		}
	}
	return nil
}

// defaultAgent returns the user's default agent config.
func (m Model) defaultAgent() config.AgentConfig {
	for _, a := range m.agents {
		if a.Default {
			return a
		}
	}
	if len(m.agents) > 0 {
		return m.agents[0]
	}
	return config.DefaultAgents()[0]
}

// agentPickerBindings builds footer bindings from the configured agents.
func (m Model) agentPickerBindings() []footerBinding {
	binds := make([]footerBinding, len(m.agents))
	for i, a := range m.agents {
		binds[i] = footerBinding{a.Key, a.Name}
	}
	return binds
}

// sessionView is one row per live Claude session — the unit every consumer
// (dashboard, state file, sidebar, notification overrides) actually needs.
// Built once per refresh in the refreshSessions goroutine and stashed on
// the model; rendered by refreshDashSessions + writeStateFile.
//
// Classification by Section + Parent + IsStray:
//   - Known project, main checkout:   Section="projects", Parent="",        IsStray=false
//   - Worktree of known project:      Section="projects", Parent=parent,    IsStray=false, IsWorktree=true
//   - Stray inside a git repo:        Section="projects", Parent="",        IsStray=true
//   - Stray outside any git repo:     Section="external", Parent="",        IsStray=true
//
// For concurrent siblings sharing a target, WindowName and Index disambiguate.
type sessionView struct {
	SessionID   string
	PID         int
	CWD         string
	ProjectPath string        // stable key for aggregation (=cwd, worktree parent, or stray repo-root/cwd)
	ProjectName string        // display name on the row
	Parent      string        // parent project name for worktree/sibling grouping; "" otherwise
	WindowName  string        // real tmux window name from sessionToWindowMap; composed fallback if unresolved
	WindowID    string        // stable tmux window id (e.g. "@5"); empty when window couldn't be resolved
	InstanceID  string        // mo-generated instance ID (from @mo_instance_id window option); empty for pre-refactor windows
	AgentKey    string        // coding agent mnemonic (from @mo_agent window option); empty = default
	Index       int           // 0 bare, 2+ ordinal, -1 custom-title (for ordering siblings)
	Status      SessionStatus // raw status from poll (notif overrides applied in updateProjectStatuses)
	Section     string        // "projects" | "external"
	Branch      string        // git-backed strays only
	Dirty       int           // git-backed strays only
	IsStray     bool
	IsWorktree  bool
	External    bool          // PID not descendant of mo tmux panes (StatusExternal)

	// Team fields — populated when session is part of a Claude Code agent team.
	TeamName  string          // team name from ~/.claude/teams/{name}/config.json
	TeamRole  string          // "lead" or "teammate"
	Teammates []teammateView  // populated for leads only
}

// teammateView describes a teammate pane within a team lead's window.
type teammateView struct {
	Name   string
	Status string // "active" (pane alive), "idle" (pane dead or gone)
	PaneID string // tmux pane ID for focus switching
}

// sessionRefreshMsg carries the per-session view list plus the CWD-keyed
// import-state caches (unchanged — the import-external flow looks them up
// by CWD via dashSessionItem.ProjectPath).
type sessionRefreshMsg struct {
	views            []sessionView
	externalPIDs     map[string]int    // path → orphan PID to kill on import
	externalSessions map[string]string // path → sessionID to resume
}

func (m Model) refreshSessions() tea.Cmd {
	// Snapshot the project list by (path → name) so the goroutine can classify
	// without touching model state.
	projectNames := make(map[string]string)
	for _, item := range m.list.Items() {
		if pi, ok := item.(ProjectItem); ok {
			projectNames[pi.project.Path] = pi.project.Name
		}
	}
	tmuxClient := m.tmux // safe for read off-goroutine
	claudeClient := m.claude
	if claudeClient == nil {
		claudeClient = ops.NewDefaultClaudeReader()
	}
	mgr := m.statusMgr
	watcher := m.statusWatcher
	return func() tea.Msg {
		sessions, _ := claudeClient.LiveSessions()
		var hostPIDs map[int]bool
		if tmuxClient != nil {
			hostPIDs, _ = tmuxClient.PanePIDs()
		}
		// Resolve real window names for every live session by walking pane
		// PID chains once. Used to populate sessionView.WindowName/WindowID/Index.
		windowBySession := resolveSessionWindows(tmuxClient, claudeClient, sessions)

		// Track which sessions are still alive for watcher management.
		liveSIDs := make(map[string]bool, len(sessions))

		views := make([]sessionView, 0, len(sessions))
		externalPIDs := make(map[string]int)
		externalSessions := make(map[string]string)

		for _, s := range sessions {
			liveSIDs[s.SessionID] = true
			isExternal := len(hostPIDs) > 0 && !claudeClient.IsDescendantOf(s.PID, hostPIDs)

			var st SessionStatus
			if isExternal {
				st = StatusExternal
			} else if mgr != nil {
				st = mgrStatusToTUI(mgr.Status(s.SessionID))
				// If the status manager has no state yet for this session,
				// bootstrap it via a JSONL read and start watching.
				if st == StatusNone {
					jsonlPath := claude.ProjectsDirForPath(s.CWD) + "/" + s.SessionID + ".jsonl"
					if mgr != nil {
						mgr.ProcessJSONLChange(s.SessionID, jsonlPath)
						st = mgrStatusToTUI(mgr.Status(s.SessionID))
					}
				}
			}
			// Default to Active if we still don't know.
			if st == StatusNone && !isExternal {
				st = StatusActive
			}

			// Register watcher for JSONL reconciliation.
			if watcher != nil && !isExternal {
				jsonlPath := claude.ProjectsDirForPath(s.CWD) + "/" + s.SessionID + ".jsonl"
				watcher.WatchSession(s.SessionID, jsonlPath)
			}

			v := sessionView{
				SessionID: s.SessionID,
				PID:       s.PID,
				CWD:       s.CWD,
				Status:    st,
				External:  isExternal,
			}

			switch {
			case knownProjectPath(projectNames, s.CWD) != "":
				name := knownProjectPath(projectNames, s.CWD)
				v.ProjectPath = s.CWD
				v.ProjectName = name
				v.Section = "projects"

			case strings.Contains(s.CWD, ".worktrees/"):
				parentPath, parentName, branch := worktreeParent(s.CWD, projectNames)
				v.ProjectPath = parentPath // stable parent key; sidebar still groups by Parent
				v.ProjectName = "@" + branch
				v.Parent = parentName
				v.Section = "projects"
				v.IsWorktree = true
				// ProjectPath intentionally points at the parent project's path
				// so dash ordering (grouping under the parent) works; the
				// session's actual cwd is stored in v.CWD.

			default:
				// Stray: claude in a directory not matching any scanned
				// project and not a worktree. Classify by git-root membership.
				repoRoot := project.FindGitRoot(s.CWD)
				if repoRoot != "" {
					if name, known := projectNames[repoRoot]; known {
						// Stray rooted in a known project — attribute to it.
						v.ProjectPath = repoRoot
						v.ProjectName = name
						v.Section = "projects"
						break
					}
					v.ProjectPath = repoRoot
					v.ProjectName = filepath.Base(repoRoot)
					v.Section = "projects"
					v.IsStray = true
					if gs := project.GetGitStatus(repoRoot); gs.Branch != "" {
						v.Branch = gs.Branch
						v.Dirty = gs.Dirty
					}
				} else {
					v.ProjectPath = s.CWD
					v.ProjectName = filepath.Base(s.CWD)
					v.Section = "external"
					v.IsStray = true
				}
				// Prefer user-chosen label from `claude --name` / `/name`.
				if s.Name != "" {
					v.ProjectName = s.Name
				}
			}

			// Real window name: look up via session→window map; fall back to
			// composed bare/worktree name so dashboards still render even if
			// the session is mid-launch and not yet attached to any pane.
			if w, ok := windowBySession[s.SessionID]; ok {
				v.WindowName = w.Name
				v.WindowID = w.ID
				v.InstanceID = w.InstanceID
				v.AgentKey = w.AgentKey
			} else {
				v.WindowName = composeFallbackWindow(v)
			}
			v.Index = parseWindowIndex(v.WindowName)

			// Import caches: key by CWD (preserves today's dashSessionItem
			// lookup semantics, so `enter` on an External row still finds
			// the PID/sessionID).
			if isExternal {
				externalPIDs[v.CWD] = s.PID
				externalSessions[v.CWD] = s.SessionID
			}

			views = append(views, v)
		}

		// Second pass: detect non-Claude agent windows by their @mo_agent
		// tmux option. These sessions don't appear in claude.LiveSessions()
		// so we must discover them from the window list directly.
		if tmuxClient != nil {
			// Build a set of window IDs already claimed by Claude sessions.
			claimedWindows := make(map[string]bool)
			for _, v := range views {
				if v.WindowID != "" {
					claimedWindows[v.WindowID] = true
				}
			}
			if windows, err := tmuxClient.ListWindows(); err == nil {
				for _, w := range windows {
					if w.AgentKey == "" || w.AgentKey == "c" {
						continue // Claude or unset — already handled above
					}
					if claimedWindows[w.ID] {
						continue // already claimed
					}
					if w.Index == "0" {
						continue // window 0 is the TUI itself
					}
					v := sessionView{
						WindowName: w.Name,
						WindowID:   w.ID,
						InstanceID: w.InstanceID,
						AgentKey:   w.AgentKey,
						CWD:        w.CWD,
						Status:     StatusActive,
						Section:    "projects",
					}
					// Classify by CWD.
					if name := knownProjectPath(projectNames, w.CWD); name != "" {
						v.ProjectPath = w.CWD
						v.ProjectName = name
					} else if strings.Contains(w.CWD, ".worktrees/") {
						parentPath, parentName, branch := worktreeParent(w.CWD, projectNames)
						v.ProjectPath = parentPath
						v.ProjectName = "@" + branch
						v.Parent = parentName
						v.IsWorktree = true
					} else {
						repoRoot := project.FindGitRoot(w.CWD)
						if repoRoot != "" {
							if name, known := projectNames[repoRoot]; known {
								v.ProjectPath = repoRoot
								v.ProjectName = name
							} else {
								v.ProjectPath = repoRoot
								v.ProjectName = filepath.Base(repoRoot)
								v.IsStray = true
								if gs := project.GetGitStatus(repoRoot); gs.Branch != "" {
									v.Branch = gs.Branch
									v.Dirty = gs.Dirty
								}
							}
						} else {
							v.ProjectPath = w.CWD
							v.ProjectName = filepath.Base(w.CWD)
							v.Section = "external"
							v.IsStray = true
						}
					}
					v.Index = parseWindowIndex(v.WindowName)
					views = append(views, v)
				}
			}
		}

		// Third pass: detect Claude Code agent teams. Reads
		// ~/.claude/teams/*/config.json and enriches lead sessions
		// with teammate pane info from their tmux window.
		enrichViewsWithTeamInfo(views, tmuxClient)

		return sessionRefreshMsg{
			views:            views,
			externalPIDs:     externalPIDs,
			externalSessions: externalSessions,
		}
	}
}

// knownProjectPath returns the ProjectItem name for cwd if cwd is a known
// project path, or "" otherwise.
func knownProjectPath(projectNames map[string]string, cwd string) string {
	return projectNames[cwd]
}

// worktreeParent splits a ".worktrees/<branch>" cwd into (parentPath,
// parentName, branch). parentName is "" when the parent project isn't in
// the projectNames map (rare; orphaned worktree).
func worktreeParent(cwd string, projectNames map[string]string) (string, string, string) {
	idx := strings.Index(cwd, ".worktrees/")
	if idx < 0 {
		return "", "", ""
	}
	parentPath := cwd[:idx+len(".worktrees/")-1]
	parentPath = strings.TrimSuffix(parentPath, ".worktrees")
	branch := filepath.Base(cwd)
	return parentPath, projectNames[parentPath], branch
}

// enrichViewsWithTeamInfo calls ops.ListTeams to detect Claude Code agent
// teams and annotates lead session views with teammate pane information.
func enrichViewsWithTeamInfo(views []sessionView, tmuxClient ops.TmuxClient) {
	// Build the sessionID → windowID map that ListTeams needs.
	sessionWindows := make(map[string]string)
	viewBySession := make(map[string]int)
	for i, v := range views {
		if v.SessionID != "" {
			viewBySession[v.SessionID] = i
			if v.WindowID != "" {
				sessionWindows[v.SessionID] = v.WindowID
			}
		}
	}

	teams, err := ops.ListTeams(tmuxClient, sessionWindows)
	if err != nil || len(teams) == 0 {
		return
	}

	for _, ts := range teams {
		idx, ok := viewBySession[ts.LeadSession]
		if !ok {
			continue
		}
		views[idx].TeamName = ts.Name
		views[idx].TeamRole = "lead"
		for _, tm := range ts.Teammates {
			views[idx].Teammates = append(views[idx].Teammates, teammateView{
				Name:   tm.Name,
				Status: tm.Status,
				PaneID: tm.PaneID,
			})
		}
	}
}

// resolveSessionWindows mirrors sessionToWindowMap but takes the sessions as
// a pre-fetched argument so it runs cleanly off-goroutine. Returns the full
// Window so callers get both the (mutable) name and the stable window id.
func resolveSessionWindows(tc ops.TmuxClient, cr ops.ClaudeReader, sessions []claude.Session) map[string]ttmux.Window {
	result := map[string]ttmux.Window{}
	if tc == nil || len(sessions) == 0 {
		return result
	}
	if cr == nil {
		cr = ops.NewDefaultClaudeReader()
	}
	windows, err := tc.ListWindows()
	if err != nil || len(windows) == 0 {
		return result
	}
	for _, w := range windows {
		panePIDs, err := tc.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		for i := range sessions {
			if _, already := result[sessions[i].SessionID]; already {
				continue
			}
			if cr.IsDescendantOf(sessions[i].PID, panePIDs) {
				result[sessions[i].SessionID] = w
			}
		}
	}
	return result
}

// composeFallbackWindow returns the bare composed tmux window name for a
// view when the session's real window can't be resolved via PID chain
// (usually because the session is mid-launch).
func composeFallbackWindow(v sessionView) string {
	if v.IsStray {
		return v.ProjectName
	}
	if v.IsWorktree {
		branch := strings.TrimPrefix(v.ProjectName, "@")
		return ttmux.ComposeWindowName(v.Parent, branch, "")
	}
	return ttmux.ComposeWindowName(v.ProjectName, "", "")
}

// parseWindowIndex returns the sibling ordinal from a window name's
// [suffix]: 0 for a bare window, N for "[N]", -1 for a custom-title suffix
// like "[foo]". Stray windows (which don't follow the project/branch
// naming) return 0 too.
func parseWindowIndex(name string) int {
	_, _, suffix, ok := ttmux.ParseWindowName(name)
	if !ok || suffix == "" {
		return 0
	}
	if n, err := strconv.Atoi(suffix); err == nil {
		return n
	}
	return -1
}

func (m *Model) updateProjectStatuses(polled sessionRefreshMsg) {
	items := m.list.Items()

	// Stash external-session metadata so the import prompt can reach it later.
	m.externalPIDs = polled.externalPIDs
	m.externalSessions = polled.externalSessions

	// Status manager is the source of truth — no notification overrides needed.
	views := polled.views
	m.sessionViews = views

	// Aggregate per project path for the left-column ProjectItem dot.
	// Priority ranking: Permission > Idle > Active > External > None.
	projectAgg := make(map[string]SessionStatus)
	for _, v := range views {
		if v.IsStray {
			continue // strays don't color a ProjectItem
		}
		key := v.ProjectPath
		if v.IsWorktree {
			// Worktree sessions roll up into their parent project's dot.
			// ProjectPath already points at the parent path for worktrees.
		}
		if rank(v.Status) > rank(projectAgg[key]) {
			projectAgg[key] = v.Status
		}
	}

	// Count totals for the title bar.
	activeCount := 0
	attentionCount := 0
	for _, v := range views {
		switch v.Status {
		case StatusExternal:
			// External sessions aren't "mo sessions" — don't count as active.
		case StatusIdle, StatusPermission:
			activeCount++
			attentionCount++
		case StatusActive:
			activeCount++
		}
	}

	for i, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		if st, ok := projectAgg[pi.project.Path]; ok {
			pi.status = st
		} else {
			pi.status = StatusNone
		}
		items[i] = pi
	}

	m.activeSessions = activeCount
	m.attentionCount = attentionCount
	m.list.SetItems(items)
	m.refreshDashSessions()
	m.syncWindowTitles()
	m.writeStateFile()
}

// rank orders SessionStatus values for project-level aggregation.
// Higher = takes priority when a project has multiple sessions.
func rank(s SessionStatus) int {
	switch s {
	case StatusPermission:
		return 4
	case StatusIdle:
		return 3
	case StatusActive:
		return 2
	case StatusExternal:
		return 1
	default:
		return 0
	}
}

// mergeRefreshedProjects takes the existing list items and a freshly-scanned
// project slice, and returns a merged item list. Existing projects keep their
// session status and git status; new projects enter with StatusNone; projects
// no longer in the scan are dropped. The result is sorted by project name.
func mergeRefreshedProjects(existing []list.Item, refreshed []project.Project) []list.Item {
	// Index existing items by path for O(1) lookup.
	byPath := make(map[string]ProjectItem, len(existing))
	for _, item := range existing {
		if pi, ok := item.(ProjectItem); ok {
			byPath[pi.project.Path] = pi
		}
	}

	items := make([]list.Item, 0, len(refreshed))
	for _, p := range refreshed {
		if old, ok := byPath[p.Path]; ok {
			// Preserve status + git, but pick up updated project metadata
			// (name, language, tags) from the fresh scan.
			old.project = p
			items = append(items, old)
		} else {
			items = append(items, ProjectItem{project: p, status: StatusNone})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].(ProjectItem).project.Name < items[j].(ProjectItem).project.Name
	})
	return items
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

func (m *Model) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}

	// Index the session views by the parent they render under.
	// - "projects" (primary + siblings on main checkout): key = projectPath
	// - worktrees:                                          key = projectPath|branch
	// - strays (git-backed): appended after known projects
	// - externals:           appended in their own section
	mainByProject := map[string][]sessionView{}
	worktreeByKey := map[string][]sessionView{}
	var stray, external []sessionView

	for _, v := range m.sessionViews {
		if v.IsStray {
			if v.Section == "external" {
				external = append(external, v)
			} else {
				stray = append(stray, v)
			}
			continue
		}
		if v.IsWorktree {
			key := v.ProjectPath + "|" + strings.TrimPrefix(v.ProjectName, "@")
			worktreeByKey[key] = append(worktreeByKey[key], v)
			continue
		}
		mainByProject[v.ProjectPath] = append(mainByProject[v.ProjectPath], v)
	}

	// Sort siblings within each group by Index (primary=0 first, ordinals next,
	// then custom-title windows). Stable within ties.
	for k := range mainByProject {
		sortViewsForDisplay(mainByProject[k])
	}
	for k := range worktreeByKey {
		sortViewsForDisplay(worktreeByKey[k])
	}

	var projects []state.ProjectState
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}

		// Main-checkout rows: one per live session. If none, emit a
		// placeholder StatusNone row so the sidebar still lists the project.
		views := mainByProject[pi.project.Path]
		if len(views) == 0 {
			projects = append(projects, state.ProjectState{
				Name:       pi.project.Name,
				Path:       pi.project.Path,
				WindowName: pi.project.Name,
				Status:     "none",
				Section:    "projects",
			})
		} else {
			// Main-checkout rows: parent stays "" (only worktree rows get a
			// parent — the sidebar indents them under it).
			for _, v := range views {
				projects = append(projects, viewToProjectState(v, "", pi.project.Name))
			}
		}

		// Worktrees for this project: enumerate distinct branches we saw.
		// Each branch may host multiple sessions (primary + siblings).
		branchesSeen := map[string]bool{}
		var branchOrder []string
		for key := range worktreeByKey {
			if !strings.HasPrefix(key, pi.project.Path+"|") {
				continue
			}
			branch := strings.TrimPrefix(key, pi.project.Path+"|")
			if !branchesSeen[branch] {
				branchesSeen[branch] = true
				branchOrder = append(branchOrder, branch)
			}
		}
		sort.Strings(branchOrder)
		for _, branch := range branchOrder {
			for _, v := range worktreeByKey[pi.project.Path+"|"+branch] {
				rowName := "@" + branch
				projects = append(projects, viewToProjectState(v, pi.project.Name, rowName))
			}
		}
	}

	// Strays: git-backed go into the projects section; externals go in their
	// own section so the sidebar can group them below known projects.
	for _, v := range stray {
		projects = append(projects, viewToProjectState(v, "", v.ProjectName))
	}
	for _, v := range external {
		projects = append(projects, viewToProjectState(v, "", v.ProjectName))
	}

	sessionName := ""
	if m.tmux != nil {
		sessionName = m.tmux.SessionName()
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

// viewToProjectState converts one sessionView into a state.ProjectState.
// rowBaseName + parent control how the Name renders for siblings (e.g.
// "unky-mo [2]", "@feature [debug-oauth]") so the sidebar picks them up
// correctly. Parent is "" for main-checkout rows and strays; the parent
// project name for worktree rows.
func viewToProjectState(v sessionView, parent, rowBaseName string) state.ProjectState {
	name := rowBaseName
	// When the window has a suffix (sibling ordinal or custom title), fold it
	// into the display Name. Primary (Index=0, suffix-less) keeps the base.
	if _, _, suffix, ok := ttmux.ParseWindowName(v.WindowName); ok && suffix != "" {
		name = rowBaseName + " [" + suffix + "]"
	}
	ps := state.ProjectState{
		Name:       name,
		Path:       v.CWD,
		WindowName: v.WindowName,
		WindowID:   v.WindowID,
		InstanceID: v.InstanceID,
		AgentKey:   v.AgentKey,
		Status:     statusToString(v.Status),
		Section:    v.Section,
		SessionID:  v.SessionID,
		Index:      v.Index,
		Parent:     parent,
	}
	if v.IsStray {
		ps.Branch = v.Branch
		ps.Dirty = v.Dirty
	}
	if v.TeamName != "" {
		ps.TeamName = v.TeamName
		ps.TeamRole = v.TeamRole
		for _, tm := range v.Teammates {
			ps.Teammates = append(ps.Teammates, state.TeammateState{
				Name:   tm.Name,
				Status: tm.Status,
				PaneID: tm.PaneID,
			})
		}
	}
	return ps
}

// sortViewsForDisplay orders views within a (project, branch) group so the
// primary-ish row renders first, ordinal siblings next, custom-title
// windows last. Index conventions: 0 = bare primary, 2+ = ordinal, -1 =
// custom title (unknown ordering — fall back to WindowName alphabetical).
func sortViewsForDisplay(views []sessionView) {
	sort.SliceStable(views, func(i, j int) bool {
		ai, bi := views[i].Index, views[j].Index
		// Map to a sort rank: bare(0) < ordinal(>=2) < custom(-1)
		rank := func(n int) int {
			switch {
			case n == 0:
				return 0
			case n >= 2:
				return n
			default:
				return 1_000_000
			}
		}
		ri, rj := rank(ai), rank(bi)
		if ri != rj {
			return ri < rj
		}
		return views[i].WindowName < views[j].WindowName
	})
}

func statusToString(s SessionStatus) string {
	switch s {
	case StatusActive:
		return "active"
	case StatusIdle:
		return "idle"
	case StatusPermission:
		return "permission"
	case StatusExternal:
		return "external"
	default:
		return "none"
	}
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
			var tags []string
			if row.branch.Merged {
				tags = append(tags, "merged")
			}
			if row.branch.RemoteGone {
				tags = append(tags, "gone")
			}
			if selected {
				line := label
				if len(tags) > 0 {
					line = label + "  " + footerDescStyle.Render("["+strings.Join(tags, ", ")+"]")
				}
				left.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				line := headerStyle.Render(label)
				if len(tags) > 0 {
					line += "  " + footerDescStyle.Render("["+strings.Join(tags, ", ")+"]")
				}
				left.WriteString(line + "\n")
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
	case m.liftSessionInput != nil:
		question := fmt.Sprintf("Lift session into new worktree — branch name:")
		footer = m.renderInputPrompt(question, m.liftSessionInput.View(), []footerBinding{
			{"enter", "continue"},
			{"esc", "cancel"},
		})
	case m.pendingLiftDirtyActive:
		q := fmt.Sprintf("Source has uncommitted changes — carry into %s?", m.pendingLiftBranch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"s", "stash + pop"},
			{"l", "leave in source"},
		}))
	case m.pendingWTExistsActive:
		q := fmt.Sprintf("Worktree for %s already exists", m.pendingWTExistsBranch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"f", "focus existing"},
			{"r", "remove + recreate"},
		}))
	case m.pendingCleanupActive:
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	case m.pendingNewMenuActive:
		footer = m.renderPrompt(m.newMenuPromptText(), []footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
			{"esc", "cancel"},
		})
	case m.pendingAgentPickerActive:
		footer = m.renderPrompt("Launch with:", withCancel(m.agentPickerBindings()))
	default:
		var bindings []footerBinding
		if !m.detailFocusLeft && len(m.detailPRs) > 0 {
			enterLabel := "expand"
			if m.detailPRExpanded >= 0 {
				enterLabel = "close"
			}
			bindings = []footerBinding{
				{"o", "github"},
				{"w", "worktree"},
				{"c", "checkout"},
				{"enter", enterLabel},
				{"←→", "switch panel"},
				{"esc", "back"},
			}
		} else {
			bindings = []footerBinding{
				{"↑↓", "select"},
				{"←→", "panel"},
				{"enter", "resume"},
				{"A", "agent"},
				{"w", "worktree"},
				{"m", "main"},
				{"M", "stash+main"},
				{"W", "new branch"},
				{"n", "session"},
				{"x", "remove"},
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
		jsonl := filepath.Join(m.claude.ProjectsDirForPath(launchPath), rs.SessionID+".jsonl")
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

		sessions := m.claude.RecentSessions(launchPath, 5)
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
	m.detailRecap = m.claude.LastMessages(row.path, row.session.SessionID, 6)
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

func (m *Model) routeNotificationToStatusMgr(n notify.Notification) {
	if m.statusMgr == nil {
		return
	}
	key := n.SessionID
	if key == "" {
		key = n.ProjectPath
	}
	var evt status.HookEvent
	evt.SessionID = key
	evt.ProjectPath = n.ProjectPath
	switch n.Type {
	case notify.NotifyIdlePrompt:
		evt.Type = status.EventNotificationIdle
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs input", filepath.Base(n.ProjectPath)))
	case notify.NotifyPermissionPrompt:
		evt.Type = status.EventNotificationPerm
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs permission", filepath.Base(n.ProjectPath)))
	case notify.NotifySessionStop:
		evt.Type = status.EventStop
	default:
		return
	}
	m.statusMgr.ProcessHookEvent(evt)
}

// mgrStatusToTUI converts a status.SessionStatus to the TUI's SessionStatus.
func mgrStatusToTUI(s status.SessionStatus) SessionStatus {
	switch s {
	case status.StatusActive:
		return StatusActive
	case status.StatusIdle:
		return StatusIdle
	case status.StatusPermission:
		return StatusPermission
	case status.StatusExternal:
		return StatusExternal
	default:
		return StatusNone
	}
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

type statusMsgEvent string
type clearStatusMsg struct{}

// worktreeExistsMsg signals that a worktree for the requested branch already
// exists. The TUI opens a multi-option prompt so the user can choose to focus
// the existing session, remove and recreate, or cancel.
type worktreeExistsMsg struct {
	branch       string
	worktreePath string
	prBranch     bool // true when triggered from a PR (needs git fetch + reset on recreate)
}

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
			var existsErr *project.ErrWorktreeExists
			if errors.As(err, &existsErr) {
				return worktreeExistsMsg{
					branch:       pr.Branch,
					worktreePath: existsErr.WorktreePath,
					prBranch:     true,
				}
			}
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}

		// Reset the worktree to the remote branch to ensure it's up to date
		_ = exec.Command("git", "-C", wtPath, "reset", "--hard", "origin/"+pr.Branch).Run()

		windowName := p.Name + "@" + pr.Branch
		var status string
		// Prefer focusing a live (possibly renamed) session at this target.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, pr.Branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Worktree ready but failed to switch: %v", err)
			} else {
				status = "Switched to " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Worktree ready but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
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
			localDir := m.claude.ProjectsDirForPath(localPath)
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

// safeWindowTarget constructs a tmux target string for the given window,
// preferring the stable window ID (@N) when available. This avoids tmux
// misinterpreting dots in window names (e.g. "moma.org.cubed") as pane
// separators.
func (m Model) safeWindowTarget(name, windowID string) string {
	session := m.tmux.SessionName()
	if windowID != "" {
		return session + ":" + windowID
	}
	if !ttmux.NeedsSafeTarget(name) {
		return session + ":" + name
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return session + ":" + name
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

// cleanupPrompt returns the stage-appropriate question + bindings for the
// `x` cleanup menu (kill-sessions confirmation first, then action menu).
// Follows the prompt convention: yes/no uses [y/N]; multi-option menus end
// with a [n] cancel hint.
func (m Model) cleanupPrompt() (string, []footerBinding) {
	switch m.pendingCleanupStage {
	case "kill":
		n := len(m.pendingCleanupSessions)
		q := fmt.Sprintf("⚠ %d live session(s) in %s — kill them? [y/N]", n, m.pendingCleanupBranch)
		return q, yesNoBindings("kill + continue")
	case "action":
		if m.pendingCleanupWorktreePath != "" {
			q := fmt.Sprintf("Remove worktree %s?", m.pendingCleanupBranch)
			return q, withCancel([]footerBinding{
				{"w", "worktree only"},
				{"b", "worktree + branch"},
			})
		}
		q := fmt.Sprintf("Delete branch %s? [y/N]", m.pendingCleanupBranch)
		return q, yesNoBindings("delete branch")
	}
	return "", nil
}

// newMenuPromptText renders the question shown above the s/p/c/esc menu.
// Names the primary target so the user knows what they're about to affect.
// If the menu was opened to resume a specific historical session, the
// prompt mentions that too so `p` / `c` semantics are unambiguous.
func (m Model) newMenuPromptText() string {
	target := m.pendingNewPrimaryWin
	if target == "" {
		target = "this target"
	}
	if m.pendingNewResumeSession != "" {
		return fmt.Sprintf("A different session is running in %s — how should the selected session be resumed?", target)
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
	m.pendingNewResumeSession = ""
}

func (m *Model) clearAgentPicker() {
	m.pendingAgentPickerActive = false
	m.pendingAgentPickerBranch = nil
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

// clearPendingCleanupMenu resets all pendingCleanup* fields so the menu closes.
func (m *Model) clearPendingCleanupMenu() {
	m.pendingCleanupActive = false
	m.pendingCleanupStage = ""
	m.pendingCleanupProjectPath = ""
	m.pendingCleanupProjectName = ""
	m.pendingCleanupBranch = ""
	m.pendingCleanupWorktreePath = ""
	m.pendingCleanupSessions = nil
}

func (m *Model) clearPendingWTExists() {
	m.pendingWTExistsActive = false
	m.pendingWTExistsBranch = ""
	m.pendingWTExistsProjectPath = ""
	m.pendingWTExistsProjectName = ""
	m.pendingWTExistsWTPath = ""
	m.pendingWTExistsPRBranch = false
}

// focusExistingWorktree tries to switch to the existing worktree's session
// window. If no live session exists, launches a new one in the worktree dir.
func (m Model) focusExistingWorktree(projectName, branch, wtPath string) tea.Cmd {
	return func() tea.Msg {
		windowName := projectName + "@" + branch
		if attempted, realWin, err := m.focusPrimaryIfLive(projectName, branch, wtPath); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
	}
}

// removeAndRecreateWorktree removes the existing worktree and recreates it.
// For PR branches, re-fetches and resets to the remote branch.
func (m Model) removeAndRecreateWorktree(projectPath, projectName, branch string, prBranch bool) tea.Cmd {
	return func() tea.Msg {
		if err := project.RemoveWorktree(projectPath, branch); err != nil {
			return statusMsgEvent(fmt.Sprintf("Remove failed: %v", err))
		}
		if prBranch {
			_ = exec.Command("git", "-C", projectPath, "fetch", "origin", branch).Run()
		}
		wtPath, err := project.CreateWorktree(projectPath, branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Recreate failed: %v", err))
		}
		if prBranch {
			_ = exec.Command("git", "-C", wtPath, "reset", "--hard", "origin/"+branch).Run()
		}
		windowName := projectName + "@" + branch
		var status string
		if attempted, realWin, err := m.focusPrimaryIfLive(projectName, branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Recreated worktree but failed to switch: %v", err)
			} else {
				status = "Switched to " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Recreated worktree but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
	}
}

// killCleanupSessions is a thin TUI adapter over ops.CleanupWorktree's
// kill-only mode (DeleteBranch=false, no worktree removal — just the kill
// step). We pass an empty branch to skip the worktree removal; the callers
// always follow up with runCleanup for the actual removal.
func (m Model) killCleanupSessions(sessions []claude.Session) tea.Cmd {
	return func() tea.Msg {
		// Kill-only: re-use ops.CleanupWorktree would also try to remove a
		// worktree by branch, which we don't have here. Use ops.SignalAndWaitExit
		// + window resolution directly.
		windowBySession := map[string]string{}
		if windows, err := m.tmux.ListWindows(); err == nil {
			for _, w := range windows {
				panePIDs, err := m.tmux.WindowPanePIDs(w.ID)
				if err != nil {
					continue
				}
				for _, s := range sessions {
					if _, already := windowBySession[s.SessionID]; already {
						continue
					}
					if m.claude.IsDescendantOf(s.PID, panePIDs) {
						windowBySession[s.SessionID] = w.ID
					}
				}
			}
		}
		for _, s := range sessions {
			ops.SignalAndWaitExit(m.ops, s.PID)
			if wID, ok := windowBySession[s.SessionID]; ok {
				_ = m.tmux.KillWindow(m.tmux.SessionName() + ":" + wID)
			}
		}
		return statusMsgEvent(fmt.Sprintf("Killed %d session(s)", len(sessions)))
	}
}

// runCleanup is a thin TUI adapter over ops.CleanupWorktree.
func (m Model) runCleanup(projectPath, branch string, alsoDeleteBranch bool) tea.Cmd {
	return func() tea.Msg {
		res, err := ops.CleanupWorktree(m.ops, ops.CleanupParams{
			ProjectPath:  projectPath,
			Branch:       branch,
			DeleteBranch: alsoDeleteBranch,
		})
		if err != nil {
			return branchesChangedMsg{status: err.Error()}
		}
		return branchesChangedMsg{status: res.Status}
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
		// Prefer focusing a live (possibly renamed) primary session at this
		// target. Only fall back to the composed bare name for leftover
		// dead-window edge cases.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wt.Path); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		windowName := p.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wt.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
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
		// Prefer focusing a live (possibly renamed) session at this target.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Pulled but failed to switch: %v", err)
			} else {
				status = "Resumed " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
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

// createWorktreeAndLaunch is a thin TUI adapter over ops.CreateWorktreeAndLaunch.
func (m Model) createWorktreeAndLaunch(branch string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.CreateWorktreeAndLaunch(m.ops, ops.WorktreeParams{
			ProjectName: p.Name,
			ProjectPath: p.Path,
			Branch:      branch,
		})
		if err != nil {
			return worktreeCreatedMsg{status: err.Error()}
		}
		if res.ExistsConflict {
			return worktreeExistsMsg{
				branch:       branch,
				worktreePath: res.WorktreePath,
				prBranch:     false,
			}
		}
		return worktreeCreatedMsg{status: res.Status}
	}
}

// sessionLiftedMsg signals a completed lift. Distinct from worktreeCreatedMsg
// because its handler also kicks off a session refresh so the newly-relocated
// JSONL (now under the new worktree's encoded-cwd dir) attributes to the new
// branch immediately, without the user having to leave and re-enter
// ScreenProject.
type sessionLiftedMsg struct{ status string }

// liftSessionToWorktree is a thin TUI adapter over ops.LiftSessionToWorktree.
// Triggered by `w` on a br-session row once the user has picked a new branch
// name and decided what to do with any dirty state at the source.
func (m Model) liftSessionToWorktree(sessionID, sourcePath, sourceWindow string, sourcePID int, newBranch string, stashAndPop bool) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.LiftSessionToWorktree(m.ops, ops.LiftParams{
			ProjectName:  p.Name,
			SourcePath:   sourcePath,
			SessionID:    sessionID,
			SourcePID:    sourcePID,
			SourceWindow: sourceWindow,
			NewBranch:    newBranch,
			StashAndPop:  stashAndPop,
		})
		if err != nil {
			return sessionLiftedMsg{status: fmt.Sprintf("Lift failed: %v", err)}
		}
		return sessionLiftedMsg{status: res.Status}
	}
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

// sessionImportedMsg signals that an external claude was successfully pulled
// into mo's tmux. Update handles it by reporting the status *and* kicking
// off an immediate sessionRefresh so the main TUI + state file (and the
// sidebar that polls it) pick the new window up without waiting for the
// next 5s tick.
type sessionImportedMsg struct{ status string }

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

// currentDetailRow returns the full detail row under the cursor (branch,
// session, kind, tmuxWindow) or nil if out of range. Used by `w` to tell a
// session row apart from a plain branch row so they can route to different
// flows.
func (m Model) currentDetailRow() *detailRow {
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return nil
	}
	return &m.detailRows[m.detailCursor]
}

// startLiftSessionPrompt opens the new-branch text input for the lift flow
// and snapshots the source session (PID, window) so later cursor moves can't
// invalidate the target.
func (m Model) startLiftSessionPrompt(row *detailRow) (tea.Model, tea.Cmd) {
	if row == nil || row.session == nil {
		return m, nil
	}
	pid := 0
	window := ""
	if row.session.IsLive {
		// Recover the live PID by matching SessionID against LiveSessions().
		// RecentSession itself only carries IsLive/SessionID; the PID + pane
		// window live on the live-session record.
		if live, err := m.claude.LiveSessions(); err == nil {
			for i := range live {
				if live[i].SessionID == row.session.SessionID {
					pid = live[i].PID
					break
				}
			}
		}
		window = row.tmuxWindow
	}
	m.liftSessionSessionID = row.session.SessionID
	m.liftSessionSourcePath = row.path
	m.liftSessionSourcePID = pid
	m.liftSessionSourceWindow = window

	ti := textinput.New()
	ti.Placeholder = "new branch name"
	ti.Focus()
	ti.CharLimit = 128
	ti.SetWidth(40)
	m.liftSessionInput = &ti
	return m, textinput.Blink
}

// decideLiftDirty runs after the user submits a branch name. If the source
// is dirty, we open the [s]/[l]/[n] menu; otherwise we run the lift directly.
func (m Model) decideLiftDirty(newBranch string) (tea.Model, tea.Cmd) {
	m.pendingLiftBranch = newBranch
	dirty, err := project.IsDirty(m.liftSessionSourcePath)
	if err != nil {
		// Surface the error and bail — don't press on with an uncertain state.
		cmd := func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("Lift failed: %v", err))
		}
		m.clearLiftSessionState()
		return m, cmd
	}
	if dirty {
		m.pendingLiftDirtyActive = true
		return m, nil
	}
	cmd := m.runLiftSession(false)
	return m, cmd
}

// runLiftSession hands control to the ops adapter with the captured source
// info and the user's stash-or-leave decision.
func (m Model) runLiftSession(stashAndPop bool) tea.Cmd {
	return m.liftSessionToWorktree(
		m.liftSessionSessionID,
		m.liftSessionSourcePath,
		m.liftSessionSourceWindow,
		m.liftSessionSourcePID,
		m.pendingLiftBranch,
		stashAndPop,
	)
}

// clearLiftSessionState resets every field touched by the lift flow so a
// fresh invocation starts from a clean slate.
func (m *Model) clearLiftSessionState() {
	m.liftSessionInput = nil
	m.liftSessionSessionID = ""
	m.liftSessionSourcePath = ""
	m.liftSessionSourcePID = 0
	m.liftSessionSourceWindow = ""
	m.pendingLiftDirtyActive = false
	m.pendingLiftBranch = ""
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
		return func() tea.Msg { return m.launchAgentInWindow(p.Name, p.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key) }
	default:
		return m.createWorktreeAndLaunch(b.Name)
	}
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

// launchWorktreeSessionWithAgent mirrors launchWorktreeSession but uses the
// given agent command instead of "claude".
func (m Model) launchWorktreeSessionWithAgent(wt project.Worktree, agent config.AgentConfig) tea.Cmd {
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
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wt.Path); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		windowName := p.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wt.Path, agent.Cmd, agent.Name, agent.Key)
	}
}

// createWorktreeAndLaunchWithAgent mirrors createWorktreeAndLaunch but passes
// the agent command through to the ops layer.
func (m Model) createWorktreeAndLaunchWithAgent(branch string, agent config.AgentConfig) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.CreateWorktreeAndLaunch(m.ops, ops.WorktreeParams{
			ProjectName: p.Name,
			ProjectPath: p.Path,
			Branch:      branch,
			ShellCmd:    agent.Cmd,
			AgentKey:    agent.Key,
		})
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		if res.ExistsConflict {
			return worktreeExistsMsg{
				branch:       branch,
				worktreePath: res.WorktreePath,
			}
		}
		return worktreeCreatedMsg{status: res.Status}
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
		if m.claude.SessionForPath(p.Path) != nil {
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
			if se, ok := m.launchAgentInWindow(p.Name, p.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key).(statusMsgEvent); ok {
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

func Run(projects []project.Project, tmuxSession, socketPath, stateFilePath string, ticketsCfg config.TicketsConfig, agents []config.AgentConfig, workspaceDirs []string, manualProjects []project.Project) error {
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
		// Install respawn hooks on window 0 so the home TUI survives crashes
		// and accidental kills. disableHomeRespawn() tears these down before
		// an intentional quit.
		installHomeRespawnHooks(tc)
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

	m := NewModel(projects, tc, ns, stateFilePath, ticketsCfg, agents, workspaceDirs, manualProjects)
	p := tea.NewProgram(m)
	_, err := p.Run()

	// Clean up state file on exit
	state.Remove(stateFilePath)

	return err
}
