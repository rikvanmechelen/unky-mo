package tui

import (
	"os"
	"path/filepath"
	"os/exec"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rvanmech/unky-mo/internal/claude"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"github.com/rvanmech/unky-mo/internal/project"
	"go.uber.org/mock/gomock"
)

// newTestListModel builds the minimum bubbles/list model the Model needs so
// updateProjectStatuses can call SetItems without panicking. Items are empty
// by default — tests that care about list contents seed their own.
func newTestListModel() list.Model {
	l := list.New(nil, projectDelegate{}, 80, 24)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	return l
}

// newTestInput builds a textinput.Model pre-filled with value so the
// esc-cancels and enter-submits paths can be exercised.
func newTestInput(value string) textinput.Model {
	ti := textinput.New()
	ti.SetValue(value)
	return ti
}

// initGitRepo makes a fresh repo in a temp dir with one empty commit. Mirrors
// the pattern used in internal/project — kept inline here so the TUI test
// doesn't depend on unexported helpers.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	initCmd := exec.Command("git", "init", "-b", "main", dir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	run("config", "user.email", "t@e")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	run("commit", "--allow-empty", "-m", "root")
	return dir
}

// startLiftSessionPrompt on a live-session row should capture the session
// ID + recover the PID via LiveSessions() and record the window name.
func TestStartLiftSessionPromptLive(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	// LiveSessions returns the live record so the helper can recover PID.
	cr.EXPECT().LiveSessions().Return([]claude.Session{
		{PID: 4711, SessionID: "live-sess"},
	}, nil)

	m := Model{claude: cr, tmux: tc}
	row := &detailRow{
		kind:       "br-session",
		session:    &claude.RecentSession{SessionID: "live-sess", IsLive: true},
		branch:     &project.Branch{Name: "main", IsMain: true},
		path:       "/ws/alpha",
		tmuxWindow: "alpha",
	}

	newModel, _ := m.startLiftSessionPrompt(row)
	mm := newModel.(Model)
	if mm.liftSessionInput == nil {
		t.Fatal("expected text input to be initialized")
	}
	if mm.liftSessionSessionID != "live-sess" {
		t.Errorf("SessionID: got %q", mm.liftSessionSessionID)
	}
	if mm.liftSessionSourcePID != 4711 {
		t.Errorf("SourcePID: got %d, want 4711", mm.liftSessionSourcePID)
	}
	if mm.liftSessionSourceWindow != "alpha" {
		t.Errorf("SourceWindow: got %q", mm.liftSessionSourceWindow)
	}
	if mm.liftSessionSourcePath != "/ws/alpha" {
		t.Errorf("SourcePath: got %q", mm.liftSessionSourcePath)
	}
}

// startLiftSessionPrompt on a historical-session row should leave PID=0 and
// window="" (no SIGTERM / kill-window work to do later).
func TestStartLiftSessionPromptHistorical(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	// No LiveSessions() expectation — IsLive=false skips that lookup.

	m := Model{claude: cr, tmux: tc}
	row := &detailRow{
		kind:    "br-session",
		session: &claude.RecentSession{SessionID: "hist-sess", IsLive: false},
		branch:  &project.Branch{Name: "main", IsMain: true},
		path:    "/ws/alpha",
	}

	newModel, _ := m.startLiftSessionPrompt(row)
	mm := newModel.(Model)
	if mm.liftSessionInput == nil {
		t.Fatal("expected text input to be initialized")
	}
	if mm.liftSessionSourcePID != 0 {
		t.Errorf("historical SourcePID must be 0, got %d", mm.liftSessionSourcePID)
	}
	if mm.liftSessionSourceWindow != "" {
		t.Errorf("historical SourceWindow must be empty, got %q", mm.liftSessionSourceWindow)
	}
}

// decideLiftDirty on a dirty source should open the dirty menu, not execute.
func TestDecideLiftDirtyOpensMenuWhenDirty(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "d.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	m := Model{
		liftSessionSourcePath: repo,
		liftSessionSessionID:  "s",
	}
	newModel, cmd := m.decideLiftDirty("feat-new")
	mm := newModel.(Model)
	if !mm.pendingLiftDirtyActive {
		t.Error("dirty source: dirty menu should be active")
	}
	if mm.pendingLiftBranch != "feat-new" {
		t.Errorf("pendingLiftBranch: got %q", mm.pendingLiftBranch)
	}
	if cmd != nil {
		t.Error("dirty path should not execute lift immediately — wait for user menu choice")
	}
}

// decideLiftDirty on a clean source should skip the dirty menu and emit a
// lift Cmd directly.
func TestDecideLiftDirtySkipsMenuWhenClean(t *testing.T) {
	repo := initGitRepo(t)
	m := Model{
		detailProject:         &project.Project{Name: "alpha", Path: repo},
		liftSessionSourcePath: repo,
		liftSessionSessionID:  "s",
	}
	newModel, cmd := m.decideLiftDirty("feat-clean")
	mm := newModel.(Model)
	if mm.pendingLiftDirtyActive {
		t.Error("clean source: dirty menu must not activate")
	}
	if cmd == nil {
		t.Error("clean source should produce a lift Cmd right away")
	}
}

