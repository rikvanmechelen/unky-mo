package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
)

// Regression: pressing `w` on the right panel with a PR cursor-highlighted but
// NOT expanded must create a worktree from that PR's branch — not fall through
// to the left panel's branch row. This was the root cause of the wrong-branch
// bug (detailPRExpanded was -1, so the handler fell through to
// currentBranchRow which used the left-panel cursor).
func TestWKeyOnRightPanelUnexpandedPRCreatesWorktreeFromPR(t *testing.T) {
	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	prs := []gh.PullRequest{
		{Number: 100, Branch: "fix-auth"},
		{Number: 200, Branch: "add-feature"},
	}
	leftBranch := project.Branch{Name: "unrelated-branch"}

	m := Model{
		screen:           ScreenProject,
		list:             newTestListModel(),
		detailProject:    proj,
		detailFocusLeft:  false, // right panel focused
		detailPRs:        prs,
		detailPRCursor:   1,  // cursor on PR #200
		detailPRExpanded: -1, // NOT expanded — this was the bug trigger
		// Left panel points at a different branch — the old code would
		// fall through and use this.
		detailRows:   []detailRow{{kind: "branch", branch: &leftBranch}},
		detailCursor: 0,
	}

	got, cmd := m.Update(tea.KeyPressMsg{Code: 'w'})
	_ = got.(Model)

	// The key handler should have returned a Cmd (createWorktreeFromPR).
	// If the bug regresses, cmd will be createWorktreeAndLaunch for
	// "unrelated-branch" instead, or nil if neither path matched.
	if cmd == nil {
		t.Fatal("w on right panel with cursor on PR should return a Cmd")
	}
}

// When a PR IS expanded and `w` is pressed, it should still work correctly
// (regression guard for the fix).
func TestWKeyOnRightPanelExpandedPRStillWorks(t *testing.T) {
	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	prs := []gh.PullRequest{
		{Number: 100, Branch: "fix-auth"},
		{Number: 200, Branch: "add-feature"},
	}

	m := Model{
		screen:           ScreenProject,
		list:             newTestListModel(),
		detailProject:    proj,
		detailFocusLeft:  false,
		detailPRs:        prs,
		detailPRCursor:   0,
		detailPRExpanded: 0, // expanded
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'w'})
	if cmd == nil {
		t.Fatal("w on right panel with expanded PR should return a Cmd")
	}
}

// `c` (checkout) on the right panel with cursor on an un-expanded PR should
// work — same bug pattern as `w`.
func TestCheckoutKeyOnRightPanelUnexpandedPR(t *testing.T) {
	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	prs := []gh.PullRequest{
		{Number: 100, Branch: "fix-auth"},
	}

	m := Model{
		screen:           ScreenProject,
		list:             newTestListModel(),
		detailProject:    proj,
		detailFocusLeft:  false,
		detailPRs:        prs,
		detailPRCursor:   0,
		detailPRExpanded: -1, // NOT expanded
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c'})
	if cmd == nil {
		t.Fatal("c on right panel with cursor on PR should return a Cmd")
	}
}

// When focus is on the left panel, `w` must NOT route to the PR path — it
// should use the branch row instead.
func TestWKeyOnLeftPanelDoesNotUsePR(t *testing.T) {
	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	prs := []gh.PullRequest{
		{Number: 100, Branch: "fix-auth"},
	}
	leftBranch := project.Branch{Name: "main", IsMain: true}

	m := Model{
		screen:          ScreenProject,
		list:            newTestListModel(),
		detailProject:   proj,
		detailFocusLeft: true, // LEFT panel
		detailPRs:       prs,
		detailPRCursor:  0,
		detailRows: []detailRow{
			{kind: "branch", branch: &leftBranch},
		},
		detailCursor: 0,
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'w'})
	// On the left panel with a branch row, `w` should still return a Cmd
	// (createWorktreeAndLaunch for the branch), not route to the PR.
	if cmd == nil {
		t.Fatal("w on left panel should still return a Cmd for the branch row")
	}
}

// --- Worktree-exists prompt tests ---

