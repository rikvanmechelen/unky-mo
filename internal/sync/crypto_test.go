package sync

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

func freshKey(t *testing.T) Key {
	t.Helper()
	var k Key
	if _, err := io.ReadFull(rand.Reader, k[:]); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := freshKey(t)
	for _, size := range []int{0, 1, 16, 64, 1 << 10, 1 << 16} {
		pt := make([]byte, size)
		if _, err := io.ReadFull(rand.Reader, pt); err != nil {
			t.Fatalf("random payload: %v", err)
		}
		ad := []byte("session:abcd")
		envelope, err := Encrypt(key, pt, ad)
		if err != nil {
			t.Fatalf("encrypt size=%d: %v", size, err)
		}
		got, err := Decrypt(key, envelope, ad)
		if err != nil {
			t.Fatalf("decrypt size=%d: %v", size, err)
		}
		if !bytes.Equal(pt, got) {
			t.Fatalf("round-trip mismatch size=%d", size)
		}
	}
}

func TestDecryptWrongKey(t *testing.T) {
	k1 := freshKey(t)
	k2 := freshKey(t)
	envelope, err := Encrypt(k1, []byte("hello"), []byte("ad"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(k2, envelope, []byte("ad")); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := freshKey(t)
	envelope, err := Encrypt(key, []byte("hello world"), []byte("ad"))
	if err != nil {
		t.Fatal(err)
	}
	// flip a byte in the ciphertext region (past magic + nonce)
	envelope[magicLen+nonceLen] ^= 0x01
	if _, err := Decrypt(key, envelope, []byte("ad")); err == nil {
		t.Fatal("expected tampered ciphertext to fail")
	}
}

func TestDecryptWrongAD(t *testing.T) {
	key := freshKey(t)
	envelope, err := Encrypt(key, []byte("payload"), adMeta("deadbeef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(key, envelope, adSession("deadbeef")); err == nil {
		t.Fatal("expected AD mismatch to fail")
	}
}

func TestDecryptMagicMismatch(t *testing.T) {
	key := freshKey(t)
	envelope, err := Encrypt(key, []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope[0] ^= 0xff
	_, err = Decrypt(key, envelope, nil)
	if err == nil || !strings.Contains(err.Error(), "magic") {
		t.Fatalf("expected magic error, got: %v", err)
	}
}

func TestDecryptTooShort(t *testing.T) {
	key := freshKey(t)
	if _, err := Decrypt(key, []byte("ab"), nil); err == nil {
		t.Fatal("expected short-input error")
	}
}

func TestDirHashDeterministic(t *testing.T) {
	key := freshKey(t)
	a := DirHash(key, "unky-mo")
	b := DirHash(key, "unky-mo")
	if a != b {
		t.Fatalf("DirHash not deterministic: %s vs %s", a, b)
	}
	if len(a) != dirHashLen*2 {
		t.Fatalf("DirHash wrong length: %d", len(a))
	}
	if !IsDirHash(a) {
		t.Fatalf("IsDirHash rejected its own output: %s", a)
	}
}

func TestDirHashDistinctProjects(t *testing.T) {
	key := freshKey(t)
	if DirHash(key, "foo") == DirHash(key, "bar") {
		t.Fatal("different project names produced same DirHash")
	}
}

func TestDirHashKeySeparation(t *testing.T) {
	k1 := freshKey(t)
	k2 := freshKey(t)
	if DirHash(k1, "foo") == DirHash(k2, "foo") {
		t.Fatal("same project name produced same DirHash under different keys")
	}
}

func TestIsDirHashRejectsOldFormat(t *testing.T) {
	if IsDirHash("unky-mo") {
		t.Fatal("IsDirHash accepted plaintext project name")
	}
	if IsDirHash("deadbeef") {
		t.Fatal("IsDirHash accepted short hex")
	}
	if IsDirHash("ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ") {
		t.Fatal("IsDirHash accepted non-hex chars")
	}
}

func TestInitKeyLoadKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(keyEnvVar, "")

	path, err := InitKey(false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("key written outside HOME override: %s", path)
	}

	k1, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey after InitKey: %v", err)
	}
	var zero Key
	if k1 == zero {
		t.Fatal("LoadKey returned zero key")
	}

	// InitKey without --force must refuse.
	if _, err := InitKey(false); err == nil {
		t.Fatal("expected InitKey to refuse overwrite without force")
	}

	// InitKey with force produces a different key.
	if _, err := InitKey(true); err != nil {
		t.Fatalf("InitKey --force: %v", err)
	}
	k2, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey after force: %v", err)
	}
	if k1 == k2 {
		t.Fatal("InitKey --force produced identical key")
	}
}

func TestLoadKeyEnvVarPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Write a file-based key first.
	if _, err := InitKey(false); err != nil {
		t.Fatal(err)
	}
	fileKey, err := LoadKey()
	if err != nil {
		t.Fatal(err)
	}

	// Now set the env var to a different key — env var wins.
	envKey := freshKey(t)
	t.Setenv(keyEnvVar, base64.StdEncoding.EncodeToString(envKey[:]))
	got, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey with env var: %v", err)
	}
	if got != envKey {
		t.Fatal("env var key did not override file key")
	}
	if got == fileKey {
		t.Fatal("env and file keys happened to match; test is inconclusive")
	}
}

func TestLoadKeyMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(keyEnvVar, "")
	if _, err := LoadKey(); err == nil {
		t.Fatal("expected error when no key is available")
	}
}

