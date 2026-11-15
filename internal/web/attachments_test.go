package web

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pngHeader = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// saveID stores body for windowID and returns its id.
func saveID(t *testing.T, s *AttachmentStore, windowID string, body io.Reader) string {
	t.Helper()
	a, err := s.Save(windowID, "upload", body)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// saveFile stores a non-image upload under name and returns its id.
func saveFile(t *testing.T, s *AttachmentStore, windowID, name, body string) string {
	t.Helper()
	a, err := s.Save(windowID, name, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != AttachmentFile {
		t.Fatalf("%s: kind %q, want file", name, a.Kind)
	}
	return a.ID
}

func TestAttachmentStoreSavesSniffedImages(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	for body, ext := range map[string]string{
		pngHeader:                      ".png",
		"\xff\xd8\xff\xe0rest":         ".jpg",
		"GIF89a rest":                  ".gif",
		"RIFF\x00\x00\x00\x00WEBPVP8 ": ".webp",
	} {
		a, err := s.Save("@3", "photo.txt", strings.NewReader(body))
		if err != nil {
			t.Fatalf("Save(%q): %v", ext, err)
		}
		id := a.ID
		if a.Kind != AttachmentImage || a.Name != id {
			t.Errorf("%s: got %+v, want an image named by its id", ext, a)
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

// Anything that doesn't sniff as one of the four images is a file, stored
// under its cleaned-up name, whatever it's called.
func TestAttachmentStoreSavesOtherContentAsFiles(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	for _, c := range []struct{ name, body, want string }{
		{"notes.txt", "just text", "notes.txt"},
		{"page.html", "<html><script>alert(1)</script></html>", "page.html"},
		{"logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, "logo.svg"},
		{"fake.png", "not a png", "fake.png"},
		{"C:\\Users\\me\\My Report (final).pdf", "%PDF-1.7", "My_Report_final_.pdf"},
		{"../../etc/passwd", "root:x:0:0", "passwd"},
		{".env", "SECRET=1", "env"},
		{"$(rm -rf ~);.sh", "echo", "rm_-rf_.sh"},
		{"日本語.txt", "text", "txt"},
		{"", "text", "file"},
		{"...", "text", "file"},
	} {
		a, err := s.Save("@1", c.name, strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("Save(%q): %v", c.name, err)
		}
		if a.Kind != AttachmentFile || a.Name != c.want {
			t.Errorf("Save(%q) = %+v, want a file named %q", c.name, a, c.want)
		}
		p, ok := s.Path("@1", a.ID)
		if !ok || filepath.Base(p) != c.want {
			t.Fatalf("Path(%q) = %q, %v", a.ID, p, ok)
		}
		if got, _ := os.ReadFile(p); string(got) != c.body {
			t.Errorf("stored %q, want %q", got, c.body)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestAttachmentNameCapsLengthKeepingExtension(t *testing.T) {
	got := attachmentName(strings.Repeat("a", 300) + ".tar.gz")
	if len(got) != 100 || !strings.HasSuffix(got, ".gz") {
		t.Errorf("got %q (%d bytes)", got, len(got))
	}
}

func TestAttachmentStoreRejectsEmpty(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	if _, err := s.Save("@1", "x.txt", strings.NewReader("")); !errors.Is(err, ErrEmptyAttachment) {
		t.Fatalf("want ErrEmptyAttachment, got %v", err)
	}
}

// An image over its cap and a file over its (larger) cap are refused and
// leave nothing behind.
func TestAttachmentStoreRejectsOversized(t *testing.T) {
	dir := t.TempDir()
	s := newAttachmentStore(dir, time.Now)
	big := bytes.NewReader(append([]byte(pngHeader), make([]byte, maxImageBytes)...))
	if _, err := s.Save("@1", "x.png", big); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("image: want ErrAttachmentTooLarge, got %v", err)
	}
	// A file may be bigger than an image.
	saveFile(t, s, "@1", "ok.bin", "x"+string(make([]byte, maxImageBytes)))
	huge := bytes.NewReader(append([]byte("x"), make([]byte, maxFileBytes)...))
	if _, err := s.Save("@1", "big.bin", huge); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("file: want ErrAttachmentTooLarge, got %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "@1"))
	if len(entries) != 1 {
		t.Errorf("want only the accepted file left, got %d entries", len(entries))
	}
}

// A file's directory must hold exactly one regular file with a safe name.
func TestAttachmentStoreFilePathRefusesTamperedDirs(t *testing.T) {
	dir := t.TempDir()
	s := newAttachmentStore(dir, time.Now)
	second := saveFile(t, s, "@1", "a.txt", "a")
	if err := os.WriteFile(filepath.Join(dir, "@1", second, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := saveFile(t, s, "@1", "c.txt", "c")
	lp := filepath.Join(dir, "@1", link, "c.txt")
	os.Remove(lp)
	if err := os.Symlink("/etc/passwd", lp); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{second, link} {
		if p, ok := s.Path("@1", id); ok {
			t.Errorf("Path(%q) = %q, want refused", id, p)
		}
	}
}

func TestAttachmentStorePathRefusesForeignAndMalformedIDs(t *testing.T) {
	dir := t.TempDir()
	s := newAttachmentStore(dir, time.Now)
	id := saveID(t, s, "@1", strings.NewReader(pngHeader))
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
	if _, err := s.Save("../x", "x.png", strings.NewReader(pngHeader)); err == nil {
		t.Error("Save with a malformed window id: want an error")
	}
	var nilStore *AttachmentStore
	if _, ok := nilStore.Path("@1", id); ok {
		t.Error("nil store: want not found")
	}
}

func TestAttachmentStoreDelete(t *testing.T) {
	s := newAttachmentStore(t.TempDir(), time.Now)
	id := saveID(t, s, "@1", strings.NewReader(pngHeader))
	s.Delete("@1", id)
	if _, ok := s.Path("@1", id); ok {
		t.Error("still there after Delete")
	}
	s.Delete("@1", id) // unknown id: no-op

	file := saveFile(t, s, "@1", "a.txt", "a")
	s.Delete("@1", file)
	if _, err := os.Stat(filepath.Join(s.dir, "@1", file)); !os.IsNotExist(err) {
		t.Error("a file's directory is still there after Delete")
	}
}

func TestAttachmentStoreSweepDropsOnlyExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := newAttachmentStore(dir, func() time.Time { return now })
	oldID := saveID(t, s, "@1", strings.NewReader(pngHeader))
	newID := saveID(t, s, "@2", strings.NewReader(pngHeader))
	oldFile := saveFile(t, s, "@1", "old.txt", "old")
	newFile := saveFile(t, s, "@2", "new.txt", "new")
	oldPath, _ := s.Path("@1", oldID)
	stale := now.Add(-attachmentTTL - time.Minute)
	for _, p := range []string{oldPath, filepath.Join(dir, "@1", oldFile)} {
		if err := os.Chtimes(p, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	s.Sweep()
	if _, ok := s.Path("@1", oldID); ok {
		t.Error("expired attachment kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "@1")); !os.IsNotExist(err) {
		t.Error("empty window directory kept")
	}
	for _, id := range []string{newID, newFile} {
		if _, ok := s.Path("@2", id); !ok {
			t.Errorf("fresh attachment %s swept", id)
		}
	}
}
