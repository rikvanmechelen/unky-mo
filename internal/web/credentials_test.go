package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCredentials hashes with a tiny work factor so tests stay fast.
func testCredentials(t *testing.T, user, pass string) *Credentials {
	t.Helper()
	orig := hashIterations
	hashIterations = 1000
	t.Cleanup(func() { hashIterations = orig })
	c, err := NewCredentials(user, pass)
	if err != nil {
		t.Fatalf("NewCredentials: %v", err)
	}
	return c
}

func TestCredentialsVerify(t *testing.T) {
	c := testCredentials(t, "mo", "s3cret")

	if strings.Contains(c.PasswordHash, "s3cret") {
		t.Fatalf("hash contains plaintext password: %q", c.PasswordHash)
	}
	if !c.Verify("mo", "s3cret") {
		t.Error("correct credentials rejected")
	}
	if c.Verify("mo", "wrong") {
		t.Error("wrong password accepted")
	}
	if c.Verify("other", "s3cret") {
		t.Error("wrong username accepted")
	}
	if c.Verify("", "") {
		t.Error("empty credentials accepted")
	}
}

func TestCredentialsSaltedPerHash(t *testing.T) {
	a := testCredentials(t, "mo", "same")
	b := testCredentials(t, "mo", "same")
	if a.PasswordHash == b.PasswordHash {
		t.Fatal("two hashes of the same password are identical; salt not applied")
	}
}

func TestCredentialsVerifyMalformedHash(t *testing.T) {
	for _, h := range []string{"", "plain", "bcrypt$1$a$b", "pbkdf2-sha256$x$a$b", "pbkdf2-sha256$0$a$b", "pbkdf2-sha256$10$!!$b"} {
		c := &Credentials{Username: "mo", PasswordHash: h}
		if c.Verify("mo", "") {
			t.Errorf("malformed hash %q verified", h)
		}
	}
}

func TestNewCredentialsRejectsEmpty(t *testing.T) {
	if _, err := NewCredentials("", "pw"); err == nil {
		t.Error("empty username accepted")
	}
	if _, err := NewCredentials("mo", ""); err == nil {
		t.Error("empty password accepted")
	}
}

func TestSaveLoadCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "web-auth.toml")
	c := testCredentials(t, "mo", "s3cret")

	if err := SaveCredentials(path, c); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("file mode: want 0600, got %o", perm)
	}

	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if !got.Verify("mo", "s3cret") {
		t.Fatal("round-tripped credentials don't verify")
	}
}

func TestLoadCredentialsMissing(t *testing.T) {
	got, err := LoadCredentials(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || got != nil {
		t.Fatalf("missing file: want (nil, nil), got (%v, %v)", got, err)
	}
}

func TestLoadCredentialsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-auth.toml")
	if err := SaveCredentials(path, testCredentials(t, "mo", "pw")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil || !strings.Contains(err.Error(), "loose permissions") {
		t.Fatalf("want loose-permissions error, got %v", err)
	}
}

func TestLoadCredentialsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-auth.toml")
	if err := os.WriteFile(path, []byte("username = \"mo\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("incomplete file accepted")
	}
}
