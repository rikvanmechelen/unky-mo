package tui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/status"
	"github.com/rvanmech/unky-mo/internal/tickets"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sendMouseClick sends a left-press at (x, y) through Update and returns the
// resulting Model. Mirrors the sidebar test helper.
func sendMouseClick(t *testing.T, m Model, x, y int) (Model, tea.Cmd) {
	t.Helper()
	msg := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
	updated, cmd := m.Update(msg)
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return m2, cmd
}

// sendMouseWheel sends a wheel event at (x, y) through Update.
func sendMouseWheel(t *testing.T, m Model, x, y int, button tea.MouseButton) (Model, tea.Cmd) {
	t.Helper()
	msg := tea.MouseWheelMsg{X: x, Y: y, Button: button}
	updated, cmd := m.Update(msg)
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return m2, cmd
}

// newDashModel builds a Model configured for dashboard mouse tests. Width and
// height are set large enough that every item renders. The list is populated
// with the given project items.
func newDashModel(projects []project.Project, sessions []dashSessionItem) Model {
	items := make([]list.Item, len(projects))
	for i, p := range projects {
		items[i] = ProjectItem{project: p}
	}
	l := list.New(items, projectDelegate{}, 120, 50)
	l.Title = "Unky Mo"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.InfiniteScrolling = true
	l.Styles.Title = titleStyle

	return Model{
		screen:            ScreenDashboard,
		list:              l,
		projects:          projects,
		dashFocusLeft:     true,
		dashSessionItems:  sessions,
		dashSessionCursor: 0,
		dashRightFocus:    dashRightSessions,
		statusMgr:         status.NewManager(),
		ticketsExpanded:   map[tickets.Bucket]bool{},
		width:             120,
		height:            50,
		ready:             true,
	}
}

// newProjectModel builds a Model configured for project-detail mouse tests.
func newProjectModel(rows []detailRow, prs []gh.PullRequest) Model {
	p := &project.Project{Name: "test-project", Path: "/ws/test", Language: "go"}
	return Model{
		screen:         ScreenProject,
		detailProject:  p,
		detailRows:     rows,
		detailCursor:   0,
		detailPRs:      prs,
		detailPRCursor: 0,
		detailFocusLeft: true,
		statusMgr:      status.NewManager(),
		ticketsExpanded: map[tickets.Bucket]bool{},
		width:          120,
		height:         50,
		ready:          true,
	}
}

// ---------------------------------------------------------------------------
// Dashboard click tests
// ---------------------------------------------------------------------------

func TestMouseClickDashboardProjectList(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "bravo", Path: "/ws/bravo"},
		{Name: "charlie", Path: "/ws/charlie"},
	}
	m := newDashModel(projects, nil)

	// List header is 4 lines (title + blank + status + blank). Items start
	// at Y=4. Height=1, Spacing=0, so Y=4 → item 0, Y=5 → item 1, etc.
	// X must be within the left panel (< leftWidth).
	m, _ = sendMouseClick(t, m, 5, 5)

	if m.list.Index() != 1 {
		t.Errorf("click on Y=5 should select item 1; got index %d", m.list.Index())
	}
	if !m.dashFocusLeft {
		t.Error("click on left panel should set dashFocusLeft=true")
	}
}

func TestMouseClickDashboardProjectListOutOfRange(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}
	m := newDashModel(projects, nil)
	m.list.Select(0)

	// Y=10 is well past the single item.
	m, cmd := sendMouseClick(t, m, 5, 10)

	if m.list.Index() != 0 {
		t.Errorf("out-of-range click should not move cursor; got index %d", m.list.Index())
	}
	if cmd != nil {
		t.Error("out-of-range click should not return a cmd")
	}
}

