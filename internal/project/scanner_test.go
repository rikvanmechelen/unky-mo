package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanWorkspace_SymlinkedProjects(t *testing.T) {
	// Create a workspace dir and a separate "real" dir for the target repo.
	workspace := t.TempDir()
	realDir := t.TempDir()

	// Create a real git repo outside the workspace.
	repoPath := filepath.Join(realDir, "my-project")
	if err := os.Mkdir(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink it into the workspace.
	linkPath := filepath.Join(workspace, "my-project")
	if err := os.Symlink(repoPath, linkPath); err != nil {
		t.Fatal(err)
	}

	projects, err := ScanWorkspace([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Name != "my-project" {
		t.Errorf("expected name %q, got %q", "my-project", projects[0].Name)
	}
	if projects[0].Path != linkPath {
		t.Errorf("expected path %q, got %q", linkPath, projects[0].Path)
	}
}

func TestScanWorkspace_SymlinkDedup(t *testing.T) {
	// A real dir and a symlink to it in the same workspace should produce one entry.
	workspace := t.TempDir()

	repoPath := filepath.Join(workspace, "real-repo")
	if err := os.Mkdir(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(workspace, "link-repo")
	if err := os.Symlink(repoPath, linkPath); err != nil {
		t.Fatal(err)
	}

	projects, err := ScanWorkspace([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project (dedup), got %d: %+v", len(projects), projects)
	}
}

func TestScanWorkspace_RegularDirsStillWork(t *testing.T) {
	workspace := t.TempDir()

	repoPath := filepath.Join(workspace, "normal-repo")
	if err := os.Mkdir(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	projects, err := ScanWorkspace([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Name != "normal-repo" {
		t.Errorf("expected name %q, got %q", "normal-repo", projects[0].Name)
	}
}

func TestScanWorkspace_BrokenSymlinkSkipped(t *testing.T) {
	workspace := t.TempDir()

	// Create a symlink pointing to a non-existent target.
	linkPath := filepath.Join(workspace, "broken-link")
	if err := os.Symlink("/nonexistent/path", linkPath); err != nil {
		t.Fatal(err)
	}

	projects, err := ScanWorkspace([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects for broken symlink, got %d", len(projects))
	}
}

