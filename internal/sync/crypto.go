package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	keyEnvVar   = "UNKY_MO_SYNC_KEY"
	magic       = "UMO1"
	magicLen    = 4
	nonceLen    = 12
	dirHashLen  = 16
	keyFileMode = 0600
	dirHashInfo = "unky-mo-dir-v1:"
)

// Key is a 32-byte shared symmetric key used for AES-256-GCM and HMAC-SHA256.
type Key [32]byte

// DefaultKeyPath returns the path where the shared sync key is stored.
func DefaultKeyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo", "sync.key")
}

// LoadKey returns the sync key from the UNKY_MO_SYNC_KEY env var if set,
// otherwise from the key file. Returns a descriptive error if no key is
// available or the key file has loose permissions.
func LoadKey() (Key, error) {
	var k Key
	if b64 := strings.TrimSpace(os.Getenv(keyEnvVar)); b64 != "" {
		return decodeKey(b64, keyEnvVar)
	}
	path := DefaultKeyPath()
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return k, fmt.Errorf("no sync key found. Run 'mo sync init-key' to create one, or set %s", keyEnvVar)
		}
		return k, fmt.Errorf("stat key file: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return k, fmt.Errorf("sync key file %s has loose permissions (%o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return k, fmt.Errorf("read key file: %w", err)
	}
	return decodeKey(strings.TrimSpace(string(data)), path)
}

func decodeKey(b64, source string) (Key, error) {
	var k Key
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return k, fmt.Errorf("decode key from %s: %w", source, err)
	}
	if len(raw) != len(k) {
		return k, fmt.Errorf("key from %s has wrong length: got %d bytes, want %d", source, len(raw), len(k))
	}
	copy(k[:], raw)
	return k, nil
}

// InitKey generates a new 32-byte random key and writes it (base64-encoded)
// to DefaultKeyPath with mode 0600. Refuses to overwrite an existing key
// unless force is true.
func InitKey(force bool) (string, error) {
	path := DefaultKeyPath()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("key file already exists at %s; pass --force to overwrite", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	var raw [32]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw[:])
	if err := os.WriteFile(path, []byte(b64+"\n"), keyFileMode); err != nil {
		return "", fmt.Errorf("write key file: %w", err)
	}
	return path, nil
}

// ShowKey returns the base64-encoded key for display/copying to another machine.
func ShowKey() (string, error) {
	k, err := LoadKey()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// DirHash returns a deterministic 32-character hex identifier for a project
// name, suitable for use as a directory name on the remote.
func DirHash(key Key, projectName string) string {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(dirHashInfo))
	mac.Write([]byte(projectName))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:dirHashLen])
}

// IsDirHash reports whether name looks like a DirHash output (32 hex chars).
func IsDirHash(name string) bool {
	if len(name) != dirHashLen*2 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// Encrypt seals plaintext with AES-256-GCM using a random nonce. The envelope
// format is: magic(4) || nonce(12) || ciphertext+tag. ad is bound into the
// AEAD authentication and must be supplied unchanged to Decrypt.
func Encrypt(key Key, plaintext, ad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, magicLen+nonceLen+len(plaintext)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, ad)
	return out, nil
}

// Decrypt opens an envelope produced by Encrypt. Returns an error on magic
// mismatch, truncated input, wrong key, tampered ciphertext, or wrong ad.
func Decrypt(key Key, envelope, ad []byte) ([]byte, error) {
	if len(envelope) < magicLen+nonceLen {
		return nil, errors.New("ciphertext too short")
	}
	if string(envelope[:magicLen]) != magic {
		return nil, errors.New("not an unky-mo encrypted file (bad magic)")
	}
	nonce := envelope[magicLen : magicLen+nonceLen]
	ct := envelope[magicLen+nonceLen:]
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, ad)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}

// EncryptFile reads src, encrypts it, and writes the envelope to dst.
func EncryptFile(key Key, srcPath, dstPath string, ad []byte) error {
	plaintext, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	envelope, err := Encrypt(key, plaintext, ad)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, envelope, 0644)
}

// DecryptFile reads an encrypted envelope from src, decrypts it, and writes
// the plaintext to dst. On decryption failure, dst is not written.
func DecryptFile(key Key, srcPath, dstPath string, ad []byte) error {
	envelope, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	pt, err := Decrypt(key, envelope, ad)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, pt, 0644)
}

// AD helpers keep AEAD labels consistent between encrypt and decrypt sites.
func adMeta(dirHash string) []byte    { return []byte("meta:" + dirHash) }
func adSession(dirHash string) []byte { return []byte("session:" + dirHash) }
