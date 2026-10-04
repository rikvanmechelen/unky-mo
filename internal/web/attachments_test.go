package web

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pngHeader = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func TestAttachmentStoreSavesSniffedImages(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	for body, ext := range map[string]string{
		pngHeader:                      ".png",
		"\xff\xd8\xff\xe0rest":         ".jpg",
		"GIF89a rest":                  ".gif",
		"RIFF\x00\x00\x00\x00WEBPVP8 ": ".webp",
	} {
		id, err := s.Save("@3", strings.NewReader(body))
		if err != nil {
			t.Fatalf("Save(%q): %v", ext, err)
		}
		if !strings.HasSuffix(id, ext) {
			t.Errorf("id %q: want extension %s", id, ext)
		}
		p, ok := s.Path("@3", id)
		if !ok {
			t.Fatalf("Path(%q) not found", id)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("stored %q, want %q", got, body)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

// The type comes from the content: text, HTML and SVG are refused whatever
// they're called.
func TestAttachmentStoreRejectsNonImages(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	for _, body := range []string{
		"just text",
		"<html><script>alert(1)</script></html>",
		`<svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		"",
	} {
		if _, err := s.Save("@1", strings.NewReader(body)); !errors.Is(err, ErrUnsupportedAttachment) {
			t.Errorf("Save(%q): want ErrUnsupportedAttachment, got %v", body, err)
		}
	}
}

func TestAttachmentStoreRejectsOversizedImages(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	big := bytes.NewReader(append([]byte(pngHeader), make([]byte, maxAttachmentBytes)...))
	if _, err := s.Save("@1", big); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("want ErrAttachmentTooLarge, got %v", err)
	}
}

func TestAttachmentStorePathRefusesForeignAndMalformedIDs(t *testing.T) {
	dir := t.TempDir()
	s := newAttachmentStore(dir, time.Now)
	id, err := s.Save("@1", strings.NewReader(pngHeader))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.png"), []byte(pngHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ window, id string }{
		{"@2", id},              // saved for another window
		{"@1", "../secret.png"}, // traversal
		{"@1", filepath.Join(dir, "secret.png")},
		{"@1", strings.ToUpper(id)}, // not the id format
		{"@1", "0123456789abcdef0123456789abcdef.svg"},
		{"..", id}, // not a window id
		{"@1", "0123456789abcdef0123456789abcdef.png"}, // well-formed, never saved
	} {
		if p, ok := s.Path(c.window, c.id); ok {
			t.Errorf("Path(%q, %q) = %q, want refused", c.window, c.id, p)
		}
	}
	if _, err := s.Save("../x", strings.NewReader(pngHeader)); err == nil {
		t.Error("Save with a malformed window id: want an error")
	}
	var nilStore *AttachmentStore
	if _, ok := nilStore.Path("@1", id); ok {
		t.Error("nil store: want not found")
	}
}

func TestAttachmentStoreDelete(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	id, _ := s.Save("@1", strings.NewReader(pngHeader))
	s.Delete("@1", id)
	if _, ok := s.Path("@1", id); ok {
		t.Error("still there after Delete")
	}
	s.Delete("@1", id) // unknown id: no-op
}

func TestAttachmentStoreSweepDropsOnlyExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := newAttachmentStore(dir, func() time.Time { return now })
	oldID, _ := s.Save("@1", strings.NewReader(pngHeader))
	newID, _ := s.Save("@2", strings.NewReader(pngHeader))
	oldPath, _ := s.Path("@1", oldID)
	stale := now.Add(-attachmentTTL - time.Minute)
	if err := os.Chtimes(oldPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	s.Sweep()
	if _, ok := s.Path("@1", oldID); ok {
		t.Error("expired attachment kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "@1")); !os.IsNotExist(err) {
		t.Error("empty window directory kept")
	}
	if _, ok := s.Path("@2", newID); !ok {
		t.Error("fresh attachment swept")
	}
}
