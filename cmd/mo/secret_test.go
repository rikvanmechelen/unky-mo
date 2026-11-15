package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A secret still in the config dir moves to the state dir unchanged, once;
// a new one goes straight to the state dir.
func TestSecretPathMovesFromConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	oldTLS := filepath.Join(home, ".config", "unky-mo", "tls")
	if err := os.MkdirAll(oldTLS, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(oldTLS, "ca-key.pem"), []byte("KEY"), 0o600)

	got := secretPath("tls")
	want := filepath.Join(home, ".local", "state", "unky-mo", "tls")
	if got != want {
		t.Fatalf("path %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(want, "ca-key.pem")); string(b) != "KEY" {
		t.Error("the CA key didn't move")
	}
	if _, err := os.Stat(oldTLS); !os.IsNotExist(err) {
		t.Error("the old copy is still there")
	}
	if secretPath("tls") != want || secretPath("push") != filepath.Join(home, ".local", "state", "unky-mo", "push") {
		t.Error("later calls and new secrets use the state dir")
	}
}