func TestMouseClickDashboardSession(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}
	sessions := []dashSessionItem{
		{Name: "alpha", Status: StatusActive},
		{Name: "bravo", Status: StatusIdle},
	}
	m := newDashModel(projects, sessions)
	m.dashFocusLeft = true

	l := m.computeDashboardLayout()

	// Find the Y for the second session (index 1).
	var targetY int
	for y, idx := range l.sessionRowY {
		if idx == 1 {
			targetY = y
			break
		}
	}
	if targetY == 0 {
		t.Fatal("could not find Y for session index 1 in layout")
	}

	m, _ = sendMouseClick(t, m, l.rightX0+5, targetY)

	if m.dashFocusLeft {
		t.Error("click on right panel should clear dashFocusLeft")
	}
	if m.dashRightFocus != dashRightSessions {
		t.Error("click on session should set focus to sessions")
	}
	if m.dashSessionCursor != 1 {
		t.Errorf("click should select session 1; got %d", m.dashSessionCursor)
	}
}

func TestMouseClickDashboardHeaderIsNoop(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}
	m := newDashModel(projects, nil)
	m.list.Select(0)

	// Y=0 is inside the list header (title bar).
	m, cmd := sendMouseClick(t, m, 5, 0)

	if m.list.Index() != 0 {
		t.Errorf("header click should not move cursor; got index %d", m.list.Index())
	}
	if cmd != nil {
		t.Error("header click should not return a cmd")
	}
}

func TestMouseClickDashboardDividerIsNoop(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}
	m := newDashModel(projects, nil)
	l := m.computeDashboardLayout()

	// Click on the divider column.
	m, cmd := sendMouseClick(t, m, l.leftWidth+1, 5)

	if cmd != nil {
		t.Error("divider click should not return a cmd")
	}
}

// ---------------------------------------------------------------------------
// Modal guard tests
// ---------------------------------------------------------------------------

func TestMouseClickDuringModalIsNoop(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "bravo", Path: "/ws/bravo"},
	}
	m := newDashModel(projects, nil)
	m.pendingCleanupActive = true // modal active

	m, cmd := sendMouseClick(t, m, 5, 5)

	if m.list.Index() != 0 {
		t.Errorf("click during modal should not move cursor; got index %d", m.list.Index())
	}
	if cmd != nil {
		t.Error("click during modal should not return a cmd")
	}
}

func TestMouseClickDuringNewMenuIsNoop(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "bravo", Path: "/ws/bravo"},
	}
	m := newDashModel(projects, nil)
	m.pendingNewMenuActive = true

	m, cmd := sendMouseClick(t, m, 5, 5)

	if m.list.Index() != 0 {
		t.Errorf("click during new-session menu should not move cursor; got %d", m.list.Index())
	}
	if cmd != nil {
		t.Error("click during new-session menu should not return a cmd")
	}
}

func TestMouseClickDuringImportPromptIsNoop(t *testing.T) {
	projects := []project.Project{{Name: "alpha", Path: "/ws/alpha"}}
	m := newDashModel(projects, nil)
	m.pendingImportSessionID = "abc"

	m, cmd := sendMouseClick(t, m, 5, 5)
	if cmd != nil {
		t.Error("click during import prompt should be a no-op")
	}
}

// ---------------------------------------------------------------------------
// Non-left button tests
// ---------------------------------------------------------------------------

func TestMouseNonLeftButtonIgnored(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "bravo", Path: "/ws/bravo"},
	}
	m := newDashModel(projects, nil)

	for _, btn := range []tea.MouseButton{tea.MouseRight, tea.MouseMiddle} {
		msg := tea.MouseClickMsg{X: 5, Y: 5, Button: btn}
		updated, cmd := m.Update(msg)
		got, _ := updated.(Model)
		if got.list.Index() != 0 {
			t.Errorf("button %v: cursor should not move; got index %d", btn, got.list.Index())
		}
		if cmd != nil {
			t.Errorf("button %v: should not return a cmd", btn)
		}
	}
}

func TestMouseReleaseIgnored(t *testing.T) {
	projects := []project.Project{{Name: "alpha", Path: "/ws/alpha"}}
	m := newDashModel(projects, nil)

	msg := tea.MouseReleaseMsg{X: 5, Y: 5, Button: tea.MouseLeft}
	updated, cmd := m.Update(msg)
	got, _ := updated.(Model)
	if got.list.Index() != 0 {
		t.Errorf("release: cursor should not move; got index %d", got.list.Index())
	}
	if cmd != nil {
		t.Error("release should not return a cmd")
	}
}

