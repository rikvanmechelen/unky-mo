package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestDevices(t *testing.T) (*DeviceStore, *clock, string) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "web-devices.json")
	return NewDeviceStore(path, c.now), c, path
}

func TestDevicePairAndLookup(t *testing.T) {
	s, _, path := newTestDevices(t)
	token, dev, err := s.Pair("Pixel 9 — Chrome", "https://10.0.0.2:7890")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 40 || dev.ID == "" || dev.Label != "Pixel 9 — Chrome" || dev.Origin != "https://10.0.0.2:7890" {
		t.Fatalf("pair = %q, %+v", token, dev)
	}
	got, ok := s.Lookup(token)
	if !ok || got.ID != dev.ID {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
	if _, ok := s.Lookup(token + "x"); ok {
		t.Error("a wrong token was accepted")
	}
	if _, ok := s.Lookup(""); ok {
		t.Error("an empty token was accepted")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Error("the token itself is on disk")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
}

func TestDeviceExpiryAndLastUsed(t *testing.T) {
	s, c, path := newTestDevices(t)
	token, _, _ := s.Pair("phone", "")

	before, _ := os.ReadFile(path)
	c.t = c.t.Add(30 * time.Minute)
	s.Lookup(token)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("lastUsed was written within the hour")
	}

	c.t = c.t.Add(2 * time.Hour)
	dev, _ := s.Lookup(token)
	if !dev.LastUsed.Equal(c.t) {
		t.Errorf("lastUsed = %v, want %v", dev.LastUsed, c.t)
	}

	// Unused for 90 days: gone.
	c.t = c.t.Add(deviceIdle + time.Minute)
	if _, ok := s.Lookup(token); ok {
		t.Error("an expired token was accepted")
	}
	if len(s.List()) != 0 {
		t.Error("the expired device is still listed")
	}
}

func TestDeviceRevokeAndOutsideChanges(t *testing.T) {
	s, c, path := newTestDevices(t)
	token, dev, _ := s.Pair("phone", "")
	other, _, _ := s.Pair("laptop", "")

	// Another process (`mo web devices revoke`) changes the file.
	cli := NewDeviceStore(path, c.now)
	if !cli.Revoke(dev.ID) {
		t.Fatal("Revoke found nothing")
	}
	if _, ok := s.Lookup(token); ok {
		t.Error("a device revoked by another process was still accepted")
	}
	if _, ok := s.Lookup(other); !ok {
		t.Error("the other device was lost")
	}
	if s.Revoke("nope") {
		t.Error("revoking an unknown id reported success")
	}
}

func TestDeviceCap(t *testing.T) {
	s, _, _ := newTestDevices(t)
	for i := 0; i < maxDevices; i++ {
		if _, _, err := s.Pair("d", ""); err != nil {
			t.Fatalf("pair %d: %v", i, err)
		}
	}
	if _, _, err := s.Pair("one too many", ""); err == nil {
		t.Error("pairing past the cap succeeded")
	}
}

// A device list left readable by others (an older version, a dotfiles
// checkout) is made the owner's only when loaded.
func TestDeviceStoreTightensMode(t *testing.T) {
	devices, _, path := newTestDevices(t)
	devices.Pair("x", "")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again := NewDeviceStore(path, nil)
	again.List()
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}
