package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrWorktreeExists is returned when a worktree for the requested branch
// already exists.
type ErrWorktreeExists struct {
	Branch       string
	WorktreePath string
}

func (e *ErrWorktreeExists) Error() string {
	return fmt.Sprintf("worktree for branch %q already exists at %s", e.Branch, e.WorktreePath)
}

// Worktree represents a git worktree for a project.
type Worktree struct {
	Path   string
	Branch string
	HEAD   string
	Bare   bool
}

// ListWorktrees returns the git worktrees for the given project path.
func ListWorktrees(projectPath string) ([]Worktree, error) {
	cmd := exec.Command("git", "-C", projectPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var worktrees []Worktree
	var current Worktree

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			branch := strings.TrimPrefix(line, "branch ")
			// Strip refs/heads/ prefix
			branch = strings.TrimPrefix(branch, "refs/heads/")
			current.Branch = branch
		case line == "bare":
			current.Bare = true
		case line == "":
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = Worktree{}
			}
		}
	}

	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}

// FindWorktreeForBranch returns the existing Worktree for the given branch,
// or nil if no worktree is checked out on that branch. Does not count the
// main checkout — only sibling worktrees.
func FindWorktreeForBranch(projectPath, branch string) (*Worktree, error) {
	wts, err := ListWorktrees(projectPath)
	if err != nil {
		return nil, err
	}
	// Resolve projectPath so the comparison handles symlinks (e.g. macOS
	// /var → /private/var vs git's resolved path).
	resolved, _ := filepath.EvalSymlinks(projectPath)
	if resolved == "" {
		resolved = projectPath
	}
	for _, wt := range wts {
		if wt.Branch == branch && wt.Path != projectPath && wt.Path != resolved {
			return &wt, nil
		}
	}
	return nil, nil
}

// WorktreesDir returns the sibling directory that holds worktrees for the
// given project (e.g. /workspace/unky-mo → /workspace/unky-mo.worktrees).
func WorktreesDir(projectPath string) string {
	return filepath.Join(filepath.Dir(projectPath), filepath.Base(projectPath)+".worktrees")
}

// CreateWorktree adds a git worktree for the given branch under the project's
// sibling ".worktrees" directory, creating that directory if needed. If the
// branch does not yet exist it is created from HEAD; if it already exists the
// worktree checks it out instead.
func CreateWorktree(projectPath, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("branch name is required")
	}

	siblingDir := WorktreesDir(projectPath)
	if err := os.MkdirAll(siblingDir, 0755); err != nil {
		return "", fmt.Errorf("creating %s: %w", siblingDir, err)
	}

	wtPath := filepath.Join(siblingDir, branch)

	// First try creating a new branch; fall back to checking out an existing one.
	out, err := exec.Command("git", "-C", projectPath, "worktree", "add", "-b", branch, wtPath).CombinedOutput()
	if err == nil {
		return wtPath, nil
	}
	firstErr := strings.TrimSpace(string(out))

	out2, err2 := exec.Command("git", "-C", projectPath, "worktree", "add", wtPath, branch).CombinedOutput()
	if err2 == nil {
		return wtPath, nil
	}

	// Both attempts failed. Check if a worktree already exists for this
	// branch so callers can offer recovery options.
	if existing, findErr := FindWorktreeForBranch(projectPath, branch); findErr == nil && existing != nil {
		return "", &ErrWorktreeExists{Branch: branch, WorktreePath: existing.Path}
	}

	return "", fmt.Errorf("git worktree add: %s / %s", strings.TrimSpace(string(out2)), firstErr)
}

// CreateNewBranchWorktree is like CreateWorktree but refuses when the branch
// already exists: it only runs `git worktree add -b`, never the existing-branch
// fallback. Used by the lift-session flow, where reusing an existing branch
// would silently diverge from the user's "new branch" intent.
func CreateNewBranchWorktree(projectPath, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("branch name is required")
	}

	// Pre-flight: refuse if the branch already exists.
	check := exec.Command("git", "-C", projectPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err := check.Run(); err == nil {
		return "", fmt.Errorf("branch %q already exists", branch)
	}

	siblingDir := WorktreesDir(projectPath)
	if err := os.MkdirAll(siblingDir, 0755); err != nil {
		return "", fmt.Errorf("creating %s: %w", siblingDir, err)
	}
	wtPath := filepath.Join(siblingDir, branch)
	out, err := exec.Command("git", "-C", projectPath, "worktree", "add", "-b", branch, wtPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	return wtPath, nil
}

// RemoveWorktree removes the worktree for the given branch under projectPath.
// Uses --force so untracked files or local modifications do not block removal;
// callers are expected to confirm with the user first.
func RemoveWorktree(projectPath, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch name is required")
	}
	worktrees, err := ListWorktrees(projectPath)
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}
	var wtPath string
	for _, wt := range worktrees {
		if wt.Branch == branch && wt.Path != projectPath {
			wtPath = wt.Path
			break
		}
	}
	if wtPath == "" {
		return fmt.Errorf("no worktree found for branch %q", branch)
	}
	out, err := exec.Command("git", "-C", projectPath, "worktree", "remove", "--force", wtPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteBranch removes a local branch from projectPath using -D (force delete,
// allows unmerged branches). Refuses to delete the current main-checkout branch.
func DeleteBranch(projectPath, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch name is required")
	}
	mainBranch, _ := MainCheckoutBranch(projectPath)
	if mainBranch != "" && branch == mainBranch {
		return fmt.Errorf("cannot delete the branch currently checked out in main")
	}
	out, err := exec.Command("git", "-C", projectPath, "branch", "-D", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