func TestMouseMotionIgnored(t *testing.T) {
	projects := []project.Project{{Name: "alpha", Path: "/ws/alpha"}}
	m := newDashModel(projects, nil)

	msg := tea.MouseMotionMsg{X: 5, Y: 5, Button: tea.MouseLeft}
	updated, cmd := m.Update(msg)
	got, _ := updated.(Model)
	if got.list.Index() != 0 {
		t.Errorf("motion: cursor should not move; got index %d", got.list.Index())
	}
	if cmd != nil {
		t.Error("motion should not return a cmd")
	}
}

// ---------------------------------------------------------------------------
// Project detail click tests
// ---------------------------------------------------------------------------

func TestMouseClickProjectDetailBranch(t *testing.T) {
	rows := []detailRow{
		{kind: "branch", branch: &project.Branch{Name: "main", IsMain: true}},
		{kind: "br-session", session: &claude.RecentSession{Title: "s1"}},
		{kind: "branch", branch: &project.Branch{Name: "feat"}},
	}
	m := newProjectModel(rows, nil)
	m.detailCursor = 0

	l := m.computeProjectLayout()

	// Find Y for the third row (index 2, the "feat" branch).
	var targetY int
	for y, idx := range l.detailRowY {
		if idx == 2 {
			targetY = y
			break
		}
	}
	if targetY == 0 {
		t.Fatal("could not find Y for detailRow index 2")
	}

	m, _ = sendMouseClick(t, m, 5, targetY)

	if !m.detailFocusLeft {
		t.Error("click on left panel should keep detailFocusLeft=true")
	}
	if m.detailCursor != 2 {
		t.Errorf("click should select row 2; got %d", m.detailCursor)
	}
}

func TestMouseClickProjectDetailPR(t *testing.T) {
	rows := []detailRow{
		{kind: "branch", branch: &project.Branch{Name: "main", IsMain: true}},
	}
	prs := []gh.PullRequest{
		{Number: 1, Title: "first PR"},
		{Number: 2, Title: "second PR"},
	}
	m := newProjectModel(rows, prs)
	m.detailFocusLeft = true

	l := m.computeProjectLayout()

	// Find Y for PR index 1.
	var targetY int
	for y, idx := range l.prRowY {
		if idx == 1 {
			targetY = y
			break
		}
	}
	if targetY == 0 {
		t.Fatal("could not find Y for PR index 1")
	}

	m, _ = sendMouseClick(t, m, l.rightX0+5, targetY)

	if m.detailFocusLeft {
		t.Error("click on PR should clear detailFocusLeft")
	}
	if m.detailPRCursor != 1 {
		t.Errorf("click should select PR 1; got %d", m.detailPRCursor)
	}
}

func TestMouseClickProjectDetailHeaderIsNoop(t *testing.T) {
	rows := []detailRow{
		{kind: "branch", branch: &project.Branch{Name: "main", IsMain: true}},
	}
	m := newProjectModel(rows, nil)

	// Y=0 is in the full-width header.
	m, cmd := sendMouseClick(t, m, 5, 0)

	if m.detailCursor != 0 {
		t.Errorf("header click should not move cursor; got %d", m.detailCursor)
	}
	if cmd != nil {
		t.Error("header click should not return a cmd")
	}
}

// ---------------------------------------------------------------------------
// Scroll wheel tests
// ---------------------------------------------------------------------------

