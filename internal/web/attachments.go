package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// maxAttachmentBytes bounds one uploaded image.
const maxAttachmentBytes = 20 << 20

// attachmentTTL is how long an uploaded image is kept. Claude Code copies
// the image into the transcript when the prompt is submitted, so the file
// only has to outlive the composer it was attached in.
const attachmentTTL = 24 * time.Hour

var (
	// ErrUnsupportedAttachment is returned for anything but a PNG, JPEG,
	// GIF or WebP image — the formats Claude Code attaches.
	ErrUnsupportedAttachment = errors.New("only PNG, JPEG, GIF and WebP images can be attached")
	// ErrAttachmentTooLarge is returned for an image over maxAttachmentBytes.
	ErrAttachmentTooLarge = fmt.Errorf("image is larger than %d MB", maxAttachmentBytes>>20)
)

// attachmentExts maps the sniffed content types that may be attached to
// their file extension.
var attachmentExts = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

var (
	attachmentIDRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
	windowIDRe     = regexp.MustCompile(`^@[0-9]+$`)
)

// AttachmentStore keeps images uploaded from the chat composer, one
// directory per tmux window, until the prompt that references them is sent.
// The browser only ever gets and sends back an id (the file's base name);
// the path that is pasted into Claude's pane is always built here.
type AttachmentStore struct {
	dir string
	now func() time.Time
}

// NewAttachmentStore returns a store under the user's cache directory
// (~/.cache/unky-mo/attachments on Linux) and drops expired files.
func NewAttachmentStore() (*AttachmentStore, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	s := newAttachmentStore(filepath.Join(cache, "unky-mo", "attachments"), time.Now)
	s.Sweep()
	return s, nil
}

func newAttachmentStore(dir string, now func() time.Time) *AttachmentStore {
	return &AttachmentStore{dir: dir, now: now}
}

// Save stores one image for windowID and returns its id. The type is
// sniffed from the content, never taken from a name or header.
func (s *AttachmentStore) Save(windowID string, r io.Reader) (string, error) {
	if !windowIDRe.MatchString(windowID) {
		return "", fmt.Errorf("invalid window id %q", windowID)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxAttachmentBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxAttachmentBytes {
		return "", ErrAttachmentTooLarge
	}
	ext, ok := attachmentExts[http.DetectContentType(data)]
	if !ok {
		return "", ErrUnsupportedAttachment
	}
	s.Sweep()

	dir := filepath.Join(s.dir, windowID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:]) + "." + ext
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return id, nil
}

// Path returns the file for an id saved under windowID. ok is false for a
// malformed id, one saved for another window, or one that has expired —
// and always on a nil store.
func (s *AttachmentStore) Path(windowID, id string) (string, bool) {
	if s == nil || !windowIDRe.MatchString(windowID) || !attachmentIDRe.MatchString(id) {
		return "", false
	}
	p := filepath.Join(s.dir, windowID, id)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

// Delete removes an attachment; an unknown id is not an error.
func (s *AttachmentStore) Delete(windowID, id string) {
	if p, ok := s.Path(windowID, id); ok {
		_ = os.Remove(p)
	}
}

// Sweep deletes attachments older than attachmentTTL, and window
// directories left empty.
func (s *AttachmentStore) Sweep() {
	cutoff := s.now().Add(-attachmentTTL)
	windows, _ := os.ReadDir(s.dir)
	for _, w := range windows {
		if !w.IsDir() {
			continue
		}
		dir := filepath.Join(s.dir, w.Name())
		files, _ := os.ReadDir(dir)
		left := len(files)
		for _, f := range files {
			if fi, err := f.Info(); err == nil && fi.ModTime().Before(cutoff) {
				if os.RemoveAll(filepath.Join(dir, f.Name())) == nil {
					left--
				}
			}
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
}
