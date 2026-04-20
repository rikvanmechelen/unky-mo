package sync

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// setupSyncRepo creates an isolated HOME with a bare upstream + cloned sync
// repo + fresh sync key. Returns the sync dir path and a live Key. Skips the
// test when git is not available.
func setupSyncRepo(t *testing.T) (string, Key) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(keyEnvVar, "")

	upstream := filepath.Join(home, "upstream.git")
	mustRun(t, "", "git", "-c", "init.defaultBranch=main", "init", "--bare", upstream)

	syncDir := filepath.Join(home, ".config", "unky-mo", "sync")
	if err := os.MkdirAll(filepath.Dir(syncDir), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustRun(t, "", "git", "-c", "init.defaultBranch=main", "clone", upstream, syncDir)
	mustRun(t, syncDir, "git", "config", "user.email", "t@e.com")
	mustRun(t, syncDir, "git", "config", "user.name", "T")
	mustRun(t, syncDir, "git", "commit", "--allow-empty", "-m", "init")
	mustRun(t, syncDir, "git", "push", "-u", "origin", "HEAD")

	if _, err := InitKey(true); err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	key, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	return syncDir, key
}

// setupReadOnlyRepo sets HOME + key but only fakes the .git dir so
// ensureRepo passes. Intended for read-path tests that hand-build files.
func setupReadOnlyRepo(t *testing.T) (string, Key) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(keyEnvVar, "")

	syncDir := filepath.Join(home, ".config", "unky-mo", "sync")
	if err := os.MkdirAll(filepath.Join(syncDir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := InitKey(true); err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	key, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	return syncDir, key
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// writeFakeSession drops a JSONL file in the Claude projects dir for
// projectPath so Push can find it.
func writeFakeSession(t *testing.T, projectPath, sessionID, body string) {
	t.Helper()
	dir := claude.ProjectsDirForPath(projectPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
}

// writeLegacyPair hand-crafts bare meta.enc + session.enc in a project dir.
// Used to simulate pre-multi-session sync repos.
func writeLegacyPair(t *testing.T, key Key, syncDir, projectName, sessionID string) string {
	t.Helper()
	dirHash := DirHash(key, projectName)
	projectDir := filepath.Join(syncDir, dirHash)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	meta := SessionMeta{
		SessionID:   sessionID,
		Title:       "legacy",
		ProjectName: projectName,
		Hostname:    "legacy-host",
		PushedAt:    time.Now().Add(-1 * time.Hour),
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	env, err := Encrypt(key, mb, adMeta(dirHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, legacyMetaFilename), env, 0644); err != nil {
		t.Fatal(err)
	}
	blob, err := Encrypt(key, []byte("legacy-session-body"), adSession(dirHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, legacySessionFilename), blob, 0644); err != nil {
		t.Fatal(err)
	}
	return projectDir
}

func TestPushMultipleSessionsCoexist(t *testing.T) {
	syncDir, key := setupSyncRepo(t)
	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "sess-aaa", `{"type":"user"}`)
	writeFakeSession(t, projectPath, "sess-bbb", `{"type":"user"}`)

	if err := Push("proj", projectPath, syncDir, "sess-aaa"); err != nil {
		t.Fatalf("push A: %v", err)
	}
	if err := Push("proj", projectPath, syncDir, "sess-bbb"); err != nil {
		t.Fatalf("push B: %v", err)
	}

	projectDir := filepath.Join(syncDir, DirHash(key, "proj"))
	for _, name := range []string{"sess-aaa.meta.enc", "sess-aaa.session.enc", "sess-bbb.meta.enc", "sess-bbb.session.enc"} {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}

	metas, err := ListLocal(syncDir)
	if err != nil {
		t.Fatalf("ListLocal: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 metas, got %d", len(metas))
	}
	seen := map[string]bool{}
	for _, m := range metas {
		seen[m.SessionID] = true
	}
	if !seen["sess-aaa"] || !seen["sess-bbb"] {
		t.Fatalf("missing session IDs in ListLocal: %+v", metas)
	}
}

func TestPushDoesNotDeleteOtherSessions(t *testing.T) {
	syncDir, key := setupSyncRepo(t)
	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "sess-a", `{"type":"user"}`)
	writeFakeSession(t, projectPath, "sess-b", `{"type":"user"}`)

	if err := Push("proj", projectPath, syncDir, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := Push("proj", projectPath, syncDir, "sess-b"); err != nil {
		t.Fatal(err)
	}
	// Re-push A with updated content; B must survive.
	writeFakeSession(t, projectPath, "sess-a", `{"type":"user","updated":true}`)
	if err := Push("proj", projectPath, syncDir, "sess-a"); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(syncDir, DirHash(key, "proj"))
	for _, name := range []string{"sess-a.meta.enc", "sess-a.session.enc", "sess-b.meta.enc", "sess-b.session.enc"} {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err != nil {
			t.Errorf("missing %s after re-push: %v", name, err)
		}
	}
}

func TestPullSpecificSession(t *testing.T) {
	syncDir, _ := setupSyncRepo(t)
	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "sess-a", "A-body")
	writeFakeSession(t, projectPath, "sess-b", "B-body")
	if err := Push("proj", projectPath, syncDir, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := Push("proj", projectPath, syncDir, "sess-b"); err != nil {
		t.Fatal(err)
	}

	// Wipe the local JSONLs and pull just sess-a.
	claudeDir := claude.ProjectsDirForPath(projectPath)
	os.Remove(filepath.Join(claudeDir, "sess-a.jsonl"))
	os.Remove(filepath.Join(claudeDir, "sess-b.jsonl"))

	meta, err := Pull("proj", "sess-a", projectPath, syncDir)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if meta.SessionID != "sess-a" {
		t.Fatalf("pulled wrong session: %s", meta.SessionID)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "sess-a.jsonl")); err != nil {
		t.Errorf("sess-a.jsonl missing after pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "sess-b.jsonl")); err == nil {
		t.Errorf("sess-b.jsonl should not have been pulled")
	}
}

func TestPullNewestWhenSessionIDEmpty(t *testing.T) {
	syncDir, _ := setupSyncRepo(t)
	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "sess-old", "old")
	writeFakeSession(t, projectPath, "sess-new", "new")

	if err := Push("proj", projectPath, syncDir, "sess-old"); err != nil {
		t.Fatal(err)
	}
	// Force PushedAt ordering by sleeping — Push uses time.Now() internally.
	time.Sleep(10 * time.Millisecond)
	if err := Push("proj", projectPath, syncDir, "sess-new"); err != nil {
		t.Fatal(err)
	}

	meta, err := Pull("proj", "", projectPath, syncDir)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if meta.SessionID != "sess-new" {
		t.Fatalf("want newest sess-new, got %s", meta.SessionID)
	}
}

func TestLegacyLayoutReadCompat(t *testing.T) {
	syncDir, key := setupReadOnlyRepo(t)
	writeLegacyPair(t, key, syncDir, "proj", "legacy-sess")

	metas, err := ListLocal(syncDir)
	if err != nil {
		t.Fatalf("ListLocal: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("want 1 meta, got %d: %+v", len(metas), metas)
	}
	if metas[0].SessionID != "legacy-sess" {
		t.Fatalf("wrong session id: %s", metas[0].SessionID)
	}
}

func TestAutoMigrateOnPush(t *testing.T) {
	syncDir, key := setupSyncRepo(t)
	writeLegacyPair(t, key, syncDir, "proj", "legacy-sess")
	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "new-sess", "body")

	if err := Push("proj", projectPath, syncDir, "new-sess"); err != nil {
		t.Fatalf("push: %v", err)
	}

	projectDir := filepath.Join(syncDir, DirHash(key, "proj"))
	// After migrate-on-push the bare pair should be gone.
	if _, err := os.Stat(filepath.Join(projectDir, legacyMetaFilename)); err == nil {
		t.Errorf("legacy meta.enc should have been renamed")
	}
	if _, err := os.Stat(filepath.Join(projectDir, legacySessionFilename)); err == nil {
		t.Errorf("legacy session.enc should have been renamed")
	}
	for _, name := range []string{"legacy-sess.meta.enc", "legacy-sess.session.enc", "new-sess.meta.enc", "new-sess.session.enc"} {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestListLocalReturnsAllSessionsPerProject(t *testing.T) {
	syncDir, _ := setupSyncRepo(t)

	p1 := filepath.Join(t.TempDir(), "p1")
	p2 := filepath.Join(t.TempDir(), "p2")
	for _, id := range []string{"a", "b", "c"} {
		writeFakeSession(t, p1, id, "x")
		if err := Push("projA", p1, syncDir, id); err != nil {
			t.Fatal(err)
		}
	}
	writeFakeSession(t, p2, "z", "x")
	if err := Push("projB", p2, syncDir, "z"); err != nil {
		t.Fatal(err)
	}

	metas, err := ListLocal(syncDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 4 {
		t.Fatalf("want 4 metas, got %d: %+v", len(metas), metas)
	}
	projCount := map[string]int{}
	for _, m := range metas {
		projCount[m.ProjectName]++
	}
	if projCount["projA"] != 3 || projCount["projB"] != 1 {
		t.Fatalf("wrong distribution: %+v", projCount)
	}
}

func TestPushPreservesUnknownSessionFiles(t *testing.T) {
	syncDir, key := setupSyncRepo(t)
	projectDir := filepath.Join(syncDir, DirHash(key, "proj"))
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Opaque (undecryptable) meta blob for some other session.
	fakeName := "ghost.meta.enc"
	if err := os.WriteFile(filepath.Join(projectDir, fakeName), []byte("not-encrypted"), 0644); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(t.TempDir(), "myproject")
	writeFakeSession(t, projectPath, "real", "body")
	if err := Push("proj", projectPath, syncDir, "real"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, fakeName)); err != nil {
		t.Errorf("ghost.meta.enc should have survived push: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	syncDir, key := setupSyncRepo(t)
	writeLegacyPair(t, key, syncDir, "proj", "legacy-sess")
	// The legacy pair was created directly on disk (not committed). Commit it
	// so the bare upstream mirrors the state Migrate would normally operate on.
	mustRun(t, syncDir, "git", "add", "-A")
	mustRun(t, syncDir, "git", "commit", "-m", "seed legacy")
	mustRun(t, syncDir, "git", "push")

	n1, err := Migrate(syncDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n1 == 0 {
		t.Fatalf("first migrate should have renamed the legacy pair")
	}
	n2, err := Migrate(syncDir)
	if err != nil {
		t.Fatalf("migrate 2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second migrate should be a no-op, got %d", n2)
	}
}
