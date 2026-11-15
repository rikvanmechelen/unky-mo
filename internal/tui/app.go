package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	WindowID    string // stable tmux window ID (@N); used for safe target construction
	AgentKey    string // coding agent mnemonic; empty = default (Claude)
	Status      SessionStatus
	ProjectPath string             // cwd — used to resolve external PID/sessionID on import
	Section     string             // "projects" (default) or "external"
	Git         *project.GitStatus // set for git-backed strays; nil otherwise
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

	// A sticky notice (e.g. the web CA isn't trusted, with the command that
	// fixes it) gets its own full-width block: the list's status line only
	// spans the left panel and would cut the instructions off.
	stickyNotice := ""
	if m.statusSticky && m.statusMsg != "" {
		stickyNotice = notifBadgeStyle.Width(totalWidth).Render(m.statusMsg)
	}

	// Resize list to fit the left panel
	footerHeight := 6
	if stickyNotice != "" {
		footerHeight += lipgloss.Height(stickyNotice)
	}
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
	if m.cleanup != nil {
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	} else if m.newSession != nil {
		footer = m.renderPrompt(m.newMenuPromptText(), withCancel([]footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
		}))
	} else if m.suspendQuit != nil {
		question := fmt.Sprintf("%d active session(s) — suspend all and quit?", m.activeSessions)
		footer = m.renderPrompt(question, withCancel([]footerBinding{
			{"S", "suspend + quit"},
		}))
	} else if m.importExternal != nil {
		question := fmt.Sprintf("%q is running outside Unky Mo. Import it? (kills the external claude and resumes here) [y/N]", m.importExternal.project)
		footer = m.renderPrompt(question, yesNoBindings("import"))
	} else if m.agentPicker != nil {
		footer = m.renderPrompt("Launch with:", withCancel(m.agentPickerBindings()))
	} else if m.newProject != nil {
		question := fmt.Sprintf("New project in %s — folder name:", m.newProject.parentDir)
		footer = m.renderInputPrompt(question, m.newProject.input.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	} else {
		footer = m.renderFooter([]footerBinding{
			{"↑↓", "navigate"},
			{"←→", "switch panel"},
			{"enter", "open"},
			{"A", "agent"},
			{"n", "new session"},
			{"N", "new project"},
			{"a", "attach"},
			{"/", "filter"},
			{"?", "help"},
			{"s", "suspend"},
			{"q", "quit"},
		})
	}

	parts := []string{body}
	if stickyNotice != "" {
		parts = append(parts, stickyNotice)
	}
	parts = append(parts, usageStrip, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
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
	return ops.AgentShellCmd(&agent, sessionID)
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
	External    bool // PID not descendant of mo tmux panes (StatusExternal)

	// Set only while Status is StatusQuestion or StatusPermission: the tool
	// call Claude is blocked on (may be empty, see status.Manager.Pending).
	PendingTool  string
	PendingInput json.RawMessage

	// Team fields — populated when session is part of a Claude Code agent team.
	TeamName  string         // team name from ~/.claude/teams/{name}/config.json
	TeamRole  string         // "lead" or "teammate"
	Teammates []teammateView // populated for leads only
}

// teammateView describes a teammate pane within a team lead's window.
type teammateView struct {
	Name   string
	Status string // "active" (pane alive), "idle" (pane dead or gone)
	PaneID string // tmux pane ID for focus switching
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
		// Taken before the CLI call so any hook landing during it counts as
		// fresher than this snapshot (see status.Manager.ProcessAgentStatus).
		observedAt := time.Now()
		sessions, listErr := claudeClient.LiveSessions()
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
				mgr.ProcessAgentStatus(s.SessionID, s.Status, s.WaitingFor, observedAt)
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
			if mgr != nil && (st == StatusQuestion || st == StatusPermission) {
				// A question or permission prompt the hooks didn't deliver
				// (TUI restarted while it was open, hook dropped) is read back
				// from the transcript.
				jsonlPath := claude.ProjectsDirForPath(s.CWD) + "/" + s.SessionID + ".jsonl"
				v.PendingTool, v.PendingInput, _ = mgr.RecoverPending(s.SessionID,
					func(st status.SessionStatus) (string, json.RawMessage, bool) {
						return status.ReadPendingTool(jsonlPath, st == status.StatusQuestion)
					})
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

		// Sessions that ended without their SessionEnd hook (killed, the
		// window closed) are forgotten, and their JSONL is no longer
		// watched. Only from a listing that worked: a failed one lists
		// nobody.
		if listErr == nil && mgr != nil {
			for _, id := range mgr.Prune(liveSIDs, observedAt) {
				if watcher != nil {
					watcher.UnwatchSession(id)
				}
			}
		}

		// Claude Code can run a window's actual conversation through a
		// background-hosted process (observed on a session with auto mode
		// on: a content-less "interactive" process in the pane, plus a
		// same-cwd "background"-kind process doing the real work). Both
		// resolve to the same window via the ancestry walk above, so the
		// window would show two rows — and the web chat view would tail the
		// empty one. Drop the transcript-less row where a sibling has one.
		views = dedupeSameWindowSessions(views)

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

// dedupeSameWindowSessions drops transcript-less sessionViews that share a
// tmux window with a session that does have a transcript.
//
// `claude agents --json` can report more than one live process for a single
// pane: observed with a same-cwd "background"-kind session doing the real
// work alongside an "interactive" one that never writes its own JSONL. Both
// resolve to the same window via the PID-ancestry walk, so the window gets
// two rows — and the web chat view, which tails the row's transcript, picks
// the empty one and renders nothing.
//
// The rule is deliberately narrow: a row is only dropped when it has no
// transcript AND a sibling on the same window has one. Several real
// sessions legitimately share a window — an agent team's teammates run as
// panes in the lead's window, each with its own session ID and transcript
// (see enrichViewsWithTeamInfo) — and those must all survive.
func dedupeSameWindowSessions(views []sessionView) []sessionView {
	// Which windows have at least one transcript-bearing session?
	hasReal := make(map[string]bool)
	transcript := make([]bool, len(views))
	for i, v := range views {
		if v.Section != "projects" || v.WindowID == "" {
			continue
		}
		transcript[i] = sessionHasTranscript(v)
		if transcript[i] {
			hasReal[v.WindowID] = true
		}
	}
	if len(hasReal) == 0 {
		return views
	}
	out := views[:0]
	for i, v := range views {
		if v.Section == "projects" && v.WindowID != "" && !transcript[i] && hasReal[v.WindowID] {
			continue // a sibling on this window carries the real conversation
		}
		out = append(out, v)
	}
	return out
}

// sessionHasTranscript reports whether v's session has written a non-empty
// JSONL transcript.
func sessionHasTranscript(v sessionView) bool {
	if v.SessionID == "" || v.CWD == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(claude.ProjectsDirForPath(v.CWD), v.SessionID+".jsonl"))
	return err == nil && info.Size() > 0
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
			} else if n, err := strconv.Atoi(suffix); err == nil && n >= 2 {
				// Already an ordinal sibling — keep its slot. Recomputing
				// would count this window's own name as taken and flip it
				// between [2] and [3] on every tick.
				continue
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

func statusToString(s SessionStatus) string {
	switch s {
	case StatusActive:
		return "active"
	case StatusIdle:
		return "idle"
	case StatusPermission:
		return "permission"
	case StatusQuestion:
		return "question"
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
		if m.detailLoading {
			left.WriteString("  " + footerDescStyle.Render("Loading branches…") + "\n")
		} else {
			left.WriteString("  " + footerDescStyle.Render("No branches found") + "\n")
		}
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
	case m.worktree != nil:
		question := fmt.Sprintf("Create new worktree for %s — branch name:", p.Name)
		footer = m.renderInputPrompt(question, m.worktree.input.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	case m.lift != nil && !m.lift.dirty:
		question := fmt.Sprintf("Lift session into new worktree — branch name:")
		footer = m.renderInputPrompt(question, m.lift.input.View(), []footerBinding{
			{"enter", "continue"},
			{"esc", "cancel"},
		})
	case m.lift != nil && m.lift.dirty:
		q := fmt.Sprintf("Source has uncommitted changes — carry into %s?", m.lift.branch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"s", "stash + pop"},
			{"l", "leave in source"},
		}))
	case m.worktreeExists != nil:
		q := fmt.Sprintf("Worktree for %s already exists", m.worktreeExists.branch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"f", "focus existing"},
			{"r", "remove + recreate"},
		}))
	case m.cleanup != nil:
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	case m.newSession != nil:
		footer = m.renderPrompt(m.newMenuPromptText(), []footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
			{"esc", "cancel"},
		})
	case m.agentPicker != nil:
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
	footerLines := 6
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
	evt, ok := hookEventFromNotification(n)
	if !ok {
		return
	}
	switch evt.Type {
	case status.EventNotificationIdle:
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs input", filepath.Base(evt.ProjectPath)))
	case status.EventNotificationPerm:
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs permission", filepath.Base(evt.ProjectPath)))
	}
	m.statusMgr.ProcessHookEvent(evt)
}

