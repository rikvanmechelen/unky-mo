package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	return "", fmt.Errorf("git worktree add: %s / %s", strings.TrimSpace(string(out2)), firstErr)
}
