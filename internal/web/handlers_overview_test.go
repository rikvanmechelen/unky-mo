package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

const mergeBase = "0123456789abcdef0123456789abcdef01234567"

func branchOverview() *gitfiles.Overview {
	return &gitfiles.Overview{
		Root: "/ws/foo", Mode: gitfiles.ModeBranch, Base: "main", MergeBase: mergeBase,
		Files: []gitfiles.OverviewFile{
			{Path: "a.go", Status: "M", Added: 3, Kind: gitfiles.KindLogic, Area: "."},
			{Path: "sub/new.go", OldPath: "sub/old.go", Status: "R", Kind: gitfiles.KindRenamed, Area: "sub"},
			{Path: "gone.go", Status: "D", Removed: 4, Kind: gitfiles.KindLogic, Area: "."},
		},
	}
}

func TestOverviewUsesStatePathAndCaches(t *testing.T) {
	srv, git := filesFixture(t)
	// One git read per mode across repeated polls.
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(branchOverview(), nil).Times(1)
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeHead).Return(&gitfiles.Overview{Root: "/ws/foo", Mode: gitfiles.ModeHead}, nil).Times(1)

	rec := get(t, srv, "/api/sessions/@5/overview")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.Repo || got.MergeBase != mergeBase || len(got.Files) != 3 {
		t.Fatalf("unexpected body %s (%v)", rec.Body, err)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/overview?base=branch", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: got %d with %d bytes", rec.Code, rec.Body.Len())
	}

	for i := 0; i < 2; i++ {
		if rec := get(t, srv, "/api/sessions/@5/overview?base=head"); rec.Code != http.StatusOK {
			t.Errorf("head: got %d", rec.Code)
		}
	}
}

func TestOverviewRejects(t *testing.T) {
	srv, git := filesFixture(t)
	// No Overview call for a bad mode or a window without a live session.
	if rec := get(t, srv, "/api/sessions/@5/overview?base=HEAD~3"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base: want 400, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@6/overview"); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(nil, gitfiles.ErrNotRepo)
	rec := get(t, srv, "/api/sessions/@5/overview")
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Repo || rec.Code != http.StatusOK {
		t.Errorf("not a repo: %d %s", rec.Code, rec.Body)
	}
}

// rev=base reads the server's merge base, under the old name for a rename.
func TestSessionFileBaseReadsMergeBase(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go", "sub/new.go"}, nil)
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(branchOverview(), nil).Times(1)
	git.EXPECT().ReadAt("/ws/foo", mergeBase, "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "h1"}, nil)
	git.EXPECT().ReadAt("/ws/foo", mergeBase, "sub/old.go").Return(&gitfiles.Content{Path: "sub/old.go", Exists: true, Hash: "h2"}, nil)

	if rec := get(t, srv, "/api/sessions/@5/file?path=a.go&rev=base"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"h1"` {
		t.Errorf("a.go: %d %s", rec.Code, rec.Body)
	}
	rec := get(t, srv, "/api/sessions/@5/file?path=sub/new.go&rev=base")
	var got gitfiles.Content
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Path != "sub/new.go" || got.Hash != "h2" {
		t.Errorf("rename: %d %s", rec.Code, rec.Body)
	}
}

// Without a merge base (the overview fell back), the base side is HEAD.
func TestSessionFileBaseFallsBackToHEAD(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil)
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(&gitfiles.Overview{Root: "/ws/foo", Mode: gitfiles.ModeHead, Fallback: true}, nil)
	git.EXPECT().ReadHEAD("/ws/foo", "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "h"}, nil)
	if rec := get(t, srv, "/api/sessions/@5/file?path=a.go&rev=base"); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body)
	}
}

// A file deleted in one of the branch's commits is in neither the tree nor
// the uncommitted changes, but the branch overview lists it.
func TestSessionFileListedByBranchOverview(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil)
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo"}, nil)
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(branchOverview(), nil)
	git.EXPECT().ReadAt("/ws/foo", mergeBase, "gone.go").Return(&gitfiles.Content{Path: "gone.go", Exists: true, Hash: "h"}, nil)
	if rec := get(t, srv, "/api/sessions/@5/file?path=gone.go&rev=base"); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body)
	}
}

func archFixture(t *testing.T) (*Server, *mock_web.MockGitFiles, *mock_web.MockChangeAnalyzer) {
	t.Helper()
	srv, git := filesFixture(t)
	an := mock_web.NewMockChangeAnalyzer(gomock.NewController(t))
	srv.deps.Review = an
	return srv, git, an
}

// /architecture analyzes the same cached overview /overview serves, and
// caches its own result per mode.
func TestArchitectureSharesOverviewAndCaches(t *testing.T) {
	srv, git, an := archFixture(t)
	o := branchOverview()
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(o, nil).Times(1)
	an.EXPECT().Analyze(o).Return(&review.Analysis{
		Module: "example.com/m", Violations: 1,
		Edges: []review.Edge{{From: "a", To: "c", Op: review.OpAdded, Violation: "core may only import b"}},
	}, nil).Times(1)

	if rec := get(t, srv, "/api/sessions/@5/overview"); rec.Code != http.StatusOK {
		t.Fatalf("overview: %d", rec.Code)
	}
	rec := get(t, srv, "/api/sessions/@5/architecture")
	var got architectureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.Repo || got.Violations != 1 || len(got.Edges) != 1 {
		t.Fatalf("architecture: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/architecture?base=branch", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: got %d", rec.Code)
	}
}

func TestArchitectureRejects(t *testing.T) {
	srv, git, _ := archFixture(t)
	// No Overview/Analyze calls for a bad mode or an ended window.
	if rec := get(t, srv, "/api/sessions/@5/architecture?base=main"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base: want 400, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@6/architecture"); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeHead).Return(nil, gitfiles.ErrNotRepo)
	rec := get(t, srv, "/api/sessions/@5/architecture?base=head")
	var got architectureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Repo || rec.Code != http.StatusOK {
		t.Errorf("not a repo: %d %s", rec.Code, rec.Body)
	}
}

// Fetch updates the overview's base and drops the cached overviews.
func TestFetchBase(t *testing.T) {
	srv, git := filesFixture(t)
	o := branchOverview()
	o.Base = "origin/main"
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(o, nil).Times(2) // re-read after the fetch
	git.EXPECT().FetchBase("/ws/foo", "origin/main").Return(nil)
	if rec := post(t, srv, "/api/sessions/@5/fetch-base", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, "/api/sessions/@5/overview"); rec.Code != http.StatusOK {
		t.Errorf("overview after fetch: %d", rec.Code)
	}
	if rec := post(t, srv, "/api/sessions/@6/fetch-base", ""); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: %d", rec.Code)
	}
}
