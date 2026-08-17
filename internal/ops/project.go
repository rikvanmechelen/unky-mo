package ops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CreateProjectParams drives CreateProject.
type CreateProjectParams struct {
	ParentDir  string // directory in which to create the new project folder
	FolderName string // name of the new project folder (not a full path)
}

// CreateProjectResult reports what was created.
type CreateProjectResult struct {
	Path string // absolute path of the new project directory
}

// CreateProject creates a new project directory under ParentDir and runs
// `git init` inside it so Claude has a git root to work with. The caller is
// responsible for launching a Claude session in the resulting path.
func CreateProject(p CreateProjectParams) (*CreateProjectResult, error) {
	if strings.TrimSpace(p.ParentDir) == "" {
		return nil, fmt.Errorf("ops.CreateProject: ParentDir is required")
	}
	name := strings.TrimSpace(p.FolderName)
	if name == "" {
		return nil, fmt.Errorf("ops.CreateProject: FolderName is required")
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("ops.CreateProject: FolderName must not contain path separators")
	}

	path := filepath.Join(p.ParentDir, name)

	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("ops.CreateProject: %q already exists", path)
	}

	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("ops.CreateProject: mkdir: %w", err)
	}

	out, err := exec.Command("git", "init", path).CombinedOutput()
	if err != nil {
		// Best-effort cleanup — if we can't remove the dir, leave it and let
		// the error message explain what happened.
		_ = os.Remove(path)
		return nil, fmt.Errorf("ops.CreateProject: git init: %s", strings.TrimSpace(string(out)))
	}

	return &CreateProjectResult{Path: path}, nil
}
