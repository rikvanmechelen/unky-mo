package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// filesFixture returns a server whose state file has a live session at @5
// (cwd /ws/foo/sub), an ended row at @6 and a stray at @7 whose branch the
// TUI already recorded.
func filesFixture(t *testing.T) (*Server, *mock_web.MockGitFiles) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().DoAndReturn(func() (*state.StateFile, error) {
		return &state.StateFile{Projects: []state.ProjectState{
			{Name: "foo", WindowID: "@5", SessionID: "s5", Path: "/ws/foo/sub", Status: "idle"},
			{Name: "bar", WindowID: "@6", Path: "/ws/bar", Status: "none"},
			{Name: "stray", WindowID: "@7", SessionID: "s7", Path: "/ws/stray", Branch: "fix-it", Status: "idle"},
		}}, nil
	}).AnyTimes()
	git := mock_web.NewMockGitFiles(ctrl)
	return NewServer(Deps{State: st, Git: git}, 0, "test"), git
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSessionFilesUsesStatePathAndCaches(t *testing.T) {
	srv, git := filesFixture(t)
	changes := &gitfiles.Changes{
		Root:  "/ws/foo",
		Files: []gitfiles.File{{Path: "a.go", Status: "M", Added: 2, Removed: 1}},
		Added: 2, Removed: 1,
		Sync: gitfiles.Sync{Branch: "main", Upstream: "origin/main", Ahead: 1},
	}
	// Two requests, one git call: the second is served from the cache.
	git.EXPECT().Changes("/ws/foo/sub").Return(changes, nil).Times(1)

	for i := 0; i < 2; i++ {
		rec := get(t, srv, "/api/sessions/@5/files")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body)
		}
		var got struct {
			Repo bool `json:"repo"`
			gitfiles.Changes
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !got.Repo || len(got.Files) != 1 || got.Files[0].Path != "a.go" || got.Sync.Ahead != 1 {
			t.Fatalf("unexpected body: %s", rec.Body)
		}
	}
}

func TestSessionFilesUnknownOrEndedWindow(t *testing.T) {
	srv, _ := filesFixture(t) // no GitFiles expectations: any call fails
	for _, path := range []string{"/api/sessions/@9/files", "/api/sessions/@6/files", "/api/sessions/@6/tree"} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

func TestSessionFilesNotARepo(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Changes("/ws/foo/sub").Return(nil, gitfiles.ErrNotRepo)

	rec := get(t, srv, "/api/sessions/@5/files")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "{\"repo\":false}\n" {
		t.Errorf("body: want {\"repo\":false}, got %q", body)
	}
}

func TestSessionTree(t *testing.T) {
	srv, git := filesFixture(t)
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go", "sub/b.go"}, nil)

	rec := get(t, srv, "/api/sessions/@5/tree")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var got treeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Repo || got.Root != "/ws/foo" || len(got.Paths) != 2 {
		t.Errorf("unexpected body: %+v", got)
	}
}

func TestStateFillsMissingBranches(t *testing.T) {
	srv, git := filesFixture(t)
	// Only the live row without a branch is looked up — not the ended row,
	// not the stray whose branch the TUI recorded — and only once across
	// two polls.
	git.EXPECT().Branch("/ws/foo/sub").Return("web-chat-layout").Times(1)

	for i := 0; i < 2; i++ {
		rec := get(t, srv, "/api/state")
		var got state.StateFile
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		branches := map[string]string{}
		for _, p := range got.Projects {
			branches[p.WindowID] = p.Branch
		}
		want := map[string]string{"@5": "web-chat-layout", "@6": "", "@7": "fix-it"}
		for k, v := range want {
			if branches[k] != v {
				t.Errorf("poll %d, %s: want branch %q, got %q", i, k, v, branches[k])
			}
		}
	}
}
