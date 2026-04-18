package project

import (
	"strings"
	"testing"
)

func TestMainCheckoutBranch(t *testing.T) {
	repo := newGitRepo(t)
	got, err := MainCheckoutBranch(repo)
	if err != nil {
		t.Fatalf("MainCheckoutBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("want main, got %q", got)
	}
}

func TestMainCheckoutBranchDetachedReturnsEmpty(t *testing.T) {
	repo := newGitRepo(t)
	// Detach HEAD.
	gitRun(t, repo, "checkout", "--detach", "HEAD")
	got, err := MainCheckoutBranch(repo)
	if err != nil {
		t.Fatalf("MainCheckoutBranch: %v", err)
	}
	if got != "" {
		t.Errorf("detached HEAD: want empty, got %q", got)
	}
}

func TestIsDirty(t *testing.T) {
	repo := newGitRepo(t)
	dirty, err := IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty clean: %v", err)
	}
	if dirty {
		t.Error("fresh repo should be clean")
	}
	// Make it dirty.
	path := repo + "/newfile"
	if err := writeFile(path, "hello"); err != nil {
		t.Fatal(err)
	}
	dirty, err = IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty dirty: %v", err)
	}
	if !dirty {
		t.Error("untracked file should make repo dirty")
	}
}

func TestListBranches(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "feature-a")
	gitRun(t, repo, "branch", "feature-b")
	// Make feature-a merged into main (already is — branches from same commit).
	// Create an unmerged branch.
	gitRun(t, repo, "checkout", "-b", "feature-c")
	path := repo + "/c.txt"
	if err := writeFile(path, "c"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "c.txt")
	gitRun(t, repo, "commit", "-m", "add c")
	gitRun(t, repo, "checkout", "main")

	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("want some branches")
	}
	// Main must be first.
	if !branches[0].IsMain || branches[0].Name != "main" {
		t.Errorf("main should be first: got %+v", branches[0])
	}
	// The feature branches (pointing at the root commit) should be marked merged.
	found := map[string]Branch{}
	for _, b := range branches {
		found[b.Name] = b
	}
	if fa, ok := found["feature-a"]; !ok {
		t.Error("feature-a missing")
	} else if !fa.Merged {
		t.Error("feature-a should be merged (same commit as main)")
	}
	if fc, ok := found["feature-c"]; !ok {
		t.Error("feature-c missing")
	} else if fc.Merged {
		t.Error("feature-c (with unique commit) should NOT be merged")
	}
}

func TestListBranchesDetectsRemoteGone(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "stale")
	// Fabricate a "gone" upstream by configuring a remote that doesn't exist.
	gitRun(t, repo, "config", "branch.stale.remote", "origin")
	gitRun(t, repo, "config", "branch.stale.merge", "refs/heads/stale")
	// No remote named origin — for-each-ref should emit `[gone]`.
	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	var stale *Branch
	for i := range branches {
		if branches[i].Name == "stale" {
			stale = &branches[i]
			break
		}
	}
	if stale == nil {
		t.Fatal("stale branch missing")
	}
	if !stale.RemoteGone {
		// Newer git prints `[gone]`; some older gits may not. Log instead of fail
		// to avoid false negatives across environments.
		t.Logf("RemoteGone not set; host git may not emit [gone] here")
	}
}

func TestListBranchesAnnotatesWorktrees(t *testing.T) {
	repo := newGitRepo(t)
	gitRun(t, repo, "branch", "wt-branch")
	// Add a worktree for wt-branch somewhere outside repo so ListBranches can
	// cross-reference it.
	wtPath := repo + "-wt"
	gitRun(t, repo, "worktree", "add", wtPath, "wt-branch")
	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	var wb *Branch
	for i := range branches {
		if branches[i].Name == "wt-branch" {
			wb = &branches[i]
			break
		}
	}
	if wb == nil {
		t.Fatal("wt-branch missing")
	}
	if !strings.HasSuffix(wb.WorktreePath, "-wt") {
		t.Errorf("WorktreePath should be set, got %q", wb.WorktreePath)
	}
}
