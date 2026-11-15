package tui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	gh "github.com/rvanmech/unky-mo/internal/github"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"github.com/rvanmech/unky-mo/internal/project"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/tickets"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
	"go.uber.org/mock/gomock"
)

// The golden tests pin what the TUI draws, ANSI and all, so the refactors
// of app.go (docs/plans/review-remediation.md, phase 2) can show they
// change nothing on screen. Regenerate with `go test ./internal/tui -run
// Golden -update` only when a change to the screen is intended.
var updateGolden = flag.Bool("update", false, "rewrite the golden files")

type stubProvider struct{}

func (stubProvider) Name() string { return "jira" }
func (stubProvider) MyTickets(context.Context) ([]tickets.Ticket, error) {
	return nil, nil
}
func (stubProvider) Detail(context.Context, string) (*tickets.TicketDetail, error) {
	return nil, nil
}

// goldenModel is a dashboard with three projects, a session in each
// status, a worktree, tickets and usage, at 120×40.
func goldenModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	ctrl := gomock.NewController(t)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	now := time.Now()

	tc.EXPECT().ListWindows().Return([]ttmux.Window{{ID: "@1", Name: "alpha"}}, nil).AnyTimes()
	tc.EXPECT().WindowPanePIDs(gomock.Any()).Return(map[int]bool{}, nil).AnyTimes()
	cr.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()
	cr.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).Return(false).AnyTimes()
	cr.EXPECT().ProjectsDirForPath(gomock.Any()).Return("/nonexistent").AnyTimes()
	cr.EXPECT().RecentSessions("/ws/alpha", 5).Return([]claude.RecentSession{
		{SessionID: "11111111-aaaa", Title: "fix-the-login-flow", LastActive: now.Add(-90 * time.Minute), IsLive: true},
		{SessionID: "22222222-bbbb", LastActive: now.Add(-50 * time.Hour)},
	}).AnyTimes()
	cr.EXPECT().RecentSessions("/ws/alpha.worktrees/feat", 5).Return([]claude.RecentSession{
		{SessionID: "33333333-cccc", Title: "worktree-session", LastActive: now.Add(-10*time.Minute - 30*time.Second)},
	}).AnyTimes()

	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha", Language: "go"},
		{Name: "beta", Path: "/ws/beta", Language: "ruby"},
		{Name: "gamma", Path: "/ws/gamma"},
	}
	agents := []config.AgentConfig{
		{Key: "c", Name: "Claude", Default: true},
		{Key: "g", Name: "Gemini"},
	}
	m := NewModelWithDeps(projects, tc, cr, nil, nil, nil, filepath.Join(t.TempDir(), "state.json"),
		config.TicketsConfig{}, agents, nil, nil)
	m.width, m.height, m.ready = 120, 40, true

	m.sessionViews = []sessionView{
		{SessionID: "s1", CWD: "/ws/alpha", ProjectPath: "/ws/alpha", ProjectName: "alpha", WindowName: "alpha", WindowID: "@1", Status: StatusActive, Section: "projects"},
		{SessionID: "s2", CWD: "/ws/alpha.worktrees/feat", ProjectPath: "/ws/alpha", ProjectName: "alpha@feat", Parent: "alpha", WindowName: "alpha@feat", WindowID: "@2", Status: StatusPermission, Section: "projects", IsWorktree: true},
		{SessionID: "s3", CWD: "/ws/beta", ProjectPath: "/ws/beta", ProjectName: "beta", WindowName: "beta", WindowID: "@3", AgentKey: "g", Status: StatusIdle, Section: "projects"},
		{SessionID: "s4", CWD: "/tmp/scratch", ProjectPath: "/tmp/scratch", ProjectName: "scratch", WindowName: "scratch", WindowID: "@4", Status: StatusQuestion, Section: "external", IsStray: true},
		{SessionID: "s5", CWD: "/ws/other", ProjectPath: "/ws/other", ProjectName: "other", WindowName: "other", WindowID: "@5", Status: StatusIdle, Section: "projects", IsStray: true, Branch: "topic", Dirty: 3},
	}
	m.refreshDashSessions()

	m.ticketsProviders = []tickets.Provider{stubProvider{}}
	m.ticketsLoaded = true
	m.ticketsGroups = []tickets.BucketGroup{
		{Bucket: tickets.BucketInProgress, Tickets: []tickets.Ticket{
			{ID: "OP-175", Title: "fix auth flow", Bucket: tickets.BucketInProgress, RawStatus: "In Progress", InSprint: true},
		}},
		{Bucket: tickets.BucketTodo, Tickets: []tickets.Ticket{
			{ID: "OP-1", Title: "one", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
			{ID: "OP-2", Title: "two", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
			{ID: "OP-3", Title: "three", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
			{ID: "OP-4", Title: "four", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
			{ID: "OP-5", Title: "five", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
			{ID: "OP-6", Title: "six", Bucket: tickets.BucketTodo, RawStatus: "To Do"},
		}},
	}
	m.usageReady = true
	m.usage = usage.Snapshot{
		FiveHour: usage.Window{Utilization: 42, ResetsAt: now.Add(2*time.Hour + 30*time.Minute + 30*time.Second)},
		SevenDay: usage.Window{Utilization: 87, ResetsAt: now.Add(72 * time.Hour)},
		Source:   "api",
	}
	return m
}

// withDetail opens the project detail screen for alpha: main, a
// worktree branch, a merged plain branch, a branch only another machine
// has a session for, and two PRs, the first expanded.
func withDetail(m Model) Model {
	now := time.Now()
	m.screen = ScreenProject
	m.detailProject = &m.projects[0]
	m.detailFocusLeft = true
	m.detailBranches = []project.Branch{
		{Name: "main", IsMain: true, LastCommit: now.Add(-3*time.Hour - 30*time.Minute)},
		{Name: "feat", WorktreePath: "/ws/alpha.worktrees/feat", LastCommit: now.Add(-26 * time.Hour)},
		{Name: "old", Merged: true, RemoteGone: true, LastCommit: now.Add(-24 * 9 * time.Hour)},
		{Name: "elsewhere", LastCommit: now.Add(-5*time.Hour - 30*time.Minute)},
	}
	m.remoteSynced = map[string]moSync.SessionMeta{
		"elsewhere": {Title: "on the laptop", Hostname: "laptop", PushedAt: now.Add(-2*time.Hour - 30*time.Minute)},
		"ghost":     {Title: "never fetched", Hostname: "desk", PushedAt: now.Add(-49 * time.Hour)},
	}
	m.buildDetailRows()
	m.detailPRs = []gh.PullRequest{
		{Number: 12, Title: "Add the thing that does the stuff", Branch: "feat", State: "OPEN"},
		{Number: 9, Title: "Small fix", Branch: "fix", State: "OPEN"},
	}
	m.detailPRExpanded = -1
	return m
}

func newInput(value string) *textinput.Model {
	ti := textinput.New()
	ti.SetValue(value)
	ti.Focus()
	return &ti
}

func TestGoldenScreens(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m Model) Model
	}{
		{"dashboard", func(m Model) Model { return m }},
		{"dashboard_sessions_focus", func(m Model) Model {
			m.dashFocusLeft = false
			m.dashRightFocus = dashRightSessions
			m.dashSessionCursor = 1
			return m
		}},
		{"dashboard_tickets_focus", func(m Model) Model {
			m.dashFocusLeft = false
			m.dashRightFocus = dashRightTickets
			m.ticketsCursor = 2
			return m
		}},
		{"dashboard_sticky", func(m Model) Model {
			m.statusMsg = "web CA not trusted — run: mo web tls trust"
			m.statusSticky = true
			return m
		}},
		{"dashboard_new_menu", func(m Model) Model {
			m.newSession = &newSessionPrompt{primaryWin: "alpha"}
			return m
		}},
		{"dashboard_new_menu_resume", func(m Model) Model {
			m.newSession = &newSessionPrompt{primaryWin: "alpha", resumeSession: "22222222-bbbb"}
			return m
		}},
		{"dashboard_suspend_quit", func(m Model) Model {
			m.activeSessions = 2
			m.suspendQuit = &suspendQuitPrompt{}
			return m
		}},
		{"dashboard_import", func(m Model) Model {
			m.importExternal = &importPrompt{sessionID: "s4", project: "scratch"}
			return m
		}},
		{"dashboard_agent_picker", func(m Model) Model {
			m.agentPicker = &agentPickerPrompt{}
			return m
		}},
		{"dashboard_new_project", func(m Model) Model {
			m.newProject = &newProjectPrompt{input: *newInput("delta"), parentDir: "/ws"}
			return m
		}},
		{"detail", withDetail},
		{"detail_cursor_session", func(m Model) Model {
			m = withDetail(m)
			m.detailCursor = 1
			return m
		}},
		{"detail_pr_focus_expanded", func(m Model) Model {
			m = withDetail(m)
			m.detailFocusLeft = false
			m.detailPRCursor = 0
			m.detailPRExpanded = 0
			d := &gh.PRDetail{Number: 12, Title: "Add the thing that does the stuff", Branch: "feat", BaseBranch: "main",
				Additions: 120, Deletions: 7, Body: "First line of the description.\n\nSecond paragraph, which is a good deal longer than the panel is wide so it gets cut."}
			d.Author.Login = "someone"
			m.detailPRDetail = d
			return m
		}},
		{"detail_pr_error", func(m Model) Model {
			m = withDetail(m)
			m.detailPRs = nil
			m.detailPRErr = "gh: not logged in"
			return m
		}},
		{"detail_status", func(m Model) Model {
			m = withDetail(m)
			m.statusMsg = "Worktree created"
			return m
		}},
		{"detail_worktree_input", func(m Model) Model {
			m = withDetail(m)
			m.worktree = &worktreePrompt{input: *newInput("feat/new")}
			return m
		}},
		{"detail_lift_input", func(m Model) Model {
			m = withDetail(m)
			m.lift = &liftPrompt{input: *newInput("lifted")}
			return m
		}},
		{"detail_lift_dirty", func(m Model) Model {
			m = withDetail(m)
			m.lift = &liftPrompt{dirty: true, branch: "lifted"}
			return m
		}},
		{"detail_wt_exists", func(m Model) Model {
			m = withDetail(m)
			m.worktreeExists = &worktreeExistsPrompt{branch: "feat"}
			return m
		}},
		{"detail_cleanup_kill", func(m Model) Model {
			m = withDetail(m)
			m.cleanup = &cleanupMenu{stage: "kill", branch: "feat", worktreePath: "/ws/alpha.worktrees/feat", sessions: []claude.Session{{PID: 1}, {PID: 2}}}
			return m
		}},
		{"detail_cleanup_worktree", func(m Model) Model {
			m = withDetail(m)
			m.cleanup = &cleanupMenu{stage: "action", branch: "feat", worktreePath: "/ws/alpha.worktrees/feat"}
			return m
		}},
		{"detail_cleanup_branch", func(m Model) Model {
			m = withDetail(m)
			m.cleanup = &cleanupMenu{stage: "action", branch: "old"}
			return m
		}},
		{"detail_new_menu", func(m Model) Model {
			m = withDetail(m)
			m.newSession = &newSessionPrompt{primaryWin: "alpha@feat"}
			return m
		}},
		{"detail_agent_picker", func(m Model) Model {
			m = withDetail(m)
			m.agentPicker = &agentPickerPrompt{}
			return m
		}},
		{"detail_recap", func(m Model) Model {
			m = withDetail(m)
			m.detailCursor = 1
			m.detailRecap = []claude.SessionMessage{
				{Role: "user", Content: "please fix the login flow"},
				{Role: "assistant", Content: "Done: the session cookie now survives a restart."},
			}
			return m
		}},
		{"help", func(m Model) Model {
			m.screen = ScreenHelp
			return m
		}},
		{"loading", func(m Model) Model {
			m.ready = false
			return m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(goldenModel(t))
			checkGolden(t, tc.name, m.View().Content)
		})
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want\n%s\n--- got\n%s", name, path, plain(string(want)), plain(got))
	}
}

// plain strips SGR escapes so a failure is readable.
func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
