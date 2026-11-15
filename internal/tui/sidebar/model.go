package sidebar

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
)

type SidebarItem struct {
	Name       string
	Path       string // project directory path
	WindowName string // tmux window target; empty for Home (window 0)
	WindowID   string // stable tmux window id (e.g. "@5"); empty when unresolved
	InstanceID string // mo-generated instance ID; empty for pre-refactor rows
	AgentKey   string // coding agent mnemonic (from @mo_agent); empty = default
	Status     string // "none", "active", "idle", "permission", "external"
	Parent     string // non-empty for worktree entries (parent project name)
	Section    string // "projects" (default) or "external" — groups stray sessions
	Branch     string // git branch (populated for git-backed strays)
	Dirty      int    // dirty file count (populated for git-backed strays)
	IsHome     bool
	IsHeader   bool // non-interactive section header (e.g., "── Terminals ──")
	// Terminal items
	IsTerminal bool
	PaneID     string
	IsActive   bool // terminal is currently visible in drawer
	// Team agent items
	IsTeamLead   bool   // true when this session leads an agent team
	IsTeammate   bool   // true for teammate sub-items
	TeammateName string // role name for display (e.g. "architect")
	TeamPaneID   string // tmux pane ID for focus switching via SelectPane
}

// TerminalTab tracks a terminal pane managed by the drawer.
type TerminalTab struct {
	PaneID string // stable tmux pane ID (e.g., "%5")
	Name   string // display name ("term-1", "term-2")
}

type sidebarStatusMsg string
type stateTickMsg time.Time

func stateTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return stateTickMsg(t)
	})
}

type Model struct {
	items         []SidebarItem
	cursor        int
	viewportStart int // for scrolling
	tmux          TmuxClient
	claude        ClaudeReader
	resolver      WindowResolver
	git           GitReader // nil: the real checkout (gitReader)
	opener        Opener    // nil: VS Code or the system default
	stateFile     string
	statusMsg     string
	cursorSetOnce bool // true after initial cursor placement
	// The project this sidebar belongs to (detected from tmux window name)
	windowName string
	windowID   string // stable tmux window id (e.g. "@5"); survives renames
	windowPath string
	instanceID string // mo-generated instance ID (from --instance-id flag); the primary key for binding sidebar+terminals
	agentKey   string // coding agent mnemonic for the own window; empty = Claude (default)
	width      int
	height     int
	// Terminal drawer state
	terminals     []TerminalTab
	activeTermIdx int // index into terminals; -1 when drawer closed
	drawerOpen    bool
	// publishedDrawerPane is the last value written to the window's
	// @mo_drawer_pane option; drawerAdopted is set once the startup
	// adoption of a still-open drawer pane has run.
	publishedDrawerPane string
	drawerAdopted       bool

	// restartRequested is set by ctrl+alt+r; RunWithOpts re-execs the
	// binary in place once bubbletea has restored the terminal.
	restartRequested bool
	termCounter      int // incrementing counter for naming
	// Sync status: "synced", "stale", or "" (not synced / no sync repo)
	syncStatus string
	// Active Claude shells (Bash tool subprocesses)
	activeShells []claude.ActiveShell
	// Changed files (from git status)
	changedFiles   []string          // raw file paths from git status --porcelain
	changedAdded   int               // total lines added
	changedRemoved int               // total lines removed
	gitStatusMap   map[string]string // path → status label (M/A/D/?/R) from git status
	// Full project file tree
	fileTreeMode       fileTreeMode // changed-only vs full tree
	dirTree            *dirNode     // cached full directory tree
	dirTreeRaw         string       // cached git ls-files output for change detection
	dirTreeRefreshTick int          // counter for 5s cadence (refresh every 5 ticks)
	// Focus section: "sessions", "shells", or "files"
	focusSection  string
	shellCursor   int
	fileCursor    int
	fileViewStart int // scroll offset for file tree viewport
	// Claude usage snapshot (populated from state file; nil until main TUI
	// has fetched at least once).
	usage *state.UsageState
	// Cumulative tokens for the live Claude session in this window; 0 when
	// no session is currently running here.
	sessionTokens int
}

