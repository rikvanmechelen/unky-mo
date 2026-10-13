package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func startupFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "startup", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Captures from Claude Code 2.1.236: the trust dialog with and without the
// pre-approved permissions notice.
func TestParseStartupDialogFixtures(t *testing.T) {
	v := parseStartupDialog(startupFixture(t, "trust-preapproved.txt"))
	if !v.found || v.kind != startupTrust || v.folder != "/home/dev/work/shop-api" || v.cursor != 1 {
		t.Fatalf("preapproved: %+v", v)
	}
	if len(v.rows) != 2 || v.rows[0].Label != "Yes, I trust this folder" || v.rows[1].N != 2 || v.rows[1].Label != "No, exit" {
		t.Errorf("rows %+v", v.rows)
	}
	for _, want := range []string{"This folder pre-approves 11 tool permissions in .claude/settings.local.json:", "Bash(go build:*)", "and 2 more"} {
		if !strings.Contains(v.warning, want) {
			t.Errorf("warning %q lacks %q", v.warning, want)
		}
	}
	if strings.Contains(v.warning, "These will apply") || strings.Contains(v.warning, "Security guide") {
		t.Errorf("warning runs past its notice: %q", v.warning)
	}

	// Output from before the dialog (settings warnings) is above the heading.
	v = parseStartupDialog(startupFixture(t, "trust-plain.txt"))
	if !v.found || v.folder != "/home/dev/work/scratch" || v.warning != "" || len(v.rows) != 2 {
		t.Errorf("plain: %+v", v)
	}
}

func TestParseStartupDialogWrappedFolder(t *testing.T) {
	screen := rule + "\n Accessing workspace:\n\n /home/dev/a/very/long/\n path/to/project\n\n Quick safety check: trust?\n\n" +
		" ❯ 1. Yes, I trust this folder\n   2. No, exit\n\n Enter to confirm · Esc to cancel\n"
	if v := parseStartupDialog(screen); !v.found || v.folder != "/home/dev/a/very/long/path/to/project" {
		t.Errorf("%+v", v)
	}
}

func TestParseStartupDialogNotTheTrustDialog(t *testing.T) {
	idle := rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on · ? for shortcuts\n"
	// Older versions drew the rows without numbers: nothing to press.
	oldRows := strings.Replace(startupFixture(t, "trust-preapproved.txt"), "1. ", "", 1)
	oldRows = strings.Replace(oldRows, "2. ", "", 1)
	for name, screen := range map[string]string{
		"unnumbered rows": oldRows,
		"older fixture":   readFixture(t, "trust-folder.txt"),
		"permission":      readFixture(t, "bash.txt"),
		"idle":            idle,
		"empty":           "",
		"heading only":    " Accessing workspace:\n /tmp/x\n",
		"no cursor":       strings.Replace(startupFixture(t, "trust-plain.txt"), "❯", " ", 1),
	} {
		if v := parseStartupDialog(screen); v.found {
			t.Errorf("%s parsed as the trust dialog: %+v", name, v)
		}
	}
}

func TestStartupSigChangesWithTheDialog(t *testing.T) {
	a := parseStartupDialog(startupFixture(t, "trust-plain.txt"))
	b := parseStartupDialog(strings.Replace(startupFixture(t, "trust-plain.txt"), "/home/dev/work/scratch", "/home/dev/work/other", 1))
	c := parseStartupDialog(strings.Replace(startupFixture(t, "trust-plain.txt"), "No, exit", "Quit", 1))
	if a.sig() == b.sig() || a.sig() == c.sig() {
		t.Error("sig ignores the folder or the rows")
	}
	// The cursor moving doesn't make it another dialog.
	moved := parseStartupDialog(strings.Replace(strings.Replace(startupFixture(t, "trust-plain.txt"), "❯ 1.", "  1.", 1), "  2.", "❯ 2.", 1))
	if !moved.found || moved.cursor != 2 || moved.sig() != a.sig() {
		t.Errorf("moved cursor: %+v", moved)
	}
}

// fakeTrust draws the trust dialog and reacts to a digit as probed: it
// answers at once, with no Enter.
type fakeTrust struct {
	screenText string
	answer     string
	keys       []string
}

func (f *fakeTrust) screen() string {
	if f.answer != "" {
		return "❯ \n" + rule + "\n"
	}
	return f.screenText
}

func (f *fakeTrust) press(k tmux.Key) {
	if k.Name != "" {
		f.keys = append(f.keys, k.Name)
		return
	}
	f.keys = append(f.keys, k.Text)
	if k.Text == "1" || k.Text == "2" {
		f.answer = k.Text
	}
}

const trustWindow = "@9"

func startupServer(t *testing.T, windows []tmux.Window, rows []state.ProjectState, f *fakeTrust) *Server {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: rows}, nil).AnyTimes()
	sess := mock_web.NewMockSessionOps(ctrl)
	sess.EXPECT().ListWindows().Return(windows, nil).AnyTimes()
	pane := mock_web.NewMockClaudePane(ctrl)
	if f != nil {
		pane.EXPECT().Capture("mo:@9.0").DoAndReturn(func(string) (string, error) { return f.screen(), nil }).AnyTimes()
		pane.EXPECT().SendKeys("mo:@9.0", gomock.Any()).DoAndReturn(func(_ string, keys []tmux.Key) error {
			for _, k := range keys {
				f.press(k)
			}
			return nil
		}).AnyTimes()
	}
	srv := NewServer(Deps{State: st, Sessions: sess, ClaudePane: pane}, 0, "mo")
	srv.modeSettle = 0
	return srv
}

