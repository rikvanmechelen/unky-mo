package web

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	hashScheme      = "pbkdf2-sha256"
	hashSaltLen     = 16
	hashKeyLen      = 32
	credentialsMode = 0600
)

// hashIterations is the PBKDF2 work factor for newly hashed passwords
// (OWASP's 2023 recommendation for PBKDF2-HMAC-SHA256). A var so tests can
// lower it; the count used is stored in each hash, so changing it never
// invalidates existing credentials.
var hashIterations = 600_000

// Credentials is the `mo web` login, stored in its own 0600 file. Only a
// salted PBKDF2 hash of the password is kept — the server never needs the
// plaintext, so nothing reversible sits on disk.
type Credentials struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
}

// NewCredentials hashes password into a fresh Credentials.
func NewCredentials(username, password string) (*Credentials, error) {
	if username == "" || password == "" {
		return nil, errors.New("username and password must both be non-empty")
	}
	salt := make([]byte, hashSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashKeyLen)
	if err != nil {
		return nil, err
	}
	enc := base64.RawStdEncoding
	hash := fmt.Sprintf("%s$%d$%s$%s", hashScheme, hashIterations, enc.EncodeToString(salt), enc.EncodeToString(key))
	return &Credentials{Username: username, PasswordHash: hash}, nil
}

// Verify reports whether username/password match. The password is always
// hashed, even on a username mismatch, so timing doesn't reveal which half
// was wrong.
func (c *Credentials) Verify(username, password string) bool {
	parts := strings.Split(c.PasswordHash, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	gotUser := sha256.Sum256([]byte(username))
	wantUser := sha256.Sum256([]byte(c.Username))
	userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) == 1
	passOK := subtle.ConstantTimeCompare(got, want) == 1
	return userOK && passOK
}

// LoadCredentials reads the credentials file at path. A missing file is not
// an error — it returns (nil, nil), meaning auth isn't configured. A file
// readable by group/other is rejected, mirroring the sync key's check.
func LoadCredentials(path string) (*Credentials, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("web credentials file %s has loose permissions (%o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	var c Credentials
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("read web credentials: %w", err)
	}
	if c.Username == "" || c.PasswordHash == "" {
		return nil, fmt.Errorf("web credentials file %s is incomplete; re-run 'mo web auth set'", path)
	}
	return &c, nil
}

// SaveCredentials writes c to path with mode 0600, replacing any existing
// file atomically.
func SaveCredentials(path string, c *Credentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".web-auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(credentialsMode); err != nil {
		tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
