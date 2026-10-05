package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func annotated(path string) *gitfiles.Annotated {
	return &gitfiles.Annotated{Path: path, Rows: []gitfiles.Row{
		{Ln: 1, OldLn: 1, Sign: " ", Text: "one"},
		{OldLn: 2, Sign: "-", Text: "two"},
		{Ln: 2, Sign: "+", Text: "TWO"},
		{Ln: 3, OldLn: 3, Sign: " ", Text: "three"},
	}}
}

func decodeExcerpt(t *testing.T, rec *httptest.ResponseRecorder) gitfiles.Excerpt {
	t.Helper()
	var e gitfiles.Excerpt
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// Malformed requests are refused before any git call (the mocks expect
// none).
func TestExcerptRefusesBadParams(t *testing.T) {
	srv, _ := filesFixture(t)
	for _, q := range []string{
		"line=2", "path=a.go", "path=a.go&line=0", "path=a.go&line=x", "path=a.go&line=-3",
		"path=a.go&line=5&end=4", "path=a.go&line=5&end=406", "path=a.go&line=2&ctx=21",
		"path=a.go&line=2&ctx=-1", "path=a.go&line=2&side=both", "path=a.go&line=2&base=nope",
		"path=a.go&line=2&base=commits&commits=HEAD",
	} {
		if rec := get(t, srv, "/api/sessions/@5/excerpt?"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := get(t, srv, "/api/sessions/@99/excerpt?path=a.go&line=1"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown window: %d", rec.Code)
	}
}

func TestExcerptSession(t *testing.T) {
	srv, git := filesFixture(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("one\nTWO\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &gitfiles.Overview{Root: root, Mode: gitfiles.ModeBranch, Rev: mergeBase, MergeBase: mergeBase, Files: []gitfiles.OverviewFile{
		{Path: "a.go", Status: "M"},
		{Path: "new.go", OldPath: "old.go", Status: "R"},
	}}
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(o, nil).AnyTimes()
	// One annotation per version of a.go: the second request is cached,
	// the edit below makes a third read it again.
	git.EXPECT().Annotate(o, "a.go").Return(annotated("a.go"), nil).Times(2)

	rec := get(t, srv, "/api/sessions/@5/excerpt?path=a.go&line=2&ctx=0")
	if e := decodeExcerpt(t, rec); e.Side != gitfiles.SideNew || !reflect.DeepEqual(e.Rows, []gitfiles.Row{{Ln: 2, Sign: "+", Text: "TWO"}}) {
		t.Errorf("new side: %+v", e)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/excerpt?path=a.go&line=2&ctx=0", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec304 := httptest.NewRecorder()
	srv.ServeHTTP(rec304, req)
	if rec304.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec304.Code)
	}
	if e := decodeExcerpt(t, get(t, srv, "/api/sessions/@5/excerpt?path=a.go&line=2&side=old&ctx=1")); len(e.Rows) != 3 || e.Rows[1].Text != "two" {
		t.Errorf("old side: %+v", e)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("one\nTWO\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decodeExcerpt(t, get(t, srv, "/api/sessions/@5/excerpt?path=a.go&line=1"))

	// A rename's old name is listed by the change itself: no tree lookup.
	git.EXPECT().Annotate(o, "old.go").Return(annotated("old.go"), nil)
	if e := decodeExcerpt(t, get(t, srv, "/api/sessions/@5/excerpt?path=old.go&line=2&side=old&ctx=0")); e.Path != "old.go" || e.Rows[0].Text != "two" {
		t.Errorf("rename's old path: %+v", e)
	}

	// An unchanged file must be in the checkout's tree; .env never is.
	git.EXPECT().Tree("/ws/foo/sub").Return(root, []string{"a.go", "lib/util.go", "new.go"}, nil).AnyTimes()
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: root}, nil).AnyTimes()
	git.EXPECT().Annotate(o, "lib/util.go").Return(annotated("lib/util.go"), nil)
	decodeExcerpt(t, get(t, srv, "/api/sessions/@5/excerpt?path=lib/util.go&line=1"))
	for _, p := range []string{".env", "../x", "/etc/passwd"} {
		if rec := get(t, srv, "/api/sessions/@5/excerpt?line=1&path="+p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}

	// gitfiles' own symlink check is a 403.
	git.EXPECT().Annotate(o, "new.go").Return(nil, gitfiles.ErrOutsideRoot)
	if rec := get(t, srv, "/api/sessions/@5/excerpt?path=new.go&line=1"); rec.Code != http.StatusForbidden {
		t.Errorf("outside root: %d", rec.Code)
	}
}

// Uncommitted mode reads the head-mode overview.
func TestExcerptHeadMode(t *testing.T) {
	srv, git := filesFixture(t)
	o := &gitfiles.Overview{Root: "/ws/foo", Mode: gitfiles.ModeHead, Rev: mergeBase, Files: []gitfiles.OverviewFile{{Path: "a.go", Status: "M"}}}
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeHead).Return(o, nil)
	git.EXPECT().Annotate(o, "a.go").Return(annotated("a.go"), nil)
	if e := decodeExcerpt(t, get(t, srv, "/api/sessions/@5/excerpt?base=head&path=a.go&line=3&ctx=0")); len(e.Rows) != 1 || e.Rows[0].Text != "three" {
		t.Errorf("%+v", e)
	}
}

// A ref target lists its head commit's tree; its files never change, so
// one annotation serves every request.
func TestBranchExcerptRef(t *testing.T) {
	srv, m := branchFixture(t)
	o := refOverview()
	m.git.EXPECT().ResolveBranch("/ws/app", "idle", false).Return(branchHead, nil).AnyTimes()
	m.git.EXPECT().OverviewAt("/ws/app", "idle", branchHead, "").Return(o, nil).AnyTimes()
	m.git.EXPECT().TreeAt("/ws/app", branchHead).Return([]string{"a.go", "lib/util.go", "new.go"}, nil).Times(1)
	m.git.EXPECT().Annotate(o, "lib/util.go").Return(annotated("lib/util.go"), nil).Times(1)
	m.git.EXPECT().Annotate(o, "gone.go").Return(annotated("gone.go"), nil).Times(1)

	for range 2 {
		decodeExcerpt(t, get(t, srv, "/api/projects/app/branches/idle/excerpt?path=lib/util.go&line=1"))
	}
	if e := decodeExcerpt(t, get(t, srv, "/api/projects/app/branches/idle/excerpt?path=gone.go&line=2&side=old&ctx=0")); e.Rows[0].Text != "two" {
		t.Errorf("deleted file: %+v", e)
	}
	for _, p := range []string{".env", "../x"} {
		if rec := get(t, srv, "/api/projects/app/branches/idle/excerpt?line=1&path="+p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
	if rec := get(t, srv, "/api/projects/app/branches/idle/excerpt?line=1&path=a.go&base=commits"); rec.Code != http.StatusBadRequest {
		t.Errorf("reviewer view has no selection mode: %d", rec.Code)
	}
}
