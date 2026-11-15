package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Devices log in once with the password and get a token (docs/plans/pwa.md
// step 3), so the installed app never keeps the password. Only a hash of
// each token is stored, and each device can be revoked on its own.

const (
	maxDevices       = 64
	deviceIdle       = 90 * 24 * time.Hour // a token unused this long expires
	deviceTouchEvery = time.Hour           // how often lastUsed is written
	devicesFileMode  = 0600
)

// Device is one paired browser.
type Device struct {
	ID       string    `json:"id"`
	Hash     string    `json:"hash"` // SHA-256 of the token, hex
	Label    string    `json:"label"`
	Origin   string    `json:"origin,omitempty"` // the page that paired it
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
}

// DeviceStore keeps the paired devices in a 0600 JSON file. It re-reads
// the file when another process (`mo web devices revoke`) changed it.
type DeviceStore struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	devices []Device
	stamp   fileStamp // of the file as last read or written
	key     []byte    // the ticket signing key, once loaded
}

type fileStamp struct {
	size  int64
	mtime time.Time
	ok    bool
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime(), ok: true}
}

// NewDeviceStore opens the store at path; now is injectable for tests.
func NewDeviceStore(path string, now func() time.Time) *DeviceStore {
	if now == nil {
		now = time.Now
	}
	return &DeviceStore{path: path, now: now}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// load re-reads the file if it changed since the last read or write.
// Called with mu held. A missing or unreadable file is an empty store.
func (s *DeviceStore) load() {
	st := stampOf(s.path)
	if st == s.stamp && s.devices != nil {
		return
	}
	s.stamp = st
	s.devices = []Device{}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	keepPrivate(s.path, 0o600)
	var list []Device
	if json.Unmarshal(raw, &list) == nil {
		s.devices = list
	}
}

// save writes the store atomically. Called with mu held.
func (s *DeviceStore) save() error {
	raw, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".web-devices-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(devicesFileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.stamp = stampOf(s.path)
	return nil
}

// prune drops devices idle past deviceIdle. Reports whether any went.
// Called with mu held.
func (s *DeviceStore) prune(now time.Time) bool {
	kept := s.devices[:0]
	for _, d := range s.devices {
		if now.Sub(d.LastUsed) < deviceIdle {
			kept = append(kept, d)
		}
	}
	dropped := len(kept) != len(s.devices)
	s.devices = kept
	return dropped
}

// Pair makes a token for a new device. origin is the page that asked.
func (s *DeviceStore) Pair(label, origin string) (string, Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	now := s.now()
	s.prune(now)
	if len(s.devices) >= maxDevices {
		return "", Device{}, errors.New("too many paired devices; revoke some with 'mo web devices revoke'")
	}
	raw := make([]byte, 32)
	idRaw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", Device{}, err
	}
	if _, err := rand.Read(idRaw); err != nil {
		return "", Device{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	d := Device{ID: hex.EncodeToString(idRaw), Hash: hashToken(token), Label: label, Origin: origin, Created: now, LastUsed: now}
	s.devices = append(s.devices, d)
	if err := s.save(); err != nil {
		s.devices = s.devices[:len(s.devices)-1]
		return "", Device{}, err
	}
	return token, d, nil
}

// Lookup returns the device a token belongs to.
func (s *DeviceStore) Lookup(token string) (Device, bool) {
	d, _, ok := s.lookup(token)
	return d, ok
}

// lookup also reports whether lastUsed was just written, which is when the
// login cookie is renewed.
func (s *DeviceStore) lookup(token string) (Device, bool, bool) {
	if token == "" {
		return Device{}, false, false
	}
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	now := s.now()
	if s.prune(now) {
		_ = s.save()
	}
	for i := range s.devices {
		d := &s.devices[i]
		if d.Hash != h {
			continue
		}
		touched := false
		if now.Sub(d.LastUsed) >= deviceTouchEvery {
			d.LastUsed = now
			touched = s.save() == nil
		}
		return *d, touched, true
	}
	return Device{}, false, false
}

// List returns the paired devices, oldest first.
func (s *DeviceStore) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.prune(s.now()) {
		_ = s.save()
	}
	return append([]Device(nil), s.devices...)
}

// Revoke removes a device. Reports whether it existed.
func (s *DeviceStore) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	for i, d := range s.devices {
		if d.ID == id {
			s.devices = append(s.devices[:i], s.devices[i+1:]...)
			return s.save() == nil
		}
	}
	return false
}

// keepPrivate takes group and other permissions off a secret file or
// directory that has them (written by an older version, or copied with a
// dotfiles checkout). Nothing happens when it doesn't exist.
func keepPrivate(path string, mode os.FileMode) {
	info, err := os.Lstat(path)
	if err != nil || !(info.Mode().IsRegular() || info.IsDir()) {
		return // missing, or a symlink: not followed
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(path, mode)
	}
}
