package sync

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// SessionMeta is the metadata stored alongside each synced session.
type SessionMeta struct {
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`
	ProjectPath string    `json:"project_path"`
	ProjectName string    `json:"project_name"`
	Hostname    string    `json:"hostname"`
	PushedAt    time.Time `json:"pushed_at"`
}

// DefaultSyncDir returns the default path for the sync repo.
func DefaultSyncDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo", "sync")
}

// Init clones the remote repo into the sync directory.
func Init(repoURL, syncDir string) error {
	if _, err := os.Stat(filepath.Join(syncDir, ".git")); err == nil {
		return fmt.Errorf("sync repo already initialized at %s", syncDir)
	}
	if err := os.MkdirAll(filepath.Dir(syncDir), 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", repoURL, syncDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Push exports a project's active session to the sync repo and pushes.
func Push(projectName, projectPath, syncDir string) error {
	if err := ensureRepo(syncDir); err != nil {
		return err
	}

	// Find the most recent session for this project
	sessions := claude.RecentSessions(projectPath, 1)
	if len(sessions) == 0 {
		return fmt.Errorf("no sessions found for %s", projectName)
	}
	session := sessions[0]

	// Create project directory in sync repo
	projectDir := filepath.Join(syncDir, projectName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return err
	}

	// Copy the session JSONL
	srcJSONL := filepath.Join(claude.ProjectsDirForPath(projectPath), session.SessionID+".jsonl")
	dstJSONL := filepath.Join(projectDir, session.SessionID+".jsonl")

	if err := copyFile(srcJSONL, dstJSONL); err != nil {
		return fmt.Errorf("copying session file: %w", err)
	}

	// Remove any old JSONL files (keep only the latest)
	entries, _ := os.ReadDir(projectDir)
	for _, e := range entries {
		if e.Name() != session.SessionID+".jsonl" && strings.HasSuffix(e.Name(), ".jsonl") {
			os.Remove(filepath.Join(projectDir, e.Name()))
		}
	}

	// Write session metadata
	hostname, _ := os.Hostname()
	meta := SessionMeta{
		SessionID:   session.SessionID,
		Title:       session.Title,
		ProjectPath: projectPath,
		ProjectName: projectName,
		Hostname:    hostname,
		PushedAt:    time.Now(),
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(projectDir, "session.json"), metaData, 0644); err != nil {
		return err
	}

	// Git add, commit, push
	msg := fmt.Sprintf("sync %s from %s", projectName, hostname)
	if err := gitRun(syncDir, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := gitRun(syncDir, "commit", "-m", msg); err != nil {
		// No changes to commit is OK
		if !strings.Contains(err.Error(), "nothing to commit") {
			return fmt.Errorf("git commit: %w", err)
		}
		fmt.Println("No changes to sync")
		return nil
	}
	if err := gitRun(syncDir, "push"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	return nil
}

// Pull fetches a project's session from the sync repo and copies it into the
// local Claude projects directory for localProjectPath. localProjectPath is the
// destination machine's path for the project — it may differ from the source
// machine's path (e.g. /Users/... on macOS vs /home/... on Linux), so callers
// must resolve it locally rather than relying on the synced metadata.
func Pull(projectName, localProjectPath, syncDir string) (*SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}

	// Git pull
	if err := gitRun(syncDir, "pull"); err != nil {
		return nil, fmt.Errorf("git pull: %w", err)
	}

	// Read session metadata
	metaPath := filepath.Join(syncDir, projectName, "session.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("no synced session for %s", projectName)
	}
	var meta SessionMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, fmt.Errorf("reading session metadata: %w", err)
	}

	// Copy JSONL to Claude's projects directory, encoded from the local path
	srcJSONL := filepath.Join(syncDir, projectName, meta.SessionID+".jsonl")
	dstDir := claude.ProjectsDirForPath(localProjectPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}
	dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")

	if err := copyFile(srcJSONL, dstJSONL); err != nil {
		return nil, fmt.Errorf("copying session file: %w", err)
	}

	return &meta, nil
}

// List returns all available sessions in the sync repo.
func List(syncDir string) ([]SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}

	// Pull latest
	gitRun(syncDir, "pull")

	entries, err := os.ReadDir(syncDir)
	if err != nil {
		return nil, err
	}

	var sessions []SessionMeta
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		metaPath := filepath.Join(syncDir, e.Name(), "session.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta SessionMeta
		if json.Unmarshal(data, &meta) == nil {
			sessions = append(sessions, meta)
		}
	}
	return sessions, nil
}

// ProjectPathResolver returns the local filesystem path for a synced project name
// on this machine, or "" if the project isn't present in the local workspace.
type ProjectPathResolver func(projectName string) string

// PullAll fetches every session in the sync repo and copies each JSONL to the
// appropriate local Claude projects directory. When resolve returns a local path
// for a project, the session lands in that path's encoded dir; otherwise it falls
// back to the source machine's path from the metadata (so the session still shows
// up in listings even if the project hasn't been checked out locally yet).
func PullAll(syncDir string, resolve ProjectPathResolver) ([]SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}
	if err := gitRun(syncDir, "pull"); err != nil {
		return nil, fmt.Errorf("git pull: %w", err)
	}

	entries, err := os.ReadDir(syncDir)
	if err != nil {
		return nil, err
	}

	var sessions []SessionMeta
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		metaPath := filepath.Join(syncDir, e.Name(), "session.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		dstPath := resolve(meta.ProjectName)
		if dstPath == "" {
			dstPath = meta.ProjectPath
		}

		srcJSONL := filepath.Join(syncDir, e.Name(), meta.SessionID+".jsonl")
		dstDir := claude.ProjectsDirForPath(dstPath)
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			continue
		}
		dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")
		if err := copyFile(srcJSONL, dstJSONL); err != nil {
			continue
		}

		sessions = append(sessions, meta)
	}
	return sessions, nil
}

func ensureRepo(syncDir string) error {
	if _, err := os.Stat(filepath.Join(syncDir, ".git")); err != nil {
		return fmt.Errorf("sync repo not initialized. Run 'mo sync init <repo-url>' first")
	}
	return nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
