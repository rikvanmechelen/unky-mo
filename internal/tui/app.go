package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// sessionHasTranscript reports whether v's session has written a non-empty
// JSONL transcript.
func sessionHasTranscript(v sessionView) bool {
	if v.SessionID == "" || v.CWD == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(claude.ProjectsDirForPath(v.CWD), v.SessionID+".jsonl"))
	return err == nil && info.Size() > 0
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
