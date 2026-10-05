package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func bashFixture(t *testing.T) (*Server, *mock_web.MockBashChanges) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{
		{Name: "foo", WindowID: "@5", SessionID: "s5", Path: "/ws/foo/sub", Status: "active"},
		{Name: "bar", WindowID: "@6", Path: "/ws/bar", Status: "none"},
	}}, nil).AnyTimes()
	bash := mock_web.NewMockBashChanges(ctrl)
	return NewServer(Deps{State: st, BashDiffs: bash}, 0, "test"), bash
}

func TestBashChangesList(t *testing.T) {
	srv, bash := bashFixture(t)
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	file := []bashsnap.FileStat{{Path: "a.go", Added: 2, Removed: 1}}
	bash.EXPECT().List(gomock.Any(), "s5").Return(map[string]bashsnap.Change{
		"toolu_a":     {Root: "/ws/foo", Start: at(0), End: at(10), Files: file},
		"toolu_b":     {Root: "/ws/foo", Start: at(5), End: at(6), Files: file},
		"toolu_c":     {Root: "/ws/foo", Start: at(20), End: at(21), Files: file},
		"toolu_other": {Root: "/ws/elsewhere", Start: at(5), End: at(6), Files: file},
	}, nil).Times(2)

	rec := get(t, srv, "/api/sessions/@5/bash-changes")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var got map[string]bashChangeView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %v; want a, b and c (another checkout's call left out)", got)
	}
	if got["toolu_a"].Overlaps != 1 || got["toolu_b"].Overlaps != 1 || got["toolu_c"].Overlaps != 0 {
		t.Errorf("overlaps = a %d, b %d, c %d", got["toolu_a"].Overlaps, got["toolu_b"].Overlaps, got["toolu_c"].Overlaps)
	}
	if got["toolu_a"].Files[0].Added != 2 {
		t.Errorf("files = %+v", got["toolu_a"].Files)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/@5/bash-changes", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	again := httptest.NewRecorder()
	srv.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: got %d", again.Code)
	}
}

func TestBashDiff(t *testing.T) {
	srv, bash := bashFixture(t)
	bash.EXPECT().Diff(gomock.Any(), "s5", "toolu_a").Return("/ws/foo", &bashsnap.Diff{
		Files: []bashsnap.FileStat{{Path: "a.go", Added: 1}}, Patch: "diff --git a/a.go b/a.go\n",
	}, nil)
	rec := get(t, srv, "/api/sessions/@5/bash-changes/toolu_a")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var got bashsnap.Diff
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Patch == "" || len(got.Files) != 1 {
		t.Errorf("got %+v, %v", got, err)
	}

	// A record from another checkout, or none, is a 404.
	bash.EXPECT().Diff(gomock.Any(), "s5", "toolu_x").Return("/ws/elsewhere", &bashsnap.Diff{}, nil)
	bash.EXPECT().Diff(gomock.Any(), "s5", "toolu_none").Return("", nil, bashsnap.ErrNoRecord)
	for _, id := range []string{"toolu_x", "toolu_none"} {
		if rec := get(t, srv, "/api/sessions/@5/bash-changes/"+id); rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d", id, rec.Code)
		}
	}
}

func TestBashChangesRefused(t *testing.T) {
	srv, _ := bashFixture(t) // no expectations: gomock fails on any call
	for _, path := range []string{
		"/api/sessions/@6/bash-changes",        // no session
		"/api/sessions/@9/bash-changes",        // no window
		"/api/sessions/@5/bash-changes/..%2Fx", // not a tool use id
		"/api/sessions/@5/bash-changes/toolu_a.json",
	} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d", path, rec.Code)
		}
	}
	off := NewServer(Deps{State: srv.deps.State}, 0, "test")
	if rec := get(t, off, "/api/sessions/@5/bash-changes"); rec.Code != http.StatusNotFound {
		t.Errorf("snapshots off: got %d", rec.Code)
	}
}

func TestSameCheckout(t *testing.T) {
	for _, c := range []struct {
		root, path string
		want       bool
	}{
		{"/ws/foo", "/ws/foo", true},
		{"/ws/foo", "/ws/foo/sub", true},
		{"/ws/foo", "/ws/foobar", false},
		{"/ws/foo/sub", "/ws/foo", false},
	} {
		if got := sameCheckout(c.root, c.path); got != c.want {
			t.Errorf("sameCheckout(%q, %q) = %v", c.root, c.path, got)
		}
	}
}
