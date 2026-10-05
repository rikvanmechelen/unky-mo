package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func callsFixture(t *testing.T) (*Server, *mock_web.MockGitFiles, *mock_web.MockCallGrapher) {
	t.Helper()
	srv, git := filesFixture(t)
	cg := mock_web.NewMockCallGrapher(gomock.NewController(t))
	srv.deps.Calls = cg
	return srv, git, cg
}

// /calls builds the call graph of the overview the state row's checkout
// has, once for as long as the change's files look the same.
func TestCallsUsesStatePathAndCaches(t *testing.T) {
	srv, git, calls := callsFixture(t)
	o := branchOverview()
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(o, nil).AnyTimes()
	calls.EXPECT().Calls(o).Return(&review.CallGraph{
		Funcs: []review.Func{{ID: "a.F", Status: review.FuncChanged}},
	}, nil).Times(1)

	rec := get(t, srv, "/api/sessions/@5/calls")
	var got callsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.Repo || len(got.Funcs) != 1 {
		t.Fatalf("calls: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/calls?base=branch", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: got %d", rec.Code)
	}
	// Past any TTL, the same files are still served from the cache.
	srv.callCache.now = func() time.Time { return time.Now().Add(time.Hour) }
	if rec := get(t, srv, "/api/sessions/@5/calls"); rec.Code != http.StatusOK {
		t.Errorf("cached: %d", rec.Code)
	}
}

func TestCallsRejects(t *testing.T) {
	srv, git, _ := callsFixture(t)
	// No Overview/Calls calls for a bad mode or an ended window.
	if rec := get(t, srv, "/api/sessions/@5/calls?base=main"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base: want 400, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@6/calls"); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeHead).Return(nil, gitfiles.ErrNotRepo)
	rec := get(t, srv, "/api/sessions/@5/calls?base=head")
	var got callsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Repo || rec.Code != http.StatusOK {
		t.Errorf("not a repo: %d %s", rec.Code, rec.Body)
	}
}

// A reviewer target that isn't checked out is a commit: its call graph is
// keyed by the target and never recomputed.
func TestBranchCallsRef(t *testing.T) {
	srv, m := branchFixture(t)
	calls := mock_web.NewMockCallGrapher(gomock.NewController(t))
	srv.deps.Calls = calls
	m.git.EXPECT().ResolveBranch("/ws/app", "idle", false).Return(branchHead, nil).Times(1)
	m.git.EXPECT().OverviewAt("/ws/app", "idle", branchHead, "").Return(refOverview(), nil).Times(1)
	calls.EXPECT().Calls(gomock.Any()).Return(&review.CallGraph{}, nil).Times(1)
	for i := 0; i < 2; i++ {
		if rec := get(t, srv, "/api/projects/app/branches/idle/calls"); rec.Code != http.StatusOK {
			t.Fatalf("calls: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := get(t, srv, "/api/projects/app/branches/nope/calls?base=x"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base: %d", rec.Code)
	}
}

// A checkout's fingerprint changes when a changed file is written, and not
// otherwise.
func TestChangeFingerprint(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	if err := os.WriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &gitfiles.Overview{Root: dir, Mode: gitfiles.ModeBranch, Rev: mergeBase, Files: []gitfiles.OverviewFile{{Path: "a.go", Status: "M"}}}
	fp := changeFingerprint(o)
	if changeFingerprint(o) != fp {
		t.Fatal("fingerprint isn't stable")
	}
	if err := os.WriteFile(p, []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changeFingerprint(o) == fp {
		t.Error("a write didn't change the fingerprint")
	}
	// A commit target ignores the disk.
	ref := &gitfiles.Overview{Root: dir, Rev: mergeBase, Head: branchHead, Files: o.Files}
	fp = changeFingerprint(ref)
	_ = os.WriteFile(p, []byte("four!"), 0o644)
	if changeFingerprint(ref) != fp {
		t.Error("a commit target's fingerprint read the disk")
	}
}

func TestFingerprintCache(t *testing.T) {
	c := newFingerprintCache()
	n := 0
	compute := func() ([]byte, error) { n++; return []byte("x"), nil }
	c.get("k", "1", compute)
	c.get("k", "1", compute)
	if n != 1 {
		t.Errorf("same fingerprint computed %d times", n)
	}
	c.get("k", "2", compute)
	if n != 2 {
		t.Errorf("new fingerprint: computed %d times", n)
	}
	// Errors are kept only briefly.
	now := time.Now()
	c.now = func() time.Time { return now }
	fail := func() ([]byte, error) { n++; return nil, errors.New("boom") }
	c.get("e", "1", fail)
	c.get("e", "1", fail)
	if n != 3 {
		t.Errorf("error recomputed within its TTL (%d)", n)
	}
	c.now = func() time.Time { return now.Add(callErrorTTL + time.Second) }
	c.get("e", "1", fail)
	if n != 4 {
		t.Errorf("error not recomputed after its TTL (%d)", n)
	}
	// Bounded.
	for i := 0; i < callCacheSize+5; i++ {
		c.get(string(rune('a'+i)), "1", compute)
	}
	if len(c.items) != callCacheSize {
		t.Errorf("%d entries, want %d", len(c.items), callCacheSize)
	}
}

// Concurrent misses for one key share a computation.
func TestFingerprintCacheSingleFlight(t *testing.T) {
	c := newFingerprintCache()
	release := make(chan struct{})
	var mu sync.Mutex
	n := 0
	compute := func() ([]byte, error) {
		mu.Lock()
		n++
		mu.Unlock()
		<-release
		return []byte("x"), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.get("k", "1", compute) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n != 1 {
		t.Errorf("computed %d times", n)
	}
}
