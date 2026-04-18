package project

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch represents a local git branch annotated with where (if anywhere)
// it is currently checked out.
type Branch struct {
	Name         string    // short ref, e.g. "main", "feat/x"
	IsMain       bool      // checked out in the main project path
	WorktreePath string    // non-empty iff branch is checked out in a worktree other than main
	LastCommit   time.Time // committer date of branch tip
	Merged       bool      // merged into the repo's main-checkout branch
	RemoteGone   bool      // branch had a configured upstream which is now gone
}

// ListBranches returns all local branches for the project, annotated with
// IsMain / WorktreePath by cross-referencing ListWorktrees. Sorted with the
// main-checkout branch pinned first, then remaining branches by LastCommit
// descending.
func ListBranches(projectPath string) ([]Branch, error) {
	cmd := exec.Command("git", "-C", projectPath, "for-each-ref",
		"--format=%(refname:short)%09%(committerdate:unix)", "refs/heads/")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref: %w", err)
	}

	worktrees, _ := ListWorktrees(projectPath)
	wtByBranch := make(map[string]string, len(worktrees))
	for _, wt := range worktrees {
		if wt.Branch == "" {
			continue
		}
		// Skip the main checkout — IsMain handles it separately below.
		if wt.Path == projectPath {
			continue
		}
		wtByBranch[wt.Branch] = wt.Path
	}

	var branches []Branch
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		name, tsStr, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(tsStr), 10, 64)
		b := Branch{
			Name:       name,
			LastCommit: time.Unix(ts, 0),
		}
		if wtPath, ok := wtByBranch[name]; ok {
			b.WorktreePath = wtPath
		}
		branches = append(branches, b)
	}

	mainBranch, _ := MainCheckoutBranch(projectPath)
	for i := range branches {
		if branches[i].Name == mainBranch {
			branches[i].IsMain = true
		}
	}

	mergedSet := mergedBranchSet(projectPath, mainBranch)
	goneSet := remoteGoneBranchSet(projectPath)
	for i := range branches {
		if branches[i].IsMain {
			continue
		}
		if mergedSet[branches[i].Name] {
			branches[i].Merged = true
		}
		if goneSet[branches[i].Name] {
			branches[i].RemoteGone = true
		}
	}

	sort.SliceStable(branches, func(i, j int) bool {
		if branches[i].IsMain != branches[j].IsMain {
			return branches[i].IsMain
		}
		return branches[i].LastCommit.After(branches[j].LastCommit)
	})

	return branches, nil
}

// mergedBranchSet returns branches merged into mainBranch, per
// `git branch --merged`. Silent on error (e.g. detached HEAD, no main).
func mergedBranchSet(projectPath, mainBranch string) map[string]bool {
	set := map[string]bool{}
	if mainBranch == "" {
		return set
	}
	cmd := exec.Command("git", "-C", projectPath, "branch", "--merged", mainBranch)
	out, err := cmd.Output()
	if err != nil {
		return set
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if name == "" || name == mainBranch {
			continue
		}
		set[name] = true
	}
	return set
}

// remoteGoneBranchSet returns branches whose configured upstream is gone,
// detected by the `[gone]` marker from `for-each-ref`'s upstream:track column.
func remoteGoneBranchSet(projectPath string) map[string]bool {
	set := map[string]bool{}
	cmd := exec.Command("git", "-C", projectPath, "for-each-ref",
		"--format=%(refname:short)%09%(upstream:track)", "refs/heads/")
	out, err := cmd.Output()
	if err != nil {
		return set
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, track, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if strings.Contains(track, "gone") {
			set[name] = true
		}
	}
	return set
}

// MainCheckoutBranch returns the branch currently checked out at projectPath.
// Returns an empty string (no error) if HEAD is detached.
func MainCheckoutBranch(projectPath string) (string, error) {
	cmd := exec.Command("git", "-C", projectPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		// Detached HEAD or other non-symbolic state: not an error for callers.
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// IsDirty returns true when `git status --porcelain` at projectPath reports
// any staged, unstaged, or untracked changes.
func IsDirty(projectPath string) (bool, error) {
	cmd := exec.Command("git", "-C", projectPath, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// CheckoutInMain runs `git checkout <branch>` in the main project repo.
// On failure the returned error contains git's stderr verbatim so callers
// can surface informative messages such as "'branch' is already checked out
// at <path>".
func CheckoutInMain(projectPath, branch string) error {
	cmd := exec.Command("git", "-C", projectPath, "checkout", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// StashMain runs `git stash push -u` in the main project repo. Returns nil
// when there was nothing to stash. The message records the target branch so
// the user can find it later with `git stash list`.
func StashMain(projectPath, branch string) error {
	msg := fmt.Sprintf("unky-mo: before switching to %s", branch)
	cmd := exec.Command("git", "-C", projectPath, "stash", "push", "-u", "-m", msg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
