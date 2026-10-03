package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// shellsFixture: window @5 runs session s5 with two shells — 101 writing
// to an output file, 102 without one. Window @6 has no session.
func shellsFixture(t *testing.T) (*Server, *mock_web.MockShells) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{
		{Name: "foo", WindowID: "@5", SessionID: "s5", Path: "/ws/foo", Status: "active"},
		{Name: "bar", WindowID: "@6", Path: "/ws/bar", Status: "none"},
	}}, nil).AnyTimes()
	sh := mock_web.NewMockShells(ctrl)
	sh.EXPECT().List("s5").Return([]claude.ActiveShell{
		{PID: 101, Command: "go test ./...", OutputFile: "/tmp/tasks/a.output", StartTime: "Sat Oct 3 00:26:08 2026"},
		{PID: 102, Command: "sleep 100"},
	}, nil).AnyTimes()
	return NewServer(Deps{State: st, Shells: sh}, 0, "test"), sh
}

func TestShellsList(t *testing.T) {
	srv, _ := shellsFixture(t)
	rec := get(t, srv, "/api/sessions/@5/shells")
	var got []shellView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d %s (%v)", rec.Code, rec.Body, err)
	}
	if len(got) != 2 || got[0] != (shellView{ID: "101", Command: "go test ./...", Started: "Sat Oct 3 00:26:08 2026", Output: true}) || got[1].Output {
		t.Errorf("unexpected list: %+v", got)
	}
}

func TestShellOutput(t *testing.T) {
	srv, sh := shellsFixture(t)
	sh.EXPECT().Tail("/tmp/tasks/a.output", shellOutputBytes).Return("ok\tpkg\n", false, nil)

	rec := get(t, srv, "/api/sessions/@5/shells/101/output")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"ok\tpkg\n"`) {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

// Only a listed shell's own output file is ever read. No Tail expectation:
// gomock fails on any call.
func TestShellOutputRejects(t *testing.T) {
	srv, _ := shellsFixture(t)
	for _, path := range []string{
		"/api/sessions/@5/shells/999/output", // not one of the session's shells
		"/api/sessions/@5/shells/102/output", // no output file
		"/api/sessions/@6/shells/101/output", // window without a session
		"/api/sessions/@6/shells",
		"/api/sessions/@9/shells",
	} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("one\ntwo\n"), 0o644)
	if text, truncated, err := tailFile(short, 64); err != nil || truncated || text != "one\ntwo\n" {
		t.Errorf("short: %q %v %v", text, truncated, err)
	}

	long := filepath.Join(dir, "long")
	os.WriteFile(long, []byte("first line\nsecond line\nthird\n"), 0o644)
	// The last 12 bytes start mid-"second line"; the partial line is dropped.
	if text, truncated, err := tailFile(long, 12); err != nil || !truncated || text != "third\n" {
		t.Errorf("long: %q %v %v", text, truncated, err)
	}
}
