package sync

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateUUIDPrefixed(t *testing.T) {
	dir := t.TempDir()

	// Seed with UUID-prefixed files (leftover from reverted multi-session branch).
	uuid := "f2b5f5f8-5ec7-4252-aac0-6ab5945a922e"
	if err := os.WriteFile(filepath.Join(dir, uuid+".meta.enc"), []byte("meta"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uuid+".session.enc"), []byte("session"), 0644); err != nil {
		t.Fatal(err)
	}

	if !migrateUUIDPrefixed(dir) {
		t.Fatal("expected migration to happen")
	}

	// Bare files should now exist.
	if _, err := os.Stat(filepath.Join(dir, metaFilename)); err != nil {
		t.Fatalf("meta.enc missing after migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, sessionFilename)); err != nil {
		t.Fatalf("session.enc missing after migration: %v", err)
	}

	// Old files should be gone.
	if _, err := os.Stat(filepath.Join(dir, uuid+".meta.enc")); !os.IsNotExist(err) {
		t.Fatal("old meta file still exists after migration")
	}
	if _, err := os.Stat(filepath.Join(dir, uuid+".session.enc")); !os.IsNotExist(err) {
		t.Fatal("old session file still exists after migration")
	}
}

func TestMigrateUUIDPrefixed_NoopWhenBareExists(t *testing.T) {
	dir := t.TempDir()

	// Bare files already in place.
	if err := os.WriteFile(filepath.Join(dir, metaFilename), []byte("meta"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionFilename), []byte("session"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also drop a UUID-prefixed file — should NOT overwrite bare.
	uuid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := os.WriteFile(filepath.Join(dir, uuid+".meta.enc"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if migrateUUIDPrefixed(dir) {
		t.Fatal("expected no migration when bare files exist")
	}

	// Bare content unchanged.
	data, _ := os.ReadFile(filepath.Join(dir, metaFilename))
	if string(data) != "meta" {
		t.Fatalf("bare meta.enc was overwritten: %q", data)
	}
}

func TestMigrateUUIDPrefixed_NoopEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if migrateUUIDPrefixed(dir) {
		t.Fatal("expected no migration on empty directory")
	}
}

func TestMigrateUUIDPrefixed_MetaOnlyNoSession(t *testing.T) {
	dir := t.TempDir()

	uuid := "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(dir, uuid+".meta.enc"), []byte("meta"), 0644); err != nil {
		t.Fatal(err)
	}

	if !migrateUUIDPrefixed(dir) {
		t.Fatal("expected migration even with meta-only")
	}

	if _, err := os.Stat(filepath.Join(dir, metaFilename)); err != nil {
		t.Fatalf("meta.enc missing: %v", err)
	}
	// session.enc should not exist (there was no source).
	if _, err := os.Stat(filepath.Join(dir, sessionFilename)); !os.IsNotExist(err) {
		t.Fatal("session.enc should not exist when source was absent")
	}
}

// initTestSyncRepo creates a git-initialized sync dir seeded with one
// encrypted session under the given projectName. Returns the sync dir
// path and the key used.
func initTestSyncRepo(t *testing.T, key Key, projectName, sessionID string) string {
	t.Helper()
	syncDir := t.TempDir()

	// Create a bare remote so git pull works inside RepairNames.
	bareDir := filepath.Join(t.TempDir(), "remote.git")

	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runIn(t.TempDir(), "init", "--bare", bareDir)

	// git init + initial commit so ensureRepo and gitRun work.
	run := func(args ...string) {
		t.Helper()
		runIn(syncDir, args...)
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")
	run("remote", "add", "origin", bareDir)

	// Create the project directory with encrypted meta + session.
	dirHash := DirHash(key, projectName)
	projectDir := filepath.Join(syncDir, dirHash)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	meta := SessionMeta{
		SessionID:   sessionID,
		Title:       "test-title",
		ProjectName: projectName,
		Hostname:    "testhost",
		PushedAt:    time.Now().Add(-time.Hour),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Encrypt(key, metaBytes, adMeta(dirHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, metaFilename), envelope, 0644); err != nil {
		t.Fatal(err)
	}

	sessionData := []byte(`{"type":"say","role":"user","message":"hello"}`)
	if err := EncryptFile(key, writeTmp(t, sessionData), filepath.Join(projectDir, sessionFilename), adSession(dirHash)); err != nil {
		t.Fatal(err)
	}

	run("add", "-A")
	run("commit", "-m", "seed")
	run("push", "-u", "origin", "main")

	return syncDir
}

// writeTmp writes data to a temp file and returns its path.
func writeTmp(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestRepairNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	key := freshKey(t)
	t.Setenv(keyEnvVar, base64.StdEncoding.EncodeToString(key[:]))

	badName := "moma-chatbot [setup-scaffold]"
	goodName := "moma-chatbot"
	sessionID := "3591a294-1234-5678-9abc-def012345678"

	syncDir := initTestSyncRepo(t, key, badName, sessionID)

	// Verify the bad-name directory exists.
	badHash := DirHash(key, badName)
	goodHash := DirHash(key, goodName)
	if badHash == goodHash {
		t.Fatal("bad and good hashes are identical — test is broken")
	}
	if _, err := os.Stat(filepath.Join(syncDir, badHash)); err != nil {
		t.Fatalf("bad-hash dir missing before repair: %v", err)
	}

	n, err := RepairNames(syncDir)
	if err != nil {
		t.Fatalf("RepairNames: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 repaired, got %d", n)
	}

	// Old directory should be gone.
	if _, err := os.Stat(filepath.Join(syncDir, badHash)); !os.IsNotExist(err) {
		t.Fatal("bad-hash dir still exists after repair")
	}

	// New directory should exist with correct metadata.
	newDir := filepath.Join(syncDir, goodHash)
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("good-hash dir missing after repair: %v", err)
	}

	meta, err := readMeta(key, newDir, goodHash)
	if err != nil {
		t.Fatalf("readMeta after repair: %v", err)
	}
	if meta.ProjectName != goodName {
		t.Errorf("ProjectName = %q, want %q", meta.ProjectName, goodName)
	}
	if meta.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", meta.SessionID, sessionID)
	}

	// Session data should decrypt with the new hash's AD.
	tmpOut := filepath.Join(t.TempDir(), "out.jsonl")
	if err := DecryptFile(key, filepath.Join(newDir, sessionFilename), tmpOut, adSession(goodHash)); err != nil {
		t.Fatalf("session decrypt after repair: %v", err)
	}
}

func TestRepairNamesNoopClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	key := freshKey(t)
	t.Setenv(keyEnvVar, base64.StdEncoding.EncodeToString(key[:]))

	// Seed with a correctly-named project — repair should be a no-op.
	syncDir := initTestSyncRepo(t, key, "moma-chatbot", "aaaa-bbbb")

	n, err := RepairNames(syncDir)
	if err != nil {
		t.Fatalf("RepairNames: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 repaired for clean repo, got %d", n)
	}
}

func TestRepairNamesWorktreeScope(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	key := freshKey(t)
	t.Setenv(keyEnvVar, base64.StdEncoding.EncodeToString(key[:]))

	badName := "unky-mo@feat [fix-sync]"
	goodName := "unky-mo@feat"
	syncDir := initTestSyncRepo(t, key, badName, "cccc-dddd")

	n, err := RepairNames(syncDir)
	if err != nil {
		t.Fatalf("RepairNames: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 repaired, got %d", n)
	}

	goodHash := DirHash(key, goodName)
	meta, err := readMeta(key, filepath.Join(syncDir, goodHash), goodHash)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.ProjectName != goodName {
		t.Errorf("ProjectName = %q, want %q", meta.ProjectName, goodName)
	}
}

func TestReadAllMetaCommitsMigration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	key := freshKey(t)
	t.Setenv(keyEnvVar, base64.StdEncoding.EncodeToString(key[:]))

	syncDir := initTestSyncRepo(t, key, "alpha", "aaaa-1111")
	dirHash := DirHash(key, "alpha")
	projectDir := filepath.Join(syncDir, dirHash)

	// Simulate a UUID-prefixed state: rename the bare files to UUID-prefixed
	// names, then commit that state. readAllMeta should migrate them back
	// and commit.
	uuid := "f2b5f5f8-5ec7-4252-aac0-6ab5945a922e"
	os.Rename(filepath.Join(projectDir, metaFilename), filepath.Join(projectDir, uuid+".meta.enc"))
	os.Rename(filepath.Join(projectDir, sessionFilename), filepath.Join(projectDir, uuid+".session.enc"))
	gitRun(syncDir, "add", "-A")
	gitRun(syncDir, "commit", "-m", "simulate UUID-prefixed state")

	// readAllMeta should migrate and commit.
	_, err := readAllMeta(key, syncDir)
	if err != nil {
		t.Fatalf("readAllMeta: %v", err)
	}

	// The repo should be clean (migration committed).
	cmd := exec.Command("git", "-C", syncDir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("repo not clean after readAllMeta migration:\n%s", out)
	}

	// The bare files should exist.
	if _, err := os.Stat(filepath.Join(projectDir, metaFilename)); err != nil {
		t.Fatalf("meta.enc missing after readAllMeta migration: %v", err)
	}
}