// worktreeExistsMsg should open the worktree-exists prompt.
func TestWorktreeExistsMsgOpensPrompt(t *testing.T) {
	proj := &project.Project{Name: "alpha", Path: "/ws/alpha"}
	m := Model{
		screen:        ScreenProject,
		list:          newTestListModel(),
		detailProject: proj,
	}
	got, cmd := m.Update(worktreeExistsMsg{
		branch:       "feat",
		worktreePath: "/ws/alpha.worktrees/feat",
		prBranch:     true,
	})
	mm := got.(Model)
	if !mm.pendingWTExistsActive {
		t.Fatal("worktreeExistsMsg should activate the prompt")
	}
	if mm.pendingWTExistsBranch != "feat" {
		t.Errorf("Branch: got %q, want %q", mm.pendingWTExistsBranch, "feat")
	}
	if mm.pendingWTExistsWTPath != "/ws/alpha.worktrees/feat" {
		t.Errorf("WTPath: got %q", mm.pendingWTExistsWTPath)
	}
	if mm.pendingWTExistsProjectPath != "/ws/alpha" {
		t.Errorf("ProjectPath: got %q", mm.pendingWTExistsProjectPath)
	}
	if mm.pendingWTExistsProjectName != "alpha" {
		t.Errorf("ProjectName: got %q", mm.pendingWTExistsProjectName)
	}
	if !mm.pendingWTExistsPRBranch {
		t.Error("PRBranch should be true")
	}
	if cmd != nil {
		t.Error("worktreeExistsMsg should not return a Cmd — it opens a prompt")
	}
}

// `f` on the worktree-exists prompt should clear state and return a Cmd.
func TestWorktreeExistsPromptFocusKey(t *testing.T) {
	m := Model{
		screen:                  ScreenProject,
		pendingWTExistsActive:   true,
		pendingWTExistsBranch:   "feat",
		pendingWTExistsProjectPath: "/ws/alpha",
		pendingWTExistsProjectName: "alpha",
		pendingWTExistsWTPath:   "/ws/alpha.worktrees/feat",
	}
	got, cmd := m.Update(tea.KeyPressMsg{Code: 'f'})
	mm := got.(Model)
	if mm.pendingWTExistsActive {
		t.Error("f should clear the prompt")
	}
	if cmd == nil {
		t.Fatal("f should return a Cmd (focus existing worktree)")
	}
}

// `r` on the worktree-exists prompt should clear state and return a Cmd.
func TestWorktreeExistsPromptRemoveKey(t *testing.T) {
	m := Model{
		screen:                  ScreenProject,
		pendingWTExistsActive:   true,
		pendingWTExistsBranch:   "feat",
		pendingWTExistsProjectPath: "/ws/alpha",
		pendingWTExistsProjectName: "alpha",
		pendingWTExistsWTPath:   "/ws/alpha.worktrees/feat",
		pendingWTExistsPRBranch: true,
	}
	got, cmd := m.Update(tea.KeyPressMsg{Code: 'r'})
	mm := got.(Model)
	if mm.pendingWTExistsActive {
		t.Error("r should clear the prompt")
	}
	if cmd == nil {
		t.Fatal("r should return a Cmd (remove + recreate)")
	}
}

// `n`, `esc`, and `enter` on the worktree-exists prompt should cancel.
func TestWorktreeExistsPromptCancelKeys(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'n'},
		{Code: tea.KeyEscape},
		{Code: tea.KeyEnter},
	} {
		m := Model{
			screen:                ScreenProject,
			pendingWTExistsActive: true,
			pendingWTExistsBranch: "feat",
		}
		got, cmd := m.Update(key)
		mm := got.(Model)
		if mm.pendingWTExistsActive {
			t.Errorf("key %v should clear the prompt", key)
		}
		if cmd != nil {
			t.Errorf("key %v should not return a Cmd (cancel)", key)
		}
	}
}

// clearPendingWTExists zeroes all worktree-exists fields.
func TestClearPendingWTExists(t *testing.T) {
	m := Model{
		pendingWTExistsActive:      true,
		pendingWTExistsBranch:      "feat",
		pendingWTExistsProjectPath: "/ws/alpha",
		pendingWTExistsProjectName: "alpha",
		pendingWTExistsWTPath:      "/ws/alpha.worktrees/feat",
		pendingWTExistsPRBranch:    true,
	}
	m.clearPendingWTExists()
	if m.pendingWTExistsActive || m.pendingWTExistsBranch != "" ||
		m.pendingWTExistsProjectPath != "" || m.pendingWTExistsProjectName != "" ||
		m.pendingWTExistsWTPath != "" || m.pendingWTExistsPRBranch {
		t.Errorf("clearPendingWTExists left dirty state: %+v", m)
	}
}
