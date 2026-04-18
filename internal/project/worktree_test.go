package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small shim so we don't import os in every _test.go.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0644)
}

func TestListWorktreesMainOnly(t *testing.T) {
	repo := newGitRepo(t)
	wts, err := ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("want 1 worktree (main), got %d: %+v", len(wts), wts)
	}
	if wts[0].Branch != "main" {
		t.Errorf("main worktree branch: want main, got %q", wts[0].Branch)
	}
}

func TestListWorktreesWithAdded(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "feat")
	wtPath := repo + "-feat"
	gitRun(t, repo, "worktree", "add", wtPath, "feat")

	wts, err := ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("want 2 worktrees, got %d: %+v", len(wts), wts)
	}
	var feat *Worktree
	for i := range wts {
		if wts[i].Branch == "feat" {
			feat = &wts[i]
		}
	}
	if feat == nil {
		t.Fatal("feat worktree missing")
	}
	if !strings.HasSuffix(feat.Path, "-feat") {
		t.Errorf("feat.Path = %q", feat.Path)
	}
}

func TestWorktreesDir(t *testing.T) {
	got := WorktreesDir("/workspace/unky-mo")
	want := filepath.Clean("/workspace/unky-mo.worktrees")
	if got != want {
		t.Errorf("WorktreesDir: want %q, got %q", want, got)
	}
}

func TestRemoveWorktreeEmptyBranchIsError(t *testing.T) {
	if err := RemoveWorktree("/tmp/nonexistent", ""); err == nil {
		t.Error("empty branch should error")
	}
}

func TestRemoveWorktreeUnknownBranch(t *testing.T) {
	repo := newGitRepo(t)
	err := RemoveWorktree(repo, "does-not-exist")
	if err == nil {
		t.Error("unknown branch should error")
	}
}

func TestRemoveWorktreeSucceeds(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "feat")
	wtPath := repo + "-feat"
	gitRun(t, repo, "worktree", "add", wtPath, "feat")

	if err := RemoveWorktree(repo, "feat"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, err=%v", err)
	}
}

func TestDeleteBranchEmptyIsError(t *testing.T) {
	if err := DeleteBranch("/tmp", ""); err == nil {
		t.Error("empty branch should error")
	}
}

func TestDeleteBranchRefusesMain(t *testing.T) {
	repo := newGitRepo(t)
	err := DeleteBranch(repo, "main")
	if err == nil {
		t.Error("deleting the current main-checkout branch must be refused")
	}
}

func TestDeleteBranchSucceeds(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "feat")
	if err := DeleteBranch(repo, "feat"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	// Re-list: feat should be gone.
	branches, _ := ListBranches(repo)
	for _, b := range branches {
		if b.Name == "feat" {
			t.Error("feat branch should be deleted")
		}
	}
}

func TestCreateWorktreeNewBranch(t *testing.T) {
	repo := newGitRepo(t)
	wtPath, err := CreateWorktree(repo, "new-feature")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if !strings.HasSuffix(wtPath, "new-feature") {
		t.Errorf("wtPath: %q", wtPath)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree dir missing: %v", err)
	}
}

func TestCreateWorktreeEmptyBranch(t *testing.T) {
	if _, err := CreateWorktree("/tmp", ""); err == nil {
		t.Error("empty branch should error")
	}
}
