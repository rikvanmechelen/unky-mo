package sync

import (
	"os"
	"path/filepath"
	"testing"
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
