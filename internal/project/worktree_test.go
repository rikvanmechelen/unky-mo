package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small shim so we don't import os in every _test.go.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0644)
}

func TestCreateNewBranchWorktreeHappyPath(t *testing.T) {
	repo := newGitRepo(t)
	wtPath, err := CreateNewBranchWorktree(repo, "feat-new")
	if err != nil {
		t.Fatalf("CreateNewBranchWorktree: %v", err)
	}
	if !strings.HasSuffix(wtPath, ".worktrees/feat-new") {
		t.Errorf("unexpected worktree path: %q", wtPath)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree dir should exist, got %v", err)
	}
	// The branch must now exist.
	branches, _ := ListBranches(repo)
	var found bool
	for _, b := range branches {
		if b.Name == "feat-new" {
			found = true
			break
		}
	}
	if !found {
		t.Error("feat-new branch should exist after CreateNewBranchWorktree")
	}
}

func TestCreateNewBranchWorktreeRefusesExistingBranch(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "taken")

	_, err := CreateNewBranchWorktree(repo, "taken")
	if err == nil {
		t.Fatal("expected error when branch already exists")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "already") &&
		!strings.Contains(strings.ToLower(err.Error()), "exists") {
		t.Errorf("error should mention existing branch, got %q", err)
	}
	// No worktree should have been created on disk.
	wts, _ := ListWorktrees(repo)
	for _, wt := range wts {
		if wt.Branch == "taken" {
			t.Error("no worktree for the refused branch should exist")
		}
	}
}

func TestCreateNewBranchWorktreeRefusesEmptyName(t *testing.T) {
	repo := newGitRepo(t)
	_, err := CreateNewBranchWorktree(repo, "")
	if err == nil {
		t.Error("empty branch name should error")
	}
}

// Regression: even when the main checkout's branch exists (it always does),
// CreateNewBranchWorktree must refuse rather than falling back to checkout.
// This is the invariant that matters for the lift flow — a user typing `main`
// as the new branch on a session row should NOT re-checkout main into a
// sibling worktree.
func TestCreateNewBranchWorktreeRefusesMainBranchName(t *testing.T) {
	repo := newGitRepo(t)
	_, err := CreateNewBranchWorktree(repo, "main")
	if err == nil {
		t.Error("should refuse to recreate the main branch")
	}
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

func TestCreateWorktreeReturnsErrWorktreeExists(t *testing.T) {
	repo := newGitRepo(t)
	// Create a worktree the first time — should succeed.
	_, err := CreateWorktree(repo, "feat")
	if err != nil {
		t.Fatalf("first CreateWorktree: %v", err)
	}
	// Second attempt for the same branch should return ErrWorktreeExists.
	_, err = CreateWorktree(repo, "feat")
	if err == nil {
		t.Fatal("expected error on duplicate worktree")
	}
	var existsErr *ErrWorktreeExists
	if !errors.As(err, &existsErr) {
		t.Fatalf("want *ErrWorktreeExists, got %T: %v", err, err)
	}
	if existsErr.Branch != "feat" {
		t.Errorf("Branch: want %q, got %q", "feat", existsErr.Branch)
	}
	if existsErr.WorktreePath == "" {
		t.Error("WorktreePath should be set")
	}
}

func TestFindWorktreeForBranchFound(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "feat")
	wtPath := repo + "-feat"
	gitRun(t, repo, "worktree", "add", wtPath, "feat")

	wt, err := FindWorktreeForBranch(repo, "feat")
	if err != nil {
		t.Fatalf("FindWorktreeForBranch: %v", err)
	}
	if wt == nil {
		t.Fatal("expected non-nil worktree")
	}
	if wt.Branch != "feat" {
		t.Errorf("Branch: want feat, got %q", wt.Branch)
	}
}

func TestFindWorktreeForBranchNotFound(t *testing.T) {
	repo := newGitRepo(t)
	wt, err := FindWorktreeForBranch(repo, "nonexistent")
	if err != nil {
		t.Fatalf("FindWorktreeForBranch: %v", err)
	}
	if wt != nil {
		t.Errorf("expected nil for nonexistent branch, got %+v", wt)
	}
}

func TestFindWorktreeForBranchIgnoresMainCheckout(t *testing.T) {
	repo := newGitRepo(t)
	// The main checkout is on "main" — FindWorktreeForBranch should NOT
	// return it (it only returns sibling worktrees).
	wt, err := FindWorktreeForBranch(repo, "main")
	if err != nil {
		t.Fatalf("FindWorktreeForBranch: %v", err)
	}
	if wt != nil {
		t.Error("should not return the main checkout as a worktree")
	}
}
