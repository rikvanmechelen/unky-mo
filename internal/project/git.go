package project

import (
	"os/exec"
	"strconv"
	"strings"
)

// GitStatus holds the current git state of a project.
type GitStatus struct {
	Branch string
	Dirty  int // number of modified/untracked files
	Ahead  int // commits ahead of upstream
	Behind int // commits behind upstream
}

// GetGitStatus returns the current git status for a project directory.
func GetGitStatus(projectPath string) GitStatus {
	var gs GitStatus

	// Branch name
	out, err := exec.Command("git", "-C", projectPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return gs
	}
	gs.Branch = strings.TrimSpace(string(out))

	// Dirty file count
	out, err = exec.Command("git", "-C", projectPath, "status", "--porcelain").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			gs.Dirty = 0
		} else {
			gs.Dirty = len(lines)
		}
	}

	// Ahead/behind upstream
	out, err = exec.Command("git", "-C", projectPath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}").Output()
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			gs.Ahead, _ = strconv.Atoi(parts[0])
			gs.Behind, _ = strconv.Atoi(parts[1])
		}
	}

	return gs
}
