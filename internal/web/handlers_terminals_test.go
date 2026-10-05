package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

var termWindow = state.ProjectState{Name: "foo", WindowID: "@5", InstanceID: "abc123", SessionID: "s5", Path: "/ws/foo", Status: "idle"}

// termFixture serves a state file with window @5 whose terminals are %20
// (shown in the drawer) and %21 (parked). Claude's own pane, %2, is not a
// terminal.
func termFixture(t *testing.T) (*Server, *mock_web.MockTerminals) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{termWindow}}, nil).AnyTimes()
	terms := mock_web.NewMockTerminals(ctrl)
	terms.EXPECT().List(termWindow).Return([]ops.Terminal{
		{ID: "%20", Command: "fish", Cwd: "/ws/foo", Visible: true},
		{ID: "%21", Command: "go", Cwd: "/ws/foo/internal"},
	}, nil).AnyTimes()
	return NewServer(Deps{State: st, Terminals: terms}, 0, "test"), terms
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestTerminalsList(t *testing.T) {
	srv, _ := termFixture(t)
	rec := do(t, srv, http.MethodGet, "/api/sessions/@5/terminals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var got []terminalView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []terminalView{
		{ID: "20", Name: "fish foo", Cwd: "/ws/foo", Visible: true},
		{ID: "21", Name: "go internal", Cwd: "/ws/foo/internal"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestTerminalsUnknownWindow(t *testing.T) {
	srv, _ := termFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/sessions/@9/terminals"},
		{http.MethodPost, "/api/sessions/@9/terminals"},
		{http.MethodGet, "/api/sessions/@9/terminals/20/output"},
	} {
		if rec := do(t, srv, c.method, c.path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: want 404, got %d", c.method, c.path, rec.Code)
		}
	}
}

// A pane that isn't one of the window's terminals — here Claude's own pane
// %2 — can't be read, typed into or interrupted. No Capture/SendLine/
// Interrupt expectations: gomock fails on any call.
func TestTerminalsRejectForeignPane(t *testing.T) {
	srv, _ := termFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/sessions/@5/terminals/2/output", ""},
		{http.MethodPost, "/api/sessions/@5/terminals/2/input", `{"text":"rm -rf /"}`},
		{http.MethodPost, "/api/sessions/@5/terminals/2/interrupt", ""},
		{http.MethodPost, "/api/sessions/@5/terminals/2/keys", `{"keys":[{"key":"Enter"}]}`},
		{http.MethodPost, "/api/sessions/@5/terminals/2/keys", `{"paste":"rm -rf /\n"}`},
	} {
		if rec := do(t, srv, c.method, c.path, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: want 404, got %d", c.method, c.path, rec.Code)
		}
	}
}

func TestTerminalOutput(t *testing.T) {
	srv, terms := termFixture(t)
	terms.EXPECT().Capture("%21").Return(tmux.Screen{Text: "\x1b[32m$\x1b[39m go test\nok", CursorLine: 1, CursorCol: 2, CursorVisible: true}, nil)

	rec := do(t, srv, http.MethodGet, "/api/sessions/@5/terminals/21/output", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var got screenView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := screenView{Text: "\x1b[32m$\x1b[39m go test\nok", CursorLine: 1, CursorCol: 2, CursorVisible: true}
	if got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

// Live keystrokes reach SendKeys in order — text literally, key names as
// keys — and the answer is the screen right after.
func TestTerminalKeys(t *testing.T) {
	srv, terms := termFixture(t)
	gomock.InOrder(
		terms.EXPECT().SendKeys("%20", []tmux.Key{{Text: "git che"}, {Name: "Tab"}, {Name: "C-r"}, {Text: "Up"}}).Return(nil),
		terms.EXPECT().Capture("%20").Return(tmux.Screen{Text: "$ git checkout", CursorCol: 15, CursorVisible: true}, nil),
	)
	body := `{"keys":[{"text":"git che"},{"key":"Tab"},{"key":"C-r"},{"text":"Up"}]}`
	rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/20/keys", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"$ git checkout"`) {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

// A paste goes in as one bracketed paste, newlines kept and other control
// characters (an escape sequence that would end the paste early) dropped.
func TestTerminalKeysPaste(t *testing.T) {
	srv, terms := termFixture(t)
	gomock.InOrder(
		terms.EXPECT().Paste("%21", "ls\n\tpwd[201~").Return(nil),
		terms.EXPECT().Capture("%21").Return(tmux.Screen{}, nil),
	)
	rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/21/keys", `{"paste":"ls\n\tpwd\u001b[201~"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

// Key names outside the fixed list, control characters in text (they must
// come as keys) and malformed entries are refused before anything is typed.
// No SendKeys/Paste expectations: gomock fails on any call.
func TestTerminalKeysRejects(t *testing.T) {
	srv, _ := termFixture(t)
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"keys":[{"key":"Enter"},{"key":"kill-server"}]}`, http.StatusBadRequest},
		{`{"keys":[{"key":"C-;"}]}`, http.StatusBadRequest},
		{`{"keys":[{"key":"Tab;"}]}`, http.StatusBadRequest},
		{`{"keys":[{"text":"ls\n"}]}`, http.StatusBadRequest},
		{`{"keys":[{"text":"a\u001b[A"}]}`, http.StatusBadRequest},
		{`{"keys":[{"text":"a\tb"}]}`, http.StatusBadRequest},
		{`{"keys":[{"text":"a","key":"Tab"}]}`, http.StatusBadRequest},
		{`{"keys":[{}]}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
		{`{"keys":[{"text":"` + strings.Repeat("x", maxLiveText+1) + `"}]}`, http.StatusRequestEntityTooLarge},
		{`{"paste":"` + strings.Repeat("x", maxLivePaste+1) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		if rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/20/keys", c.body); rec.Code != c.code {
			b := c.body
			if len(b) > 60 {
				b = b[:60] + "…"
			}
			t.Errorf("%s: want %d, got %d: %s", b, c.code, rec.Code, rec.Body)
		}
	}
}

func TestTerminalInput(t *testing.T) {
	srv, terms := termFixture(t)
	gomock.InOrder(
		terms.EXPECT().SendLine("%20", "echo hello").Return(nil),
		terms.EXPECT().SendLine("%20", "").Return(nil), // bare Enter
	)

	for _, body := range []string{`{"text":"echo hello"}`, `{"text":""}`} {
		if rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/20/input", body); rec.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestTerminalInputRejectsMultiLine(t *testing.T) {
	srv, _ := termFixture(t) // no SendLine expectation
	for _, body := range []string{`{"text":"ls\nrm x"}`, `{"text":"a\rb"}`, `not json`} {
		if rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/20/input", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", body, rec.Code)
		}
	}
}

func TestTerminalInterrupt(t *testing.T) {
	srv, terms := termFixture(t)
	terms.EXPECT().Interrupt("%21").Return(nil)

	if rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals/21/interrupt", ""); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestNewTerminal(t *testing.T) {
	srv, terms := termFixture(t)
	terms.EXPECT().New(termWindow).Return("%22", nil)

	rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"id":"22"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

func TestNewTerminalError(t *testing.T) {
	srv, terms := termFixture(t)
	terms.EXPECT().New(termWindow).Return("", errors.New("no server"))

	if rec := do(t, srv, http.MethodPost, "/api/sessions/@5/terminals", ""); rec.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rec.Code)
	}
}

func TestCloseTerminal(t *testing.T) {
	srv, terms := termFixture(t)
	terms.EXPECT().Close("%21").Return(nil)

	if rec := do(t, srv, http.MethodDelete, "/api/sessions/@5/terminals/21", ""); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	// Claude's pane %2 isn't a terminal: no Close call (gomock would fail).
	if rec := do(t, srv, http.MethodDelete, "/api/sessions/@5/terminals/2", ""); rec.Code != http.StatusNotFound {
		t.Errorf("foreign pane: want 404, got %d", rec.Code)
	}
}