// hookEventFromNotification maps a socket notification to a status hook
// event. V2 messages (status-hook.sh's hook_event_name format — every event
// type, carrying Claude's own payload) go through status.ParseHookPayload;
// the three legacy notification types map directly. ok is false for anything
// that isn't a status event.
func hookEventFromNotification(n notify.Notification) (status.HookEvent, bool) {
	if n.Type == notify.NotifyHookEvent {
		evt, err := status.ParseHookPayload(n.Raw)
		if err != nil || evt.SessionID == "" || evt.SessionID == "unknown" {
			return status.HookEvent{}, false
		}
		return evt, true
	}
	key := n.SessionID
	if key == "" {
		key = n.ProjectPath
	}
	evt := status.HookEvent{SessionID: key, ProjectPath: n.ProjectPath}
	switch n.Type {
	case notify.NotifyIdlePrompt:
		evt.Type = status.EventNotificationIdle
	case notify.NotifyPermissionPrompt:
		evt.Type = status.EventNotificationPerm
	case notify.NotifySessionStop:
		evt.Type = status.EventStop
	default:
		return status.HookEvent{}, false
	}
	return evt, true
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
	case status.StatusQuestion:
		return StatusQuestion
	case status.StatusExternal:
		return StatusExternal
	default:
		return StatusNone
	}
}

