package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveStatic(t *testing.T, srv *Server, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestStaticServesVendoredBundleGzipped(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")

	plain := serveStatic(t, srv, "/vendor/codemirror.js", nil)
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("plain: got %d, encoding %q", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	if ct := plain.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type: want javascript, got %q", ct)
	}

	gz := serveStatic(t, srv, "/vendor/codemirror.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Body.Len() >= plain.Body.Len() {
		t.Fatalf("gzip: encoding %q, %d >= %d bytes", gz.Header().Get("Content-Encoding"), gz.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(zr)
	if !bytes.Equal(unzipped, plain.Body.Bytes()) {
		t.Error("gzipped body doesn't decode to the plain body")
	}
}

func TestStaticRevalidatesByETag(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	for _, path := range []string{"/", "/chat", "/style.css"} {
		first := serveStatic(t, srv, path, nil)
		etag := first.Header().Get("ETag")
		if first.Code != http.StatusOK || etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: got %d, ETag %q, Cache-Control %q", path, first.Code, etag, first.Header().Get("Cache-Control"))
		}
		again := serveStatic(t, srv, path, map[string]string{"If-None-Match": etag})
		if again.Code != http.StatusNotModified {
			t.Errorf("%s: want 304 for a matching ETag, got %d", path, again.Code)
		}
	}
	if rec := serveStatic(t, srv, "/nope.js", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing file: want 404, got %d", rec.Code)
	}
	// Only embedded static files are served, never package sources.
	if rec := serveStatic(t, srv, "/embed.go", nil); rec.Code != http.StatusNotFound {
		t.Errorf("non-asset: want 404, got %d", rec.Code)
	}
}