func NewModel(sessionName, stateFile string) Model {
	tc := ttmux.NewClient(sessionName)
	tmuxAdapter := newTmuxClientAdapter(tc)
	tmuxAdapter.ConfigureStatusFormat()
	return NewModelWithDeps(sessionName, stateFile, tmuxAdapter, defaultClaudeReader{}, NewDefaultWindowResolver())
}

// NewModelWithDeps is the test-friendly constructor. Callers supply the
// tmux + claude seams plus a window resolver. Production callers use
// NewModel, which wires up the real implementations.
func NewModelWithDeps(sessionName, stateFile string, tmux TmuxClient, claude ClaudeReader, resolver WindowResolver) Model {
	if resolver == nil {
		resolver = NewDefaultWindowResolver()
	}
	windowName, windowID := resolver.ResolveOwnWindow()

	// The sidebar process's cwd is its pane's cwd at startup; it's a Go
	// program with no shell that could cd elsewhere, so this is always the
	// right path for the window we're in — including worktrees.
	windowPath, _ := os.Getwd()

	m := Model{
		tmux:          tmux,
		claude:        claude,
		resolver:      resolver,
		stateFile:     stateFile,
		windowName:    windowName,
		windowID:      windowID,
		windowPath:    windowPath,
		activeTermIdx: -1,
		focusSection:  "sessions",
		fileTreeMode:  fileTreeModeFull,
	}
	m.items = append(m.items, SidebarItem{
		Name:   "Unky Mo Home",
		IsHome: true,
	})
	m.refreshState()
	m.refreshSyncStatus()
	return m
}

