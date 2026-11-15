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
	"strings"
	"time"
)

// maxImageBytes bounds one uploaded image, maxFileBytes any other file.
const (
	maxImageBytes = 20 << 20
	maxFileBytes  = 50 << 20
)

// attachmentTTL is how long an upload is kept. Claude Code copies an image
// into the transcript when the prompt is submitted, and reads a file when
// it's asked to, so the upload only has to outlive the conversation turn.
const attachmentTTL = 24 * time.Hour

var (
	// ErrAttachmentTooLarge is returned for an upload over its cap.
	ErrAttachmentTooLarge = fmt.Errorf("too large: images up to %d MB, other files up to %d MB", maxImageBytes>>20, maxFileBytes>>20)
	// ErrEmptyAttachment is returned for an upload with no content.
	ErrEmptyAttachment = errors.New("the file is empty")
)

// attachmentExts maps the sniffed content types that are attached as
// images (the formats Claude Code attaches) to their file extension.
var attachmentExts = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

var (
	imageAttachmentIDRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
	fileAttachmentIDRe  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// safeNameRe is what attachmentName produces; a file's directory must
	// hold exactly one such name.
	safeNameRe  = regexp.MustCompile(`^[A-Za-z0-9+_][A-Za-z0-9._+-]{0,99}$`)
	unsafeRunRe = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)
	windowIDRe  = regexp.MustCompile(`^@[0-9]+$`)
)

// Attachment kinds: an image is pasted as "[Image #N]", a file is named in
// the prompt's text as a path for Claude to read.
const (
	AttachmentImage = "image"
	AttachmentFile  = "file"
)

// Attachment is a stored upload.
type Attachment struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is the stored file name: the cleaned-up upload name for a file,
	// the id for an image.
	Name string `json:"name"`
}

// isImageAttachmentID reports whether id names an image (as opposed to a
// file, whose id has no extension).
func isImageAttachmentID(id string) bool { return imageAttachmentIDRe.MatchString(id) }

// attachmentName turns an upload's name into one safe to type into a pane
// as part of a path: the base name, runs of anything outside
// [A-Za-z0-9._+-] as one "_", no leading dot, dash or underscore, at most
// 100 bytes with the extension kept, "file" when nothing is left.
func attachmentName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = unsafeRunRe.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, "._-")
	if len(name) > 100 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = strings.TrimRight(name[:100-len(ext)], ".") + ext
	}
	if !safeNameRe.MatchString(name) {
		return "file"
	}
	return name
}

// AttachmentStore keeps files uploaded from the chat composer, one
// directory per tmux window, until the prompt that references them is sent.
// An image is stored as <window>/<id>, a file as <window>/<id>/<name>. The
// browser only ever gets and sends back an id; the path that is pasted into
// Claude's pane is always built here.
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

// Save stores one upload for windowID. Whether it's an image is sniffed
// from the content, never taken from the name or a header; name only names
// a file.
func (s *AttachmentStore) Save(windowID, name string, r io.Reader) (Attachment, error) {
	if !windowIDRe.MatchString(windowID) {
		return Attachment{}, fmt.Errorf("invalid window id %q", windowID)
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Attachment{}, err
	}
	head = head[:n]
	if n == 0 {
		return Attachment{}, ErrEmptyAttachment
	}
	s.Sweep()

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Attachment{}, err
	}
	a := Attachment{ID: hex.EncodeToString(raw[:])}
	dir := filepath.Join(s.dir, windowID)
	limit := int64(maxFileBytes)
	if ext, ok := attachmentExts[http.DetectContentType(head)]; ok {
		a.ID += "." + ext
		a.Kind, a.Name, limit = AttachmentImage, a.ID, maxImageBytes
	} else {
		a.Kind, a.Name = AttachmentFile, attachmentName(name)
		dir = filepath.Join(dir, a.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, err
	}
	f, err := os.OpenFile(filepath.Join(dir, a.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Attachment{}, err
	}
	written, err := io.Copy(f, io.LimitReader(io.MultiReader(bytes.NewReader(head), r), limit+1))
	if err == nil && written > limit {
		err = ErrAttachmentTooLarge
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		s.remove(windowID, a.ID)
		return Attachment{}, err
	}
	return a, nil
}

// Path returns the file for an id saved under windowID. ok is false for a
// malformed id, one saved for another window, or one that has expired —
// and always on a nil store.
func (s *AttachmentStore) Path(windowID, id string) (string, bool) {
	if s == nil || !windowIDRe.MatchString(windowID) {
		return "", false
	}
	p := filepath.Join(s.dir, windowID, id)
	switch {
	case isImageAttachmentID(id):
	case fileAttachmentIDRe.MatchString(id):
		// A file's directory holds exactly the one file Save wrote.
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 1 || !safeNameRe.MatchString(entries[0].Name()) {
			return "", false
		}
		p = filepath.Join(p, entries[0].Name())
	default:
		return "", false
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

// Delete removes an attachment; an unknown id is not an error.
func (s *AttachmentStore) Delete(windowID, id string) {
	if _, ok := s.Path(windowID, id); ok {
		s.remove(windowID, id)
	}
}

// remove deletes an id's file, or a file's directory, without checking it.
func (s *AttachmentStore) remove(windowID, id string) {
	_ = os.RemoveAll(filepath.Join(s.dir, windowID, id))
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