var moWindows = []tmux.Window{
	{ID: "@0", Name: "fish"},
	{ID: "@9", Name: "shop-api", InstanceID: "abc123"},
}

func getStartup(srv *Server, window string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/windows/"+window+"/startup", nil))
	return rec
}

func postStartup(srv *Server, window, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/windows/"+window+"/startup", strings.NewReader(body)))
	return rec
}

func TestGetStartup(t *testing.T) {
	f := &fakeTrust{screenText: startupFixture(t, "trust-preapproved.txt")}
	srv := startupServer(t, moWindows, nil, f)
	rec := getStartup(srv, trustWindow)
	var got struct{ Dialog *startupDialogJSON }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Dialog == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	d := got.Dialog
	if d.Kind != "trust" || d.Question != "/home/dev/work/shop-api" || len(d.Rows) != 2 || d.Sig == "" || !strings.Contains(d.Preview, "pre-approves 11") {
		t.Errorf("dialog %+v", d)
	}
	if len(f.keys) != 0 {
		t.Errorf("GET pressed keys %q", f.keys)
	}

	// Just starting: nothing on screen yet, which isn't an error.
	srv = startupServer(t, moWindows, nil, &fakeTrust{screenText: "\n\n"})
	rec = getStartup(srv, trustWindow)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"dialog":null}` {
		t.Errorf("starting: %d %s", rec.Code, rec.Body)
	}
}

// Only a window mo launched and that has no session is ever read or typed
// into; every refusal happens before the pane is touched (the pane mock has
// no expectations, so a Capture or SendKeys fails the test).
func TestStartupRefusals(t *testing.T) {
	withSession := []state.ProjectState{{WindowID: "@9", SessionID: "s1", Status: "idle"}}
	for _, tc := range []struct {
		name, window, method, body string
		rows                       []state.ProjectState
		want                       int
	}{
		{"malformed id", "9", "GET", "", nil, 400},
		{"flag-like id", "-t", "GET", "", nil, 400},
		{"unknown window", "@42", "GET", "", nil, 404},
		{"the TUI's own window", "@0", "GET", "", nil, 404},
		{"already has a session", "@9", "GET", "", withSession, 409},
		{"answer: malformed id", "9", "POST", `{"sig":"x","row":1}`, nil, 400},
		{"answer: not a mo window", "@0", "POST", `{"sig":"x","row":1}`, nil, 404},
		{"answer: has a session", "@9", "POST", `{"sig":"x","row":1}`, withSession, 409},
		{"answer: bad body", "@9", "POST", `nope`, nil, 400},
		{"answer: row 0", "@9", "POST", `{"sig":"x","row":0}`, nil, 400},
		{"answer: row 10", "@9", "POST", `{"sig":"x","row":10}`, nil, 400},
	} {
		srv := startupServer(t, moWindows, tc.rows, nil)
		var rec *httptest.ResponseRecorder
		if tc.method == "GET" {
			rec = getStartup(srv, tc.window)
		} else {
			rec = postStartup(srv, tc.window, tc.body)
		}
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestAnswerStartup(t *testing.T) {
	newFake := func() *fakeTrust { return &fakeTrust{screenText: startupFixture(t, "trust-preapproved.txt")} }
	sig := parseStartupDialog(newFake().screenText).sig()

	t.Run("a digit, no Enter", func(t *testing.T) {
		f := newFake()
		rec := postStartup(startupServer(t, moWindows, nil, f), trustWindow, `{"sig":"`+sig+`","row":1}`)
		if rec.Code != http.StatusOK || f.answer != "1" || len(f.keys) != 1 || f.keys[0] != "1" {
			t.Errorf("%d %s, answer %q, keys %q", rec.Code, rec.Body, f.answer, f.keys)
		}
	})
	t.Run("No, exit is a row too", func(t *testing.T) {
		f := newFake()
		rec := postStartup(startupServer(t, moWindows, nil, f), trustWindow, `{"sig":"`+sig+`","row":2}`)
		if rec.Code != http.StatusOK || f.answer != "2" {
			t.Errorf("%d %s, answer %q", rec.Code, rec.Body, f.answer)
		}
	})
	t.Run("a stale sig", func(t *testing.T) {
		f := newFake()
		rec := postStartup(startupServer(t, moWindows, nil, f), trustWindow, `{"sig":"stale","row":1}`)
		if rec.Code != http.StatusConflict || len(f.keys) != 0 {
			t.Errorf("%d, keys %q", rec.Code, f.keys)
		}
	})
	t.Run("already answered in the terminal", func(t *testing.T) {
		f := newFake()
		f.answer = "1"
		rec := postStartup(startupServer(t, moWindows, nil, f), trustWindow, `{"sig":"`+sig+`","row":1}`)
		if rec.Code != http.StatusConflict || len(f.keys) != 0 {
			t.Errorf("%d, keys %q", rec.Code, f.keys)
		}
	})
	t.Run("a row the dialog lacks", func(t *testing.T) {
		f := newFake()
		rec := postStartup(startupServer(t, moWindows, nil, f), trustWindow, `{"sig":"`+sig+`","row":3}`)
		if rec.Code != http.StatusBadRequest || len(f.keys) != 0 {
			t.Errorf("%d, keys %q", rec.Code, f.keys)
		}
	})
}