// clearLiftSessionState zeros every lift-flow field so a fresh invocation
// starts from a known state.
func TestClearLiftSessionState(t *testing.T) {
	m := Model{
		liftSessionSessionID:    "x",
		liftSessionSourcePath:   "/y",
		liftSessionSourcePID:    42,
		liftSessionSourceWindow: "w",
		pendingLiftDirtyActive:  true,
		pendingLiftBranch:       "b",
	}
	m.clearLiftSessionState()
	if m.liftSessionSessionID != "" || m.liftSessionSourcePath != "" ||
		m.liftSessionSourcePID != 0 || m.liftSessionSourceWindow != "" ||
		m.pendingLiftDirtyActive || m.pendingLiftBranch != "" {
		t.Errorf("clearLiftSessionState left dirty state: %+v", m)
	}
}

// Regression: when a sessionRefreshMsg arrives on ScreenProject, the detail
// rows must be rebuilt so the new data (e.g. a freshly-lifted session showing
// up at its new worktree path) lands without waiting for the user to leave +
// re-enter the screen.
func TestSessionRefreshRebuildsDetailRowsOnScreenProject(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	// Seed a single branch so buildDetailRows has something to scan.
	branches := []project.Branch{{Name: "main", IsMain: true}}

	// First buildDetailRows call (the one we're triggering): returns no live
	// sessions, no live windows, no recent sessions. We don't care about the
	// content — we care that buildDetailRows was invoked at all.
	tc.EXPECT().ListWindows().Return(nil, nil).AnyTimes()
	cr.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()
	cr.EXPECT().RecentSessions(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m := Model{
		screen:         ScreenProject,
		list:           newTestListModel(),
		detailProject:  proj,
		detailBranches: branches,
		claude:         cr,
		tmux:           tc,
		// Start with a stale row list so we can observe the rebuild replacing it.
		detailRows: []detailRow{
			{kind: "br-empty", branch: &branches[0]},
			{kind: "br-empty", branch: &branches[0]},
			{kind: "br-empty", branch: &branches[0]},
			{kind: "br-empty", branch: &branches[0]},
		},
	}
	got, _ := m.Update(sessionRefreshMsg{})
	mm := got.(Model)
	// After rebuild from our seeded single-branch list, buildDetailRows produces
	// exactly 2 rows (one "branch" header, one "br-empty"), not 4.
	if len(mm.detailRows) != 2 {
		t.Errorf("detail rows after refresh: got %d, want 2 (rebuilt from 1 branch)", len(mm.detailRows))
	}
}

// sessionLiftedMsg is the signal from ops.LiftSessionToWorktree back to the
// TUI. Its handler must: rebuild branch/worktree lists + detail rows, set
// status, trigger an immediate session refresh, and schedule a delayed
// re-refresh to catch the JSONL-write lag (claude writes its new per-cwd
// JSONL a short moment after the PID file lands).
func TestSessionLiftedMsgRefreshesAndRebuildsRows(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	repo := initGitRepo(t)
	proj := &project.Project{Name: "alpha", Path: repo}

	tc.EXPECT().ListWindows().Return(nil, nil).AnyTimes()
	cr.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()
	cr.EXPECT().RecentSessions(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	tc.EXPECT().PanePIDs().Return(map[int]bool{}, nil).AnyTimes()

	m := Model{
		screen:        ScreenProject,
		list:          newTestListModel(),
		detailProject: proj,
		claude:        cr,
		tmux:          tc,
		// Stale rows pre-lift.
		detailRows: []detailRow{{kind: "br-empty"}, {kind: "br-empty"}, {kind: "br-empty"}},
	}
	got, cmd := m.Update(sessionLiftedMsg{status: "Lifted session into alpha@feat"})
	mm := got.(Model)
	// Rows rebuilt: repo has only main branch, so buildDetailRows produces
	// exactly 2 rows (branch header + br-empty).
	if len(mm.detailRows) != 2 {
		t.Errorf("rows after lift: got %d, want 2", len(mm.detailRows))
	}
	if cmd == nil {
		t.Fatal("lifted msg must return a Cmd (refresh + delayed refresh + status)")
	}
}

// currentDetailRow bounds-check.
func TestCurrentDetailRowOutOfRangeReturnsNil(t *testing.T) {
	m := Model{detailCursor: 5, detailRows: []detailRow{{kind: "branch"}}}
	if m.currentDetailRow() != nil {
		t.Error("out-of-range cursor should return nil")
	}
	m.detailCursor = -1
	if m.currentDetailRow() != nil {
		t.Error("negative cursor should return nil")
	}
}

// In-bounds cursor returns the pointer to the underlying row.
func TestCurrentDetailRowInBoundsReturnsRow(t *testing.T) {
	rows := []detailRow{{kind: "branch"}, {kind: "br-session"}}
	m := Model{detailCursor: 1, detailRows: rows}
	got := m.currentDetailRow()
	if got == nil {
		t.Fatal("expected non-nil row")
	}
	if got.kind != "br-session" {
		t.Errorf("wrong row returned: %+v", got)
	}
}

// Pressing `w` on a br-session row in ScreenProject opens the lift-session
// input prompt rather than creating a worktree for the existing branch.
func TestWKeyOnSessionRowOpensLiftPrompt(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	// startLiftSessionPrompt calls LiveSessions() to recover the PID for a
	// live row. Return one so the captured PID makes sense.
	cr.EXPECT().LiveSessions().Return([]claude.Session{
		{PID: 4321, SessionID: "live-x"},
	}, nil).AnyTimes()

	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	branch := project.Branch{Name: "main", IsMain: true}
	m := Model{
		screen:        ScreenProject,
		list:          newTestListModel(),
		detailProject: proj,
		detailFocusLeft: true,
		detailPRExpanded: -1,
		detailRows: []detailRow{
			{kind: "branch", branch: &branch},
			{
				kind:       "br-session",
				branch:     &branch,
				session:    &claude.RecentSession{SessionID: "live-x", IsLive: true},
				path:       "/ws/alpha",
				tmuxWindow: "alpha",
			},
		},
		detailCursor: 1, // on the br-session row
		claude:       cr,
		tmux:         tc,
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	mm := got.(Model)
	if mm.liftSessionInput == nil {
		t.Fatal("w on br-session row should open the lift input prompt")
	}
	if mm.liftSessionSessionID != "live-x" {
		t.Errorf("SessionID not captured: %q", mm.liftSessionSessionID)
	}
	if mm.liftSessionSourcePID != 4321 {
		t.Errorf("SourcePID not captured: %d", mm.liftSessionSourcePID)
	}
}

// Dirty-menu key routing: `l` → run lift with stashAndPop=false and clear state.
func TestDirtyMenuLeaveKeyClearsState(t *testing.T) {
	m := Model{
		pendingLiftDirtyActive: true,
		pendingLiftBranch:      "feat",
		liftSessionSessionID:   "s",
		liftSessionSourcePath:  "/ws/alpha",
		// No detailProject → the cmd runs but the adapter returns a status event;
		// we're validating state transitions, not ops behavior.
	}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	mm := got.(Model)
	if mm.pendingLiftDirtyActive {
		t.Error("dirty menu should clear after `l`")
	}
	if mm.pendingLiftBranch != "" {
		t.Error("pendingLiftBranch should clear after `l`")
	}
	if cmd == nil {
		t.Error("l should return a Cmd (the lift op adapter)")
	}
}

// Dirty-menu key routing: `s` is the default (also bound to enter) and runs
// with stashAndPop=true.
func TestDirtyMenuStashKeyClearsState(t *testing.T) {
	m := Model{
		pendingLiftDirtyActive: true,
		pendingLiftBranch:      "feat",
		liftSessionSessionID:   "s",
		liftSessionSourcePath:  "/ws/alpha",
	}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	mm := got.(Model)
	if mm.pendingLiftDirtyActive {
		t.Error("dirty menu should clear after `s`")
	}
	if cmd == nil {
		t.Error("s should return a Cmd (the lift op adapter)")
	}
}

// Dirty-menu cancel: `n` / `esc` clears state and returns no Cmd.
func TestDirtyMenuCancelClearsState(t *testing.T) {
	m := Model{
		pendingLiftDirtyActive: true,
		pendingLiftBranch:      "feat",
		liftSessionSessionID:   "s",
		liftSessionSourcePath:  "/ws/alpha",
	}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	mm := got.(Model)
	if mm.pendingLiftDirtyActive {
		t.Error("dirty menu should clear after `n`")
	}
	if cmd != nil {
		t.Error("n should NOT return a Cmd — cancel is a no-op")
	}
}

// Lift input prompt `esc` cancels and clears all lift state.
func TestLiftInputPromptEscCancels(t *testing.T) {
	// Build a proper textinput so the input-active branch fires.
	ti := newTestInput("some-branch")
	m := Model{
		screen:                 ScreenProject,
		liftSessionInput:       &ti,
		liftSessionSessionID:   "s",
		liftSessionSourcePath:  "/ws/alpha",
	}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := got.(Model)
	if mm.liftSessionInput != nil {
		t.Error("esc should clear liftSessionInput")
	}
	if mm.liftSessionSessionID != "" {
		t.Error("esc should clear session state")
	}
	if cmd != nil {
		t.Error("esc should not emit a Cmd")
	}
}
