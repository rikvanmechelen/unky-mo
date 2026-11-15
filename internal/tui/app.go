package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/status"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
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

func sessionTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return sessionTickMsg(t)
	})
}

func usageTick() tea.Cmd {
	return tea.Tick(60*time.Second, func(t time.Time) tea.Msg {
		return usageTickMsg(t)
	})
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

func ticketsTick(period time.Duration) tea.Cmd {
	return tea.Tick(period, func(t time.Time) tea.Msg {
		return ticketsTickMsg(t)
	})
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

func (m Model) refreshGitStatuses() tea.Cmd {
	return func() tea.Msg {
		statuses := make(map[string]project.GitStatus)
		for _, p := range m.projects {
			statuses[p.Path] = project.GetGitStatus(p.Path)
		}
		return gitStatusMsg(statuses)
	}
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
	statusMgr      *status.Manager            // central source of truth for session statuses
	statusWatcher  *status.Watcher            // fsnotify watcher for JSONL reconciliation
	statusSub      <-chan status.StatusChange // subscription to status changes
	statusMsg      string
	startupNotice  string // emitted once as a statusMsgEvent from Init
	startupSticky  bool   // startupNotice stays up until a keypress (e.g. web CA not trusted)
	statusSticky   bool   // statusMsg came from a stickyStatusMsgEvent
	activeSessions int
	attentionCount int
	gitStatuses    map[string]project.GitStatus // project path → git status
	// Dashboard active sessions panel (right side)
	dashFocusLeft     bool // true = project list, false = sessions/tickets panel
	dashSessionItems  []dashSessionItem
	dashSessionCursor int
	// Dashboard right-panel focus: sessions (top) vs tickets (bottom). Only
	// relevant when dashFocusLeft is false.
	dashRightFocus dashRightSection
	// Tickets state (populated by background ticketsFetch).
	ticketsDisabled  bool // explicit opt-out via [tickets] enabled = false
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
	ticketProjectMap     map[string]map[string]string
	ticketsInstances     []jira.Instance // retained for NeedsConfig re-check on token env changes
	ticketsGroups        []tickets.BucketGroup
	ticketsErrors        []tickets.FetchResult
	ticketsLoaded        bool // false until first fetch completes
	ticketsLoading       bool // true while a fetch is in flight
	ticketsLastFetch     time.Time
	ticketsCursor        int // index into the flat visible list below
	ticketsPerBucket     int // config-driven overflow cap
	ticketsRefreshPeriod time.Duration
	// Buckets the user has expanded via enter on the overflow row. Expansion
	// is session-scoped (not persisted) — the default per-bucket cap applies
	// again on next launch, which is the behavior the user most often wants.
	ticketsExpanded map[tickets.Bucket]bool
	// Detail views
	detailProject   *project.Project
	detailSession   *claude.Session
	detailWorktrees []project.Worktree
	detailBranches  []project.Branch
	detailLoading   bool // the branches are being listed (loadDetail)
	// detailRows is a flat list of navigable items in the project detail view:
	// one row per local branch, each followed by the sessions discovered at
	// that branch's checkout location (main path or worktree path).
	detailRows   []detailRow
	detailCursor int
	detailRecap  []claude.SessionMessage // last messages for currently selected session
	// Import-external-session prompt: non-empty means we're asking the user
	// whether to take over a claude running outside mo (kill it + resume here).
	importExternal *importPrompt
	// New-session menu: active when the user pressed `n` on a target that
	// already has a live session, or `enter` on a historical session row
	// whose primary window is occupied by a different session. Presents
	// s/p/c/esc options (switch / park+new / concurrent / cancel). Captured
	// per-field so the menu survives cursor movement without re-querying.
	newSession *newSessionPrompt
	// Agent picker menu: shown when shift+enter is pressed on a branch row.
	agentPicker *agentPickerPrompt
	// Cleanup menu: active when the user pressed `x` on a branch row.
	// Two stages: "kill" (one or more sessions live in the target; user
	// must confirm SIGINT) then "action" (choose [w] worktree only / [b]
	// worktree + branch / [esc]). Entry skips "kill" when no sessions.
	cleanup *cleanupMenu
	// Worktree-exists prompt: shown when creating a worktree for a branch
	// that already has one. Options: focus existing, remove + recreate, cancel.
	worktreeExists *worktreeExistsPrompt
	// Suspend-and-quit prompt: active when q pressed with active sessions.
	suspendQuit *suspendQuitPrompt
	// externalPIDs / externalSessions cache the orphan PID + sessionID for each
	// CWD currently in StatusExternal, populated by refreshSessions.
	externalPIDs     map[string]int
	externalSessions map[string]string
	// sessionViews is the source of truth for dashboard rows + state file
	// entries: one entry per live Claude session. Populated by
	// updateProjectStatuses from the poll result (status manager is source of truth).
	sessionViews []sessionView
	// newProject is non-nil while the user enters a folder name for a new
	// project (N on the dashboard); key events route to it.
	newProject *newProjectPrompt
	// worktree is non-nil while the user enters a branch name for a new
	// worktree (W on the project screen).
	worktree *worktreePrompt
	// lift is non-nil during the lift-to-new-worktree flow (w on a session
	// row): first the branch name, then, if the source is dirty, what to do
	// with its changes. The source session is captured when it opens, so
	// cursor moves can't change the target.
	lift *liftPrompt
	// Pull requests panel (right side of project detail)
	detailPRs        []gh.PullRequest
	detailPRCursor   int
	detailPRErr      string                        // error message if gh failed
	detailPRExpanded int                           // index of expanded PR (-1 = none)
	detailPRDetail   *gh.PRDetail                  // fetched detail for expanded PR
	detailFocusLeft  bool                          // true = left panel (sessions/worktrees), false = right (PRs)
	syncedSessions   map[string]moSync.SessionMeta // sync metadata keyed by session ID for the current detail project
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

	// restartRequested is set by ctrl+alt+r; Run re-execs the binary after
	// the program quits.
	restartRequested bool
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
	l.SetShowHelp(false)       // We use our own footer
	l.DisableQuitKeybindings() // We handle q/esc ourselves
	l.InfiniteScrolling = true // Circular navigation
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
		screen:               ScreenDashboard,
		list:                 l,
		projects:             projects,
		tmux:                 tmuxClient,
		claude:               claudeReader,
		ops:                  opsCtx,
		agents:               agents,
		agentChoices:         agentChoices,
		notifServer:          notifServer,
		statusMgr:            statusMgr,
		statusWatcher:        statusWatcher,
		statusSub:            statusSub,
		dashFocusLeft:        true,
		stateFilePath:        stateFilePath,
		workspaceDirs:        workspaceDirs,
		manualProjects:       manualProjects,
		ticketsDisabled:      ticketsCfg.Disabled,
		ticketsProviders:     providers,
		ticketProjectMap:     projectMap,
		ticketsExpanded:      map[tickets.Bucket]bool{},
		ticketsInstances:     instances,
		ticketsPerBucket:     perBucket,
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
	if m.startupNotice != "" {
		notice := m.startupNotice
		if m.startupSticky {
			cmds = append(cmds, func() tea.Msg { return stickyStatusMsgEvent(notice) })
		} else {
			cmds = append(cmds, func() tea.Msg { return statusMsgEvent(notice) })
		}
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
		footerHeight := 6
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
		return m.onKey(msg)

	case sessionTickMsg:
		return m.onSessionTick(msg)

	case projectsRefreshMsg:
		return m.onProjectsRefresh(msg)

	case newProjectCreatedMsg:
		return m.onNewProjectCreated(msg)

	case suspendCompleteMsg:
		return m.onSuspendComplete(msg)

	case sessionRefreshMsg:
		return m.onSessionRefresh(msg)

	case usageTickMsg:
		return m.onUsageTick(msg)

	case usageRefreshMsg:
		return m.onUsageRefresh(msg)

	case ticketsTickMsg:
		return m.onTicketsTick(msg)

	case ticketsRefreshMsg:
		return m.onTicketsRefresh(msg)

	case ticketDetailMsg:
		return m.onTicketDetail(msg)

	case gitStatusMsg:
		return m.onGitStatus(msg)

	case notificationMsg:
		return m.onNotification(msg)

	case statusChangeMsg:
		return m.onStatusChange(msg)

	case sessionImportedMsg:
		return m.onSessionImported(msg)

	case stickyStatusMsgEvent:
		return m.onStickyStatusMsgEvent(msg)

	case statusMsgEvent:
		return m.onStatusMsgEvent(msg)

	case clearStatusMsg:
		return m.onClearStatus(msg)

	case prFetchMsg:
		return m.onPRFetch(msg)

	case prDetailMsg:
		return m.onPRDetail(msg)

	case syncPullMsg:
		return m.onSyncPull(msg)

	case detailLoadedMsg:
		return m.onDetailLoaded(msg)

	case worktreeExistsMsg:
		return m.onWorktreeExists(msg)

	case worktreeCreatedMsg:
		return m.onWorktreeCreated(msg)

	case sessionLiftedMsg:
		return m.onSessionLifted(msg)

	case branchesChangedMsg:
		return m.onBranchesChanged(msg)
	}
	return m.updateNav(msg)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isErrorStatus reports whether a status message reads as an error, which
// keeps it up until a keypress instead of auto-clearing.
func isErrorStatus(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "err:")
}

// startupNotice, if non-empty, is shown in the status bar on first paint
// (e.g. results of the CLI's pre-TUI installation checks); startupSticky
// keeps it there until a keypress instead of the usual 4s.
func Run(projects []project.Project, tmuxSession, socketPath, stateFilePath string, ticketsCfg config.TicketsConfig, agents []config.AgentConfig, workspaceDirs []string, manualProjects []project.Project, startupNotice string, startupSticky bool) error {
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
		// Not fatal, but not quiet either: without the socket no hook event
		// arrives, and status falls back to slower signals.
		ns = nil
		startupNotice = strings.TrimSpace(fmt.Sprintf("hook socket %s: %v — session status will lag. %s", socketPath, err, startupNotice))
		startupSticky = true
	}
	if ns != nil {
		defer ns.Stop()
	}

	m := NewModel(projects, tc, ns, stateFilePath, ticketsCfg, agents, workspaceDirs, manualProjects)
	m.startupNotice = startupNotice
	m.startupSticky = startupSticky
	p := tea.NewProgram(m)
	final, err := p.Run()

	if fm, ok := final.(Model); ok && err == nil && fm.restartRequested {
		return restartInPlace(ns)
	}

	// Clean up state file on exit
	state.Remove(stateFilePath)

	return err
}
