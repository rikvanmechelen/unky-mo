package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// embeddedStatic prepares the embedded files once per process (hashing and
// gzipping them isn't free, and every Server serves the same files).
var embeddedStatic = sync.OnceValue(func() *staticHandler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil
	}
	h, err := newStaticHandler(sub)
	if err != nil {
		return nil
	}
	return h
})

// staticAsset is one embedded file, prepared once at startup: its content
// hash as an ETag, and a gzipped copy when that's worth sending.
type staticAsset struct {
	name string
	raw  []byte
	gz   []byte
	etag string
}

// staticHandler serves the embedded dashboard files. Embedded files have no
// modification time, so http.FileServer can't answer conditional requests;
// a content-hash ETag plus "no-cache" lets the browser revalidate each load
// with a cheap 304, and still picks up a new binary's files immediately.
// Text files are served gzipped to clients that accept it — the vendored
// editor bundle is ~720 KB raw, ~260 KB gzipped, which matters on a phone.
type staticHandler struct {
	assets map[string]*staticAsset
}

func newStaticHandler(fsys fs.FS) (*staticHandler, error) {
	h := &staticHandler{assets: map[string]*staticAsset{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		a := &staticAsset{name: p, raw: raw, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		if compressible(p) && len(raw) > 1024 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(raw)
			_ = zw.Close()
			if buf.Len() < len(raw) {
				a.gz = buf.Bytes()
			}
		}
		h.assets[p] = a
		return nil
	})
	return h, err
}

func compressible(name string) bool {
	switch path.Ext(name) {
	case ".js", ".css", ".html", ".svg", ".json", ".txt":
		return true
	}
	return false
}

// serve writes the named asset, or 404s.
func (h *staticHandler) serve(w http.ResponseWriter, r *http.Request, name string) {
	a, ok := h.assets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("ETag", a.etag)
	hdr.Set("Cache-Control", "no-cache")
	body := a.raw
	if a.gz != nil {
		hdr.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			hdr.Set("Content-Encoding", "gzip")
			body = a.gz
		}
	}
	// ServeContent sets Content-Type from the name and answers
	// If-None-Match against the ETag set above.
	http.ServeContent(w, r, a.name, time.Time{}, bytes.NewReader(body))
}

// ServeHTTP maps a URL path onto the embedded files; "/" is index.html.
func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	h.serve(w, r, name)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.TrimSpace(enc) == "gzip" && strings.ReplaceAll(strings.TrimSpace(q), " ", "") != "q=0" {
			return true
		}
	}
	return false
}
