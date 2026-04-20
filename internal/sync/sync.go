package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	// Legacy single-session filenames. Readers still accept them as a fallback
	// when no per-session files exist; writers auto-migrate them on push.
	legacyMetaFilename    = "meta.enc"
	legacySessionFilename = "session.enc"

	metaSuffix    = ".meta.enc"
	sessionSuffix = ".session.enc"
)

// sessionMetaFilename returns the per-session meta filename for a session ID.
func sessionMetaFilename(sessionID string) string { return sessionID + metaSuffix }

// sessionBlobFilename returns the per-session encrypted JSONL filename for a session ID.
func sessionBlobFilename(sessionID string) string { return sessionID + sessionSuffix }

// DefaultSyncDir returns the default path for the sync repo.
func DefaultSyncDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo", "sync")
}

// IsConfigured reports whether the user appears to have set up sync on this
// machine — i.e. both the sync repo and the key file exist. Used by callers
// (like the TUI auto-pull) to decide whether sync errors are worth surfacing
// or whether the feature is simply not enabled and should stay silent.
func IsConfigured(syncDir string) bool {
	if _, err := os.Stat(filepath.Join(syncDir, ".git")); err != nil {
		return false
	}
	if _, err := os.Stat(DefaultKeyPath()); err != nil {
		return false
	}
	return true
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

// Push exports a specific session for a project to the sync repo (encrypted)
// and pushes. sessionID must match a JSONL file in the project's Claude
// sessions directory — callers are responsible for resolving which session
// they mean (live session for the window, an explicit --session flag, etc.).
//
// Multiple sessions per project coexist: each session is written as
// <sessionID>.meta.enc / <sessionID>.session.enc inside the project's hashed
// directory. Pushing a new session never disturbs blobs belonging to its
// siblings. Legacy bare meta.enc / session.enc pairs are auto-migrated into
// the prefixed layout on first push.
func Push(projectName, projectPath, syncDir, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	if err := ensureRepo(syncDir); err != nil {
		return err
	}
	key, err := LoadKey()
	if err != nil {
		return err
	}

	srcJSONL := filepath.Join(claude.ProjectsDirForPath(projectPath), sessionID+".jsonl")
	if _, err := os.Stat(srcJSONL); err != nil {
		return fmt.Errorf("session %s not found for %s", sessionID, projectName)
	}
	title := claude.SessionTitle(srcJSONL)

	dirHash := DirHash(key, projectName)
	projectDir := filepath.Join(syncDir, dirHash)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return err
	}

	// Auto-migrate any legacy bare meta.enc / session.enc pair before writing
	// the new session. Never destroys siblings — only touches the bare pair.
	if err := migrateLegacyPairInDir(key, projectDir, dirHash, sessionID); err != nil {
		return fmt.Errorf("migrate legacy layout: %w", err)
	}

	metaPath := filepath.Join(projectDir, sessionMetaFilename(sessionID))
	blobPath := filepath.Join(projectDir, sessionBlobFilename(sessionID))

	if err := EncryptFile(key, srcJSONL, blobPath, adSession(dirHash)); err != nil {
		return fmt.Errorf("encrypt session: %w", err)
	}

	hostname, _ := os.Hostname()
	meta := SessionMeta{
		SessionID:   sessionID,
		Title:       title,
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
	if err := os.WriteFile(metaPath, envelope, 0644); err != nil {
		return err
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
//
// sessionID picks a specific session; if empty, the newest session (by
// PushedAt) is returned. Works against both the per-session layout and the
// legacy single-session layout.
func Pull(projectName, sessionID, localProjectPath, syncDir string) (*SessionMeta, error) {
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

	meta, metaPath, blobPath, err := resolveSession(key, projectDir, dirHash, sessionID)
	if err != nil {
		return nil, err
	}
	_ = metaPath

	dstDir := claude.ProjectsDirForPath(localProjectPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}
	dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")
	if err := DecryptFile(key, blobPath, dstJSONL, adSession(dirHash)); err != nil {
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

// PullAll fetches every session in the sync repo and attempts to copy each
// JSONL into the local Claude projects directory. The returned slice always
// contains one entry per remote session — including ones that couldn't be
// pulled locally (no matching project/worktree, decrypt failure, etc.) — so
// callers can display the full inventory.
type ProjectPathResolver func(projectName string) string

// PullResult is one remote session returned by PullAll. Pulled is true when
// the JSONL was successfully written into the local Claude projects dir.
// Skipped holds a short reason when Pulled is false ("" when Pulled).
type PullResult struct {
	Meta    SessionMeta
	Pulled  bool
	Skipped string
}

func PullAll(syncDir string, resolve ProjectPathResolver) ([]PullResult, error) {
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

	var results []PullResult
	for _, e := range entries {
		if !e.IsDir() || !IsDirHash(e.Name()) {
			continue
		}
		projectDir := filepath.Join(syncDir, e.Name())
		metas, err := readAllMetaInDir(key, projectDir, e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", e.Name(), err)
			continue
		}
		for _, meta := range metas {
			res := PullResult{Meta: meta}
			dstPath := resolve(meta.ProjectName)
			if dstPath == "" {
				res.Skipped = "no local repo"
				results = append(results, res)
				continue
			}
			dstDir := claude.ProjectsDirForPath(dstPath)
			if err := os.MkdirAll(dstDir, 0755); err != nil {
				res.Skipped = fmt.Sprintf("mkdir: %v", err)
				results = append(results, res)
				continue
			}
			_, blobPath := sessionFilePaths(projectDir, meta.SessionID)
			dstJSONL := filepath.Join(dstDir, meta.SessionID+".jsonl")
			if err := DecryptFile(key, blobPath, dstJSONL, adSession(e.Name())); err != nil {
				res.Skipped = fmt.Sprintf("decrypt: %v", err)
				results = append(results, res)
				continue
			}
			res.Pulled = true
			results = append(results, res)
		}
	}
	return results, nil
}

// Migrate re-encrypts any legacy plaintext project directories in the sync
// repo into the hashed/encrypted layout, and renames any bare meta.enc /
// session.enc pairs into the per-session layout. Commits and pushes if any
// change was made.
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
		if !e.IsDir() {
			continue
		}
		if IsDirHash(e.Name()) {
			// Rename legacy bare pair inside an already-hashed dir into the
			// per-session layout. Pass empty pushingSessionID so the bare pair
			// is only renamed (never deleted).
			migrated += migrateLegacyPairInDirCounted(key, filepath.Join(syncDir, e.Name()), e.Name())
			continue
		}
		if e.Name() == ".git" {
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
		// Write directly into the per-session layout.
		if err := EncryptFile(key, srcJSONL, filepath.Join(newDir, sessionBlobFilename(legacy.SessionID)), adSession(dirHash)); err != nil {
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
		if err := os.WriteFile(filepath.Join(newDir, sessionMetaFilename(legacy.SessionID)), envelope, 0644); err != nil {
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

// sessionFilePaths returns (metaPath, blobPath) for a session ID in a project
// dir, assuming the per-session layout. Does not check existence.
func sessionFilePaths(projectDir, sessionID string) (string, string) {
	return filepath.Join(projectDir, sessionMetaFilename(sessionID)),
		filepath.Join(projectDir, sessionBlobFilename(sessionID))
}

// resolveSession locates the meta + blob file for a specific session in a
// project dir. Falls back to the legacy bare pair when nothing matches.
// sessionID == "" picks the newest session in the directory (by PushedAt).
func resolveSession(key Key, projectDir, dirHash, sessionID string) (*SessionMeta, string, string, error) {
	if sessionID != "" {
		metaPath, blobPath := sessionFilePaths(projectDir, sessionID)
		if _, err := os.Stat(metaPath); err == nil {
			meta, err := readMetaFile(key, metaPath, dirHash)
			if err != nil {
				return nil, "", "", err
			}
			return meta, metaPath, blobPath, nil
		}
		// Fall through to legacy fallback — might be a pre-migration pair
		// whose sessionID happens to match.
		legacyMeta, legacyBlob := legacyPairPaths(projectDir)
		if _, err := os.Stat(legacyMeta); err == nil {
			meta, err := readMetaFile(key, legacyMeta, dirHash)
			if err == nil && meta.SessionID == sessionID {
				return meta, legacyMeta, legacyBlob, nil
			}
		}
		return nil, "", "", fmt.Errorf("session %s not found", sessionID)
	}

	// sessionID empty: pick the newest.
	metas, err := readAllMetaInDir(key, projectDir, dirHash)
	if err != nil {
		return nil, "", "", err
	}
	if len(metas) == 0 {
		return nil, "", "", fmt.Errorf("no sessions in %s", projectDir)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].PushedAt.After(metas[j].PushedAt)
	})
	newest := metas[0]
	// Prefer per-session filenames; fall back to legacy.
	metaPath, blobPath := sessionFilePaths(projectDir, newest.SessionID)
	if _, err := os.Stat(metaPath); err != nil {
		metaPath, blobPath = legacyPairPaths(projectDir)
	}
	return &newest, metaPath, blobPath, nil
}

func legacyPairPaths(projectDir string) (string, string) {
	return filepath.Join(projectDir, legacyMetaFilename),
		filepath.Join(projectDir, legacySessionFilename)
}

// migrateLegacyPairInDir renames a bare meta.enc / session.enc pair (if
// present) into the per-session layout. If pushingSessionID matches the
// legacy pair's SessionID, the pair is removed instead (the caller is about
// to overwrite it with a fresh write). If the legacy meta can't be decrypted,
// the pair is left untouched — better to preserve an opaque blob than
// silently destroy it. Returns a non-nil error only on unexpected I/O
// failures.
func migrateLegacyPairInDir(key Key, projectDir, dirHash, pushingSessionID string) error {
	metaPath, blobPath := legacyPairPaths(projectDir)
	if _, err := os.Stat(metaPath); err != nil {
		return nil // no legacy pair
	}
	legacyMeta, err := readMetaFile(key, metaPath, dirHash)
	if err != nil {
		// Undecryptable — leave it alone so a future Migrate/version can deal
		// with it.
		return nil
	}
	if legacyMeta.SessionID == "" {
		return nil
	}
	if legacyMeta.SessionID == pushingSessionID {
		// Caller is about to write a new pair for the same session; discard
		// the legacy pair so we don't end up with both layouts pointing at
		// the same id.
		_ = os.Remove(metaPath)
		_ = os.Remove(blobPath)
		return nil
	}
	// Rename both files to the prefixed layout.
	newMetaPath, newBlobPath := sessionFilePaths(projectDir, legacyMeta.SessionID)
	// If the target already exists (both layouts present for the same id),
	// prefer the prefixed one and drop the legacy pair.
	if _, err := os.Stat(newMetaPath); err == nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(blobPath)
		return nil
	}
	if err := os.Rename(metaPath, newMetaPath); err != nil {
		return err
	}
	if err := os.Rename(blobPath, newBlobPath); err != nil {
		return err
	}
	return nil
}

// migrateLegacyPairInDirCounted is the Migrate-side variant that returns 1
// when a rename (or cleanup) happened, 0 otherwise. Errors are swallowed and
// logged to stderr so one bad project doesn't abort the whole migration.
func migrateLegacyPairInDirCounted(key Key, projectDir, dirHash string) int {
	metaPath, _ := legacyPairPaths(projectDir)
	if _, err := os.Stat(metaPath); err != nil {
		return 0
	}
	before := 0
	if entries, err := os.ReadDir(projectDir); err == nil {
		for _, e := range entries {
			if e.Name() == legacyMetaFilename || e.Name() == legacySessionFilename {
				before++
			}
		}
	}
	if before == 0 {
		return 0
	}
	if err := migrateLegacyPairInDir(key, projectDir, dirHash, ""); err != nil {
		fmt.Fprintf(os.Stderr, "migrate %s: %v\n", filepath.Base(projectDir), err)
		return 0
	}
	// Verify the legacy files are gone.
	if _, err := os.Stat(metaPath); err == nil {
		// Legacy meta still present (e.g. undecryptable). Not counted.
		return 0
	}
	return 1
}

// readMetaFile reads and decrypts one meta envelope at an explicit path.
func readMetaFile(key Key, path, dirHash string) (*SessionMeta, error) {
	envelope, err := os.ReadFile(path)
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

// readAllMetaInDir returns every SessionMeta in one project directory. When
// the per-session layout holds at least one .meta.enc file, the legacy bare
// pair is ignored (the legacy pair is just a pre-migration artifact the
// sibling entry already covers, or an orphaned blob). When no per-session
// files exist, a bare meta.enc is read as a single-session fallback.
func readAllMetaInDir(key Key, projectDir, dirHash string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, err
	}
	var out []SessionMeta
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, metaSuffix) {
			continue
		}
		if name == legacyMetaFilename {
			// Bare meta.enc is treated only as the legacy fallback (handled
			// below); skip it here so it doesn't double-count.
			continue
		}
		meta, err := readMetaFile(key, filepath.Join(projectDir, name), dirHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s/%s: %v\n", filepath.Base(projectDir), name, err)
			continue
		}
		out = append(out, *meta)
	}
	if len(out) > 0 {
		return out, nil
	}
	// Legacy fallback: no per-session files — try bare meta.enc.
	legacyMeta, _ := legacyPairPaths(projectDir)
	if _, err := os.Stat(legacyMeta); err == nil {
		meta, err := readMetaFile(key, legacyMeta, dirHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s/%s: %v\n", filepath.Base(projectDir), legacyMetaFilename, err)
			return nil, nil
		}
		out = append(out, *meta)
	}
	return out, nil
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
		metas, err := readAllMetaInDir(key, filepath.Join(syncDir, e.Name()), e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, metas...)
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
