package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func TestSessionLogScopesCacheAndETag(t *testing.T) {
	srv, git := filesFixture(t)
	l := &gitfiles.Log{Root: "/ws/foo", Scope: "branch", Head: "abc", Commits: []gitfiles.Commit{{Hash: "abc", Subject: "hi"}}}
	// The default scope is "branch"; two requests make one git call.
	git.EXPECT().Log("/ws/foo/sub", gitfiles.ScopeBranch).Return(l, nil).Times(1)
	git.EXPECT().Log("/ws/foo/sub", gitfiles.ScopeAll).Return(&gitfiles.Log{Scope: "all", Commits: []gitfiles.Commit{}}, nil).Times(1)

	rec := get(t, srv, "/api/sessions/@5/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got logResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.Repo || len(got.Commits) != 1 || got.Commits[0].Subject != "hi" {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/log?scope=branch", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidation: %d %q", rec.Code, rec.Body)
	}

	if rec := get(t, srv, "/api/sessions/@5/log?scope=all"); rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag {
		t.Errorf("all scope: %d, etag %s", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestSessionLogRefusals(t *testing.T) {
	srv, git := filesFixture(t)
	if rec := get(t, srv, "/api/sessions/@5/log?scope=--all"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad scope: %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@6/log"); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: %d", rec.Code)
	}
	git.EXPECT().Log("/ws/foo/sub", gitfiles.ScopeBranch).Return(nil, gitfiles.ErrNotRepo)
	rec := get(t, srv, "/api/sessions/@5/log")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"repo\":false}\n" {
		t.Errorf("not a repo: %d %s", rec.Code, rec.Body)
	}
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

func TestCommitUsesRepoRootFromState(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo"}, nil)
	git.EXPECT().Commit("/ws/foo", testHash).Return(&gitfiles.CommitDetail{Hash: testHash, Files: []gitfiles.CommitFile{{Path: "a.go", Status: "M"}}}, nil)
	git.EXPECT().CommitFile("/ws/foo", testHash, "a.go").Return(&gitfiles.CommitFileDiff{Path: "a.go", Before: &gitfiles.Content{}, After: &gitfiles.Content{}}, nil)

	if rec := get(t, srv, "/api/sessions/@5/commits/"+testHash); rec.Code != http.StatusOK {
		t.Errorf("commit: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, "/api/sessions/@5/commits/"+testHash+"/file?path=a.go"); rec.Code != http.StatusOK {
		t.Errorf("commit file: %d %s", rec.Code, rec.Body)
	}
}

func TestCommitErrors(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo"}, nil).AnyTimes()
	git.EXPECT().Commit("/ws/foo", "HEAD").Return(nil, gitfiles.ErrUnknownCommit)
	git.EXPECT().CommitFile("/ws/foo", testHash, ".env").Return(nil, gitfiles.ErrNotInCommit)

	if rec := get(t, srv, "/api/sessions/@5/commits/HEAD"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown commit: %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@5/commits/"+testHash+"/file?path=.env"); rec.Code != http.StatusNotFound {
		t.Errorf("path not in commit: %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@5/commits/"+testHash+"/file"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing path: %d", rec.Code)
	}
	// An ended window never reaches git.
	if rec := get(t, srv, "/api/sessions/@6/commits/"+testHash); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: %d", rec.Code)
	}
}
