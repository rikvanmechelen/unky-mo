package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// SessionMeta is the metadata stored alongside each synced session. It is
// encrypted at rest on the remote. ProjectPath from the source machine is
// intentionally omitted: cross-machine paths differ, and we don't want the
// field present in metadata if the key is ever misused.
type SessionMeta struct {
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`
	ProjectName string    `json:"project_name"`
	Hostname    string    `json:"hostname"`
	PushedAt    time.Time `json:"pushed_at"`
}

const (
	metaFilename    = "meta.enc"
	sessionFilename = "session.enc"
)

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

// Push exports a project's active session to the sync repo (encrypted) and pushes.
func Push(projectName, projectPath, syncDir string) error {
	if err := ensureRepo(syncDir); err != nil {
		return err
	}
	key, err := LoadKey()
	if err != nil {
		return err
	}

	sessions := claude.RecentSessions(projectPath, 1)
	if len(sessions) == 0 {
		return fmt.Errorf("no sessions found for %s", projectName)
	}
	session := sessions[0]

	dirHash := DirHash(key, projectName)
	projectDir := filepath.Join(syncDir, dirHash)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return err
	}

	srcJSONL := filepath.Join(claude.ProjectsDirForPath(projectPath), session.SessionID+".jsonl")
	if err := EncryptFile(key, srcJSONL, filepath.Join(projectDir, sessionFilename), adSession(dirHash)); err != nil {
		return fmt.Errorf("encrypt session: %w", err)
	}

	hostname, _ := os.Hostname()
	meta := SessionMeta{
		SessionID:   session.SessionID,
		Title:       session.Title,
		ProjectName: projectName,
		Hostname:    hostname,
		PushedAt:    time.Now(),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	envelope, err := Encrypt(key, metaBytes, adMeta(dirHash))
	if err != nil {
		return fmt.Errorf("encrypt meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, metaFilename), envelope, 0644); err != nil {
		return err
	}

	// Remove any stray files (legacy plaintext, old session IDs) inside this
	// project's directory — only the two encrypted files should remain.
	entries, _ := os.ReadDir(projectDir)
	for _, e := range entries {
		if e.Name() == metaFilename || e.Name() == sessionFilename {
			continue
		}
		os.Remove(filepath.Join(projectDir, e.Name()))
	}

	// Generic commit message so the commit log doesn't leak project names.
	if err := gitRun(syncDir, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := gitRun(syncDir, "commit", "-m", "sync update"); err != nil {
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
// local Claude projects directory for localProjectPath.
func Pull(projectName, localProjectPath, syncDir string) (*SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}
	key, err := LoadKey()
	if err != nil {
		return nil, err
	}
	if err := gitRun(syncDir, "pull"); err != nil {
		return nil, fmt.Errorf("git pull: %w", err)
	}

	dirHash := DirHash(key, projectName)
	projectDir := filepath.Join(syncDir, dirHash)
	if _, err := os.Stat(projectDir); err != nil {
		return nil, fmt.Errorf("no synced session for %s", projectName)
	}

	meta, err := readMeta(key, projectDir, dirHash)
	if err != nil {
		return nil, err
	}

	dstDir := claude.ProjectsDirForPath(localProjectPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}
	dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")
	if err := DecryptFile(key, filepath.Join(projectDir, sessionFilename), dstJSONL, adSession(dirHash)); err != nil {
		return nil, fmt.Errorf("decrypt session: %w", err)
	}
	return meta, nil
}

// List returns all available sessions in the sync repo.
func List(syncDir string) ([]SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}
	key, err := LoadKey()
	if err != nil {
		return nil, err
	}
	gitRun(syncDir, "pull")
	return readAllMeta(key, syncDir)
}

// ListLocal reads synced sessions from the local sync repo without pulling.
func ListLocal(syncDir string) ([]SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}
	key, err := LoadKey()
	if err != nil {
		return nil, err
	}
	return readAllMeta(key, syncDir)
}

// PullAll fetches every session in the sync repo and copies each JSONL to the
// appropriate local Claude projects directory. Sessions whose project isn't
// present locally are skipped with a warning (project metadata no longer
// carries the source machine's path).
type ProjectPathResolver func(projectName string) string

func PullAll(syncDir string, resolve ProjectPathResolver) ([]SessionMeta, error) {
	if err := ensureRepo(syncDir); err != nil {
		return nil, err
	}
	key, err := LoadKey()
	if err != nil {
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
		if !e.IsDir() || !IsDirHash(e.Name()) {
			continue
		}
		projectDir := filepath.Join(syncDir, e.Name())
		meta, err := readMeta(key, projectDir, e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", e.Name(), err)
			continue
		}
		dstPath := resolve(meta.ProjectName)
		if dstPath == "" {
			fmt.Fprintf(os.Stderr, "skipping %s: no local workspace for project (run 'mo scan' after checking it out)\n", meta.ProjectName)
			continue
		}
		dstDir := claude.ProjectsDirForPath(dstPath)
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			continue
		}
		dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")
		if err := DecryptFile(key, filepath.Join(projectDir, sessionFilename), dstJSONL, adSession(e.Name())); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: decrypt session: %v\n", meta.ProjectName, err)
			continue
		}
		sessions = append(sessions, *meta)
	}
	return sessions, nil
}

// Migrate re-encrypts any legacy plaintext project directories in the sync
// repo into the hashed/encrypted layout, commits, and pushes.
func Migrate(syncDir string) (int, error) {
	if err := ensureRepo(syncDir); err != nil {
		return 0, err
	}
	key, err := LoadKey()
	if err != nil {
		return 0, err
	}
	if err := gitRun(syncDir, "pull"); err != nil {
		return 0, fmt.Errorf("git pull: %w", err)
	}

	entries, err := os.ReadDir(syncDir)
	if err != nil {
		return 0, err
	}

	migrated := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" || IsDirHash(e.Name()) {
			continue
		}
		oldDir := filepath.Join(syncDir, e.Name())
		metaPath := filepath.Join(oldDir, "session.json")
		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: no session.json (%v)\n", e.Name(), err)
			continue
		}
		// Legacy meta included ProjectPath; unmarshal into a superset struct
		// to avoid losing it silently, then write the modern shape.
		var legacy struct {
			SessionID   string    `json:"session_id"`
			Title       string    `json:"title"`
			ProjectName string    `json:"project_name"`
			ProjectPath string    `json:"project_path"`
			Hostname    string    `json:"hostname"`
			PushedAt    time.Time `json:"pushed_at"`
		}
		if err := json.Unmarshal(metaData, &legacy); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: bad session.json: %v\n", e.Name(), err)
			continue
		}
		if legacy.ProjectName == "" || legacy.SessionID == "" {
			fmt.Fprintf(os.Stderr, "skipping %s: incomplete session.json\n", e.Name())
			continue
		}
		srcJSONL := filepath.Join(oldDir, legacy.SessionID+".jsonl")
		if _, err := os.Stat(srcJSONL); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: missing %s.jsonl\n", e.Name(), legacy.SessionID)
			continue
		}

		dirHash := DirHash(key, legacy.ProjectName)
		newDir := filepath.Join(syncDir, dirHash)
		if err := os.MkdirAll(newDir, 0755); err != nil {
			return migrated, err
		}
		if err := EncryptFile(key, srcJSONL, filepath.Join(newDir, sessionFilename), adSession(dirHash)); err != nil {
			return migrated, fmt.Errorf("encrypt %s: %w", legacy.ProjectName, err)
		}
		newMeta := SessionMeta{
			SessionID:   legacy.SessionID,
			Title:       legacy.Title,
			ProjectName: legacy.ProjectName,
			Hostname:    legacy.Hostname,
			PushedAt:    legacy.PushedAt,
		}
		metaBytes, err := json.Marshal(newMeta)
		if err != nil {
			return migrated, err
		}
		envelope, err := Encrypt(key, metaBytes, adMeta(dirHash))
		if err != nil {
			return migrated, fmt.Errorf("encrypt meta %s: %w", legacy.ProjectName, err)
		}
		if err := os.WriteFile(filepath.Join(newDir, metaFilename), envelope, 0644); err != nil {
			return migrated, err
		}

		// Remove the plaintext directory from working tree + index.
		if err := gitRun(syncDir, "rm", "-r", "--", e.Name()); err != nil {
			// Fall back to local removal if the dir wasn't tracked (unlikely).
			_ = os.RemoveAll(oldDir)
		}
		migrated++
	}

	if migrated == 0 {
		return 0, nil
	}

	if err := gitRun(syncDir, "add", "-A"); err != nil {
		return migrated, fmt.Errorf("git add: %w", err)
	}
	if err := gitRun(syncDir, "commit", "-m", "sync: migrate to encrypted layout"); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			return migrated, fmt.Errorf("git commit: %w", err)
		}
	}
	if err := gitRun(syncDir, "push"); err != nil {
		return migrated, fmt.Errorf("git push: %w", err)
	}
	return migrated, nil
}

func readMeta(key Key, projectDir, dirHash string) (*SessionMeta, error) {
	envelope, err := os.ReadFile(filepath.Join(projectDir, metaFilename))
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	pt, err := Decrypt(key, envelope, adMeta(dirHash))
	if err != nil {
		return nil, fmt.Errorf("decrypt meta: %w", err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(pt, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &meta, nil
}

func readAllMeta(key Key, syncDir string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(syncDir)
	if err != nil {
		return nil, err
	}
	var out []SessionMeta
	for _, e := range entries {
		if !e.IsDir() || !IsDirHash(e.Name()) {
			continue
		}
		meta, err := readMeta(key, filepath.Join(syncDir, e.Name()), e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, *meta)
	}
	return out, nil
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