func TestMouseWheelDashboardLeftPanel(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "bravo", Path: "/ws/bravo"},
		{Name: "charlie", Path: "/ws/charlie"},
		{Name: "delta", Path: "/ws/delta"},
		{Name: "echo", Path: "/ws/echo"},
	}
	m := newDashModel(projects, nil)
	m.list.Select(0)

	// Scroll down.
	m, _ = sendMouseWheel(t, m, 5, 10, tea.MouseWheelDown)

	// Wheel scrolls 3 items. Starting at 0, should land on 3.
	if m.list.Index() != 3 {
		t.Errorf("wheel down should advance by 3; got index %d", m.list.Index())
	}
	if !m.dashFocusLeft {
		t.Error("wheel on left panel should set dashFocusLeft=true")
	}
}

func TestMouseWheelDashboardRightPanel(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}
	sessions := []dashSessionItem{
		{Name: "s1", Status: StatusActive},
		{Name: "s2", Status: StatusIdle},
		{Name: "s3", Status: StatusActive},
		{Name: "s4", Status: StatusIdle},
	}
	m := newDashModel(projects, sessions)
	m.dashFocusLeft = false
	m.dashRightFocus = dashRightSessions
	m.dashSessionCursor = 0

	l := m.computeDashboardLayout()

	// Scroll down on the right panel.
	m, _ = sendMouseWheel(t, m, l.rightX0+5, 10, tea.MouseWheelDown)

	if m.dashFocusLeft {
		t.Error("wheel on right panel should clear dashFocusLeft")
	}
	if m.dashSessionCursor != 3 {
		t.Errorf("wheel down 3 from 0 with 4 sessions should land on 3; got %d", m.dashSessionCursor)
	}
}

func TestMouseWheelProjectDetailLeft(t *testing.T) {
	rows := []detailRow{
		{kind: "branch", branch: &project.Branch{Name: "main", IsMain: true}},
		{kind: "branch", branch: &project.Branch{Name: "feat-a"}},
		{kind: "branch", branch: &project.Branch{Name: "feat-b"}},
		{kind: "branch", branch: &project.Branch{Name: "feat-c"}},
	}
	m := newProjectModel(rows, nil)
	m.detailCursor = 0

	// Scroll down.
	m, _ = sendMouseWheel(t, m, 5, 10, tea.MouseWheelDown)

	if m.detailCursor != 3 {
		t.Errorf("wheel down 3 from 0 should land on 3; got %d", m.detailCursor)
	}
	if !m.detailFocusLeft {
		t.Error("wheel on left panel should keep focus left")
	}
}

func TestMouseWheelProjectDetailRight(t *testing.T) {
	rows := []detailRow{
		{kind: "branch", branch: &project.Branch{Name: "main", IsMain: true}},
	}
	prs := []gh.PullRequest{
		{Number: 1, Title: "PR 1"},
		{Number: 2, Title: "PR 2"},
		{Number: 3, Title: "PR 3"},
		{Number: 4, Title: "PR 4"},
	}
	m := newProjectModel(rows, prs)

	l := m.computeProjectLayout()

	// Scroll down on the right panel.
	m, _ = sendMouseWheel(t, m, l.rightX0+5, 10, tea.MouseWheelDown)

	if m.detailFocusLeft {
		t.Error("wheel on right panel should clear focus left")
	}
	if m.detailPRCursor != 3 {
		t.Errorf("wheel down 3 from 0 should land on 3; got %d", m.detailPRCursor)
	}
}

// ---------------------------------------------------------------------------
// Help / ticket screen: clicks are no-ops
// ---------------------------------------------------------------------------

func TestMouseClickHelpScreenIsNoop(t *testing.T) {
	m := Model{
		screen:      ScreenHelp,
		statusMgr:   status.NewManager(),
		width:       120,
		height:      50,
		ready:       true,
	}

	m, cmd := sendMouseClick(t, m, 5, 5)
	if cmd != nil {
		t.Error("click on help screen should be a no-op")
	}
}

func TestMouseClickTicketScreenIsNoop(t *testing.T) {
	m := Model{
		screen:      ScreenTicket,
		statusMgr:   status.NewManager(),
		width:       120,
		height:      50,
		ready:       true,
	}

	m, cmd := sendMouseClick(t, m, 5, 5)
	if cmd != nil {
		t.Error("click on ticket screen should be a no-op")
	}
}