// beginRestart restarts the sidebars and quits; Run then execs the
// freshly-installed binary in place. Shared by ctrl+alt+r and the
// NotifyRestart socket message.
func (m Model) beginRestart() (tea.Model, tea.Cmd) {
	m.restartSidebars()
	m.restartRequested = true
	return m, tea.Quit
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

// isErrorStatus reports whether a status message reads as an error, which
// keeps it up until a keypress instead of auto-clearing.
func isErrorStatus(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "err:")
}

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

		// The worktree at what origin has for the PR's branch.
		wtPath, warning, err := ops.WorktreeFromRemote(m.ops, p.Path, pr.Branch)
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
		if warning != "" {
			status = warning + ". " + status
		}
		return worktreeCreatedMsg{status: status}
	}
}

func (m Model) fetchPRs(projectPath string) tea.Cmd {
	return func() tea.Msg {
		prs, err := gh.ListPRs(projectPath)
		return prFetchMsg{prs: prs, err: err}
	}
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
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return ttmux.ExactTarget(session, name)
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
		var wtPath, warning string
		var err error
		if prBranch {
			wtPath, warning, err = ops.WorktreeFromRemote(m.ops, projectPath, branch)
		} else {
			wtPath, err = project.CreateWorktree(projectPath, branch)
		}
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Recreate failed: %v", err))
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
		if warning != "" {
			status = warning + ". " + status
		}
		return worktreeCreatedMsg{status: status}
	}
}

// killCleanupSessions is a thin TUI adapter over ops.StopSessions. The
// callers always follow up with runCleanup for the actual worktree removal.
func (m Model) killCleanupSessions(sessions []claude.Session) tea.Cmd {
	return func() tea.Msg {
		n := ops.StopSessions(m.ops, sessions)
		return statusMsgEvent(fmt.Sprintf("Killed %d session(s)", n))
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
	ti := textinput.New()
	ti.Placeholder = "new branch name"
	ti.Focus()
	ti.CharLimit = 128
	ti.SetWidth(40)
	m.lift = &liftPrompt{
		input:        ti,
		sessionID:    row.session.SessionID,
		sourcePath:   row.path,
		sourcePID:    pid,
		sourceWindow: window,
	}
	return m, textinput.Blink
}

// decideLiftDirty runs after the user submits a branch name. If the source
// is dirty, we open the [s]/[l]/[n] menu; otherwise we run the lift directly.
func (m Model) decideLiftDirty(newBranch string) (tea.Model, tea.Cmd) {
	p := *m.lift
	p.branch = newBranch
	m.lift = &p
	dirty, err := project.IsDirty(p.sourcePath)
	if err != nil {
		// Surface the error and bail — don't press on with an uncertain state.
		cmd := func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("Lift failed: %v", err))
		}
		m.lift = nil
		return m, cmd
	}
	if dirty {
		p.dirty = true
		m.lift = &p
		return m, nil
	}
	cmd := m.runLiftSession(false)
	m.lift = nil
	return m, cmd
}

// runLiftSession hands control to the ops adapter with the captured source
// info and the user's stash-or-leave decision.
func (m Model) runLiftSession(stashAndPop bool) tea.Cmd {
	l := m.lift
	return m.liftSessionToWorktree(l.sessionID, l.sourcePath, l.sourceWindow, l.sourcePID, l.branch, stashAndPop)
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
		return func() tea.Msg {
			return m.launchAgentInWindow(p.Name, p.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
		}
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

// restartInPlace replaces this process with the (freshly installed) mo
// binary — same PID, same tmux pane, no nesting. The state file is left in
// place for the new process to rewrite, so sidebars don't blank in between.
func restartInPlace(ns *notify.Server) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	// Release the socket now — exec skips deferred calls, and the new
	// process binds the same path.
	if ns != nil {
		ns.Stop()
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
