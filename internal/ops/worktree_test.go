package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
)

// Worktree ops rely on project.CreateWorktree / RemoveWorktree which shell
// out to real git. The ops-level tests construct a real git repo in a
// tempdir (pattern already established in internal/project) so the test
// exercises the full ops path without mocking git.

func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@e",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	initCmd := exec.Command("git", "init", "-b", "main", dir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	run("commit", "--allow-empty", "-m", "root")
	return dir
}

func TestCleanupWorktreeRemovesThenDeletes(t *testing.T) {
	repo := newGitRepo(t)
	// Create a worktree to remove.
	runGit(t, repo, "branch", "feat")
	wtPath := repo + "-feat"
	runGit(t, repo, "worktree", "add", wtPath, "feat")

	ctx, tmux, _ := newTestContext(t)
	_ = tmux

	res, err := CleanupWorktree(ctx, CleanupParams{
		ProjectPath:  repo,
		Branch:       "feat",
		DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if !res.WorktreeRemoved {
		t.Error("want WorktreeRemoved=true")
	}
	if !res.BranchDeleted {
		t.Error("want BranchDeleted=true")
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, err=%v", err)
	}
}

func TestCleanupWorktreeIgnoresMissingWorktree(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "branch", "feat")

	ctx, _, _ := newTestContext(t)
	res, err := CleanupWorktree(ctx, CleanupParams{
		ProjectPath:  repo,
		Branch:       "feat",
		DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if res.WorktreeRemoved {
		t.Error("no worktree existed → WorktreeRemoved should be false")
	}
	if !res.BranchDeleted {
		t.Error("branch should still be deletable")
	}
}

func TestCleanupWorktreeRefusesEmptyBranch(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	_, err := CleanupWorktree(ctx, CleanupParams{ProjectPath: "/tmp", Branch: ""})
	if err == nil {
		t.Error("empty Branch should error")
	}
}

// runGit is a tiny wrapper so tests don't need exec/bytes scaffolding.
func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@e",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCreateWorktreeAndLaunchHappyPath(t *testing.T) {
	repo := newGitRepo(t)

	ctx, tmux, claude := newTestContext(t)
	// primaryWindowForTarget: no live sessions at the worktree path.
	claude.EXPECT().SessionsForPath(gomock.Any()).Return(nil).AnyTimes()
	// No existing window for alpha@feat.
	tmux.EXPECT().WindowExists("alpha@feat").Return(false)
	// LaunchSession ceremony.
	tmux.EXPECT().CreateWindow("alpha@feat", gomock.Any()).Return("mo:alpha@feat", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	res, err := CreateWorktreeAndLaunch(ctx, WorktreeParams{
		ProjectName: "alpha",
		ProjectPath: repo,
		Branch:      "feat",
	})
	if err != nil {
		t.Fatalf("CreateWorktreeAndLaunch: %v", err)
	}
	if !res.Launched {
		t.Error("Launched should be true when no existing session/window")
	}
	if !strings.HasSuffix(res.WorktreePath, "alpha.worktrees/feat") && !strings.Contains(res.WorktreePath, "feat") {
		t.Errorf("unexpected worktree path: %q", res.WorktreePath)
	}
	if res.WindowName != "alpha@feat" {
		t.Errorf("window: %q", res.WindowName)
	}
}

func TestOpenBranchInMainRefusesDirty(t *testing.T) {
	repo := newGitRepo(t)
	// Make dirty.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, _, _ := newTestContext(t)
	res, err := OpenBranchInMain(ctx, OpenBranchParams{
		ProjectName: "alpha",
		ProjectPath: repo,
		Branch:      "feat",
		Stash:       false,
	})
	if err != nil {
		t.Fatalf("OpenBranchInMain: %v", err)
	}
	if !strings.Contains(res.Status, "dirty") {
		t.Errorf("want dirty-refusal status, got %q", res.Status)
	}
	if res.Launched {
		t.Error("should NOT launch on dirty repo without Stash")
	}
}

