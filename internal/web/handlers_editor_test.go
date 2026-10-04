package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func TestSessionFileReadsListedPaths(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go", "sub/b.go"}, nil).Times(1)
	git.EXPECT().ReadFile("/ws/foo", "sub/b.go").Return(&gitfiles.Content{Path: "sub/b.go", Exists: true, Hash: "abc", Text: "b\n"}, nil)
	git.EXPECT().ReadHEAD("/ws/foo", "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "def", Text: "a\n"}, nil)

	rec := get(t, srv, "/api/sessions/@5/file?path=sub/b.go")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var got gitfiles.Content
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Text != "b\n" {
		t.Errorf("unexpected body %s (%v)", rec.Body, err)
	}
	if et := rec.Header().Get("ETag"); et != `"abc"` {
		t.Errorf("ETag: want \"abc\", got %q", et)
	}

	rec = get(t, srv, "/api/sessions/@5/file?path=a.go&rev=HEAD")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"def"` {
		t.Errorf("rev=HEAD: got %d %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// A file deleted from the working tree is no longer in the tree listing,
// but is still readable (from HEAD) because it's in the changes list.
func TestSessionFileFallsBackToChanges(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil)
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo", Files: []gitfiles.File{{Path: "gone.go", Status: "D"}}}, nil)
	git.EXPECT().ReadHEAD("/ws/foo", "gone.go").Return(&gitfiles.Content{Path: "gone.go", Exists: true, Hash: "h"}, nil)

	if rec := get(t, srv, "/api/sessions/@5/file?path=gone.go&rev=HEAD"); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body)
	}
}

// Paths the Files panel doesn't list (ignored files, traversal, absolute
// paths) never reach a read.
func TestSessionFileRejectsUnlistedPaths(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil)
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo"}, nil)
	// No ReadFile/ReadHEAD expectations: gomock fails on any call.
	for _, path := range []string{".env", "../../etc/passwd", "/etc/passwd", "sub/../a.go"} {
		if rec := get(t, srv, "/api/sessions/@5/file?path="+url.QueryEscape(path)); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
	if rec := get(t, srv, "/api/sessions/@5/file"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing path: want 400, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@5/file?path=a.go&rev=HEAD~1"); rec.Code != http.StatusBadRequest {
		t.Errorf("other rev: want 400, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@6/file?path=a.go"); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
}

// A listed path that's a symlink out of the checkout is refused by gitfiles.
func TestSessionFileSymlinkEscape(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"link"}, nil)
	git.EXPECT().ReadFile("/ws/foo", "link").Return(nil, gitfiles.ErrOutsideRoot)
	if rec := get(t, srv, "/api/sessions/@5/file?path=link"); rec.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rec.Code)
	}
}

func TestSessionFileNotModified(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil)
	git.EXPECT().ReadFile("/ws/foo", "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "abc", Text: "a"}, nil).Times(2)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/file?path=a.go", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("matching ETag: want empty 304, got %d %q", rec.Code, rec.Body)
	}

	req.Header.Set("If-None-Match", `"old"`)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("stale ETag: want 200, got %d", rec.Code)
	}
}