func (m Model) Init() tea.Cmd {
	return stateTick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sidebarStatusMsg:
		m.statusMsg = string(msg)
		// Update sync status after a successful push
		if strings.HasPrefix(string(msg), "synced ") {
			m.syncStatus = "synced"
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return sidebarStatusMsg("")
		})

	case stateTickMsg:
		m.refreshState()
		return m, stateTick()

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m.handleMouseClick(msg.Y)
		}

	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if m.focusSection == "files" {
				if m.fileCursor > 0 {
					m.fileCursor--
				} else if len(m.activeShells) > 0 {
					m.focusSection = "shells"
					m.shellCursor = len(m.activeShells) - 1
				} else {
					m.focusSection = "sessions"
					if len(m.items) > 0 {
						m.cursor = len(m.items) - 1
						m.ensureCursorVisible()
					}
				}
			} else if m.focusSection == "shells" {
				if m.shellCursor > 0 {
					m.shellCursor--
				} else {
					m.focusSection = "sessions"
					if len(m.items) > 0 {
						m.cursor = len(m.items) - 1
						m.ensureCursorVisible()
					}
				}
			} else if len(m.items) > 0 {
				m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				if m.items[m.cursor].IsHeader {
					m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				}
				m.ensureCursorVisible()
			}
		case "down", "j":
			if m.focusSection == "files" {
				max := m.fileLineCount()
				if m.fileCursor < max-1 {
					m.fileCursor++
				} else {
					m.focusSection = "sessions"
					m.cursor = 0
					m.ensureCursorVisible()
				}
			} else if m.focusSection == "shells" {
				if m.shellCursor < len(m.activeShells)-1 {
					m.shellCursor++
				} else if m.fileLineCount() > 0 {
					m.focusSection = "files"
					m.fileCursor = 0
				} else {
					m.focusSection = "sessions"
					m.cursor = 0
					m.ensureCursorVisible()
				}
			} else if len(m.items) > 0 {
				next := (m.cursor + 1) % len(m.items)
				if m.items[next].IsHeader {
					next = (next + 1) % len(m.items)
				}
				// If we wrapped around, enter shells or files section
				if next <= m.cursor {
					if len(m.activeShells) > 0 {
						m.focusSection = "shells"
						m.shellCursor = 0
					} else if m.fileLineCount() > 0 {
						m.focusSection = "files"
						m.fileCursor = 0
					}
				} else {
					m.cursor = next
					m.ensureCursorVisible()
				}
			}
		case "enter", " ":
			if m.focusSection == "shells" && m.shellCursor < len(m.activeShells) {
				return m, m.showShellOutput(m.activeShells[m.shellCursor])
			}
			if m.focusSection == "files" {
				path, isDir, hasStatus := m.resolveFilePath()
				if isDir {
					// Toggle expand/collapse on directory node
					if node := findDirNodeFromRoot(m.dirTree, path); node != nil {
						node.expanded = !node.expanded
					}
					return m, nil
				} else if path != "" {
					if hasStatus {
						return m, m.showDiffPopup(path)
					}
					return m, m.openFileInEditor(path)
				}
			}
			if msg.String() == " " {
				// Space only acts on files section; elsewhere ignore
				return m, nil
			}
			return m, m.handleEnter()
		case "d":
			if m.focusSection == "files" {
				path, isDir, hasStatus := m.resolveFilePath()
				if !isDir && path != "" && hasStatus {
					return m, m.showDiffPopup(path)
				}
			}
		case "v":
			if m.focusSection == "files" {
				path, isDir, _ := m.resolveFilePath()
				if !isDir && path != "" {
					return m, m.openFileInEditor(path)
				}
			}
		case "o":
			if m.focusSection == "files" {
				path, isDir, _ := m.resolveFilePath()
				if !isDir && path != "" {
					return m, m.openFileExternal(path)
				}
			} else {
				return m, m.openProjectExternal()
			}
		case ".":
			if m.focusSection == "files" {
				if m.fileTreeMode == fileTreeModeChanged {
					m.fileTreeMode = fileTreeModeFull
				} else {
					m.fileTreeMode = fileTreeModeChanged
				}
				m.fileCursor = 0
				m.fileViewStart = 0
				return m, nil
			}
		case "pgdown":
			// Jump to next section
			switch m.focusSection {
			case "sessions":
				if len(m.activeShells) > 0 {
					m.focusSection = "shells"
					m.shellCursor = 0
				} else if m.fileLineCount() > 0 {
					m.focusSection = "files"
					m.fileCursor = 0
					m.fileViewStart = 0
				}
			case "shells":
				if m.fileLineCount() > 0 {
					m.focusSection = "files"
					m.fileCursor = 0
					m.fileViewStart = 0
				}
			case "files":
				m.focusSection = "sessions"
				m.cursor = 0
				m.ensureCursorVisible()
			}
		case "pgup":
			// Jump to previous section
			switch m.focusSection {
			case "sessions":
				if m.fileLineCount() > 0 {
					m.focusSection = "files"
					m.fileCursor = 0
					m.fileViewStart = 0
				} else if len(m.activeShells) > 0 {
					m.focusSection = "shells"
					m.shellCursor = 0
				}
			case "shells":
				m.focusSection = "sessions"
				m.cursor = 0
				m.ensureCursorVisible()
			case "files":
				if len(m.activeShells) > 0 {
					m.focusSection = "shells"
					m.shellCursor = 0
				} else {
					m.focusSection = "sessions"
					m.cursor = 0
					m.ensureCursorVisible()
				}
			}
		case "t":
			return m, m.toggleDrawer()
		case "T":
			return m, m.newTerminal()
		case "tab":
			return m, m.cycleTerminal(1)
		case "shift+tab":
			return m, m.cycleTerminal(-1)
		case "x":
			return m, m.closeTerminal()
		case "`":
			return m, m.openPopup()
		case "s":
			return m, m.syncPush()
		case "ctrl+r":
			// Force an immediate state refresh instead of waiting for the 1s tick.
			m.refreshState()
			return m, func() tea.Msg { return sidebarStatusMsg("Refreshed") }
		case "ctrl+alt+r", "ctrl+super+r":
			// Restart the sidebar so it picks up a freshly-installed binary:
			// quit, then RunWithOpts execs it in place with the original
			// arguments. ("super" is the Command key on macOS.) Running it as
			// a child via tea.ExecProcess nested a sidebar per restart and
			// dropped --instance-id, so the new one looked for its terminals
			// in the wrong mo-terms session.
			m.restartRequested = true
			return m, tea.Quit
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m *Model) refreshState() {
	resolver := m.resolver
	if resolver == nil {
		resolver = NewDefaultWindowResolver()
	}
	name, id := resolver.ResolveOwnWindow()
	if name != "" {
		m.windowName = name
	}
	if m.windowID == "" && id != "" {
		// NewModel ran before TMUX_PANE was available (rare); retry.
		m.windowID = id
	}
	sf, err := state.Read(m.stateFile)
	if err != nil {
		// Fallback: try to detect sessions independently
		m.refreshFromSessions()
		m.refreshTerminals()
		return
	}

	// Check staleness (>30s means main TUI might not be running)
	if time.Since(sf.UpdatedAt) > 30*time.Second {
		m.refreshFromSessions()
		m.refreshTerminals()
		return
	}

	// Rebuild items: Home + projects section + optional External section.
	// Group into two passes so the external header only renders when there
	// is at least one stray, and so external rows always sit below projects.
	items := []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}
	var projectItems, externalItems []SidebarItem
	for _, p := range sf.Projects {
		if p.Status == "none" {
			continue
		}
		row := SidebarItem{
			Name:       p.Name,
			Path:       p.Path,
			WindowName: p.WindowName,
			WindowID:   p.WindowID,
			InstanceID: p.InstanceID,
			AgentKey:   p.AgentKey,
			Status:     p.Status,
			Parent:     p.Parent,
			Section:    p.Section,
			Branch:     p.Branch,
			Dirty:      p.Dirty,
			IsTeamLead: len(p.Teammates) > 0,
		}
		if p.Section == "external" {
			externalItems = append(externalItems, row)
		} else {
			projectItems = append(projectItems, row)
			// Emit teammate sub-items immediately after the lead
			for _, tm := range p.Teammates {
				projectItems = append(projectItems, SidebarItem{
					Name:         tm.Name,
					IsTeammate:   true,
					TeammateName: tm.Name,
					TeamPaneID:   tm.PaneID,
					Status:       tm.Status,
					Parent:       p.Name, // triggers indent
					WindowID:     p.WindowID,
					WindowName:   p.WindowName,
					Section:      "projects",
				})
			}
		}
	}
	items = append(items, projectItems...)
	if len(externalItems) > 0 {
		items = append(items, SidebarItem{Name: "External", IsHeader: true, Section: "external"})
		items = append(items, externalItems...)
	}

	m.items = items
	m.usage = sf.Usage
	m.sessionTokens = 0
	// Detect own agent and session.
	m.agentKey = m.ownAgentKey(sf)
	isClaude := m.agentKey == "" || m.agentKey == "c"

	if sid := m.ownSessionID(sf); sid != "" && isClaude {
		jsonl := filepath.Join(m.claude.ProjectsDirForPath(m.windowPath), sid+".jsonl")
		m.sessionTokens = usage.SessionTokens(jsonl)
	} else if !isClaude {
		m.sessionTokens = 0
	}
	m.refreshTerminals()
	m.refreshChangedFiles()
	m.refreshDirTree()
	if isClaude {
		// Look up shells via this window's OWN Claude session PID rather than
		// by path. When two concurrent sessions share a CWD (e.g. main +
		// concurrent in the same project), a path-based lookup returns an
		// arbitrary first match and both sidebars end up showing the same
		// shells. ownWindowSession disambiguates via pane-PID descent.
		if live := m.ownWindowSession(); live != nil {
			m.activeShells = m.claude.ActiveShells(live.PID)
		} else {
			m.activeShells = nil
		}
	} else {
		m.activeShells = nil
	}
	// Sync status is checked on init and after push, not every tick
	// (moSync.List does git pull which is too slow for 1s polling)

	// Set cursor to own project on first load only
	if !m.cursorSetOnce {
		for i, item := range m.items {
			if itemMatchesOwnWindow(item, m.instanceID, m.windowID, m.windowName) {
				m.cursor = i
				break
			}
		}
		m.cursorSetOnce = true
	}

	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
}
