package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// fakeMenuPane draws a pick-one menu the way Claude Code does (probed on
// 2.1.325: title, subtitle, rows with a ❯ cursor, "↓ N more" when the list
// is cut, a legend) and reacts to keys: ↑/↓ move without wrapping, Enter
// picks and closes, Esc closes, text is collected.
type fakeMenuPane struct {
	rows   []string
	cursor int
	top    int
	view   int
	open   bool
	stuck  bool // ignores ↑/↓, like a menu whose list can't move
	slow   bool // hasn't redrawn after Esc yet (the menu still shows)
	sent   [][]tmux.Key
	picked string
	typed  string
	closed int
}

func newFakeMenuPane(rows ...string) *fakeMenuPane {
	return &fakeMenuPane{rows: rows, view: len(rows), open: true}
}

func (f *fakeMenuPane) screen() string {
	if !f.open {
		return footerScreen("  ⏵⏵ auto mode on (shift+tab to cycle)")
	}
	var b strings.Builder
	b.WriteString("❯ /model\n\n" + strings.Repeat("▔", 40) + "\n   Pick one\n   Choose carefully\n")
	if f.top > 0 {
		fmt.Fprintf(&b, "     ↑ %d more\n", f.top)
	}
	end := min(f.top+f.view, len(f.rows))
	for i := f.top; i < end; i++ {
		if i == f.cursor {
			b.WriteString("   ❯ " + f.rows[i] + "\n")
		} else {
			b.WriteString("     " + f.rows[i] + "\n")
		}
	}
	if below := len(f.rows) - end; below > 0 {
		fmt.Fprintf(&b, "     ↓ %d more\n", below)
	}
	b.WriteString("\n   Enter to select · Esc to cancel\n")
	return b.String()
}

func (f *fakeMenuPane) send(_ string, keys []tmux.Key) error {
	f.sent = append(f.sent, keys)
	for _, k := range keys {
		if !f.open {
			continue
		}
		switch {
		case k.Text != "":
			f.typed += k.Text
		case k.Name == "Down" && !f.stuck:
			if f.cursor < len(f.rows)-1 {
				f.cursor++
			}
		case k.Name == "Up" && !f.stuck:
			if f.cursor > 0 {
				f.cursor--
			}
		case k.Name == "Enter":
			f.picked, f.open = f.rows[f.cursor], false
		case k.Name == "Escape":
			f.closed++
			if !f.slow {
				f.open = false
			}
		}
		if f.cursor < f.top {
			f.top = f.cursor
		}
		if f.cursor >= f.top+f.view {
			f.top = f.cursor - f.view + 1
		}
	}
	return nil
}

func (f *fakeMenuPane) pressed() []string {
	var out []string
	for _, batch := range f.sent {
		for _, k := range batch {
			if k.Text != "" {
				out = append(out, "text:"+k.Text)
			} else {
				out = append(out, k.Name)
			}
		}
	}
	return out
}

func menuKeysServer(t *testing.T, f *fakeMenuPane) *Server {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().CaptureStyled("mo:@4.0").DoAndReturn(func(string) (string, error) { return f.screen(), nil }).AnyTimes()
	pane.EXPECT().SendKeys("mo:@4.0", gomock.Any()).DoAndReturn(f.send).AnyTimes()
	return srv
}

func postMenuKeys(srv *Server, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/@4/menu/keys", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

type menuReply struct {
	Menu  *menuView `json:"menu"`
	Error string    `json:"error"`
}

func decodeMenuReply(t *testing.T, rec *httptest.ResponseRecorder) menuReply {
	t.Helper()
	var r menuReply
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return r
}

func currentSig(t *testing.T, f *fakeMenuPane) string {
	t.Helper()
	v := parseMenu(f.screen())
	if v == nil {
		t.Fatal("fake shows no menu")
	}
	return v.Sig
}

func keyList(names ...string) []map[string]string {
	var out []map[string]string
	for _, n := range names {
		out = append(out, map[string]string{"key": n})
	}
	return out
}

func TestMenuKeysMoveTheCursorAndReturnTheNewScreen(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta", "Gamma")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "keys": keyList("down", "down")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := f.pressed(); !reflect.DeepEqual(got, []string{"Down", "Down"}) {
		t.Errorf("pressed %v", got)
	}
	r := decodeMenuReply(t, rec)
	if r.Menu == nil || r.Menu.Cursor != 2 || r.Menu.Rows[2].Text != "Gamma" {
		t.Fatalf("menu = %+v", r.Menu)
	}
}

func TestMenuKeysEnterClosesTheMenu(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "keys": keyList("down", "enter")})
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"menu\":null}\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
	if f.picked != "Beta" {
		t.Errorf("picked %q", f.picked)
	}
}

func TestMenuKeysStaleSigPressesNothing(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta")
	srv := menuKeysServer(t, f)
	sig := currentSig(t, f)
	f.cursor = 1 // the terminal user moved it
	rec := postMenuKeys(srv, map[string]any{"sig": sig, "keys": keyList("enter")})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if r := decodeMenuReply(t, rec); r.Menu == nil || r.Menu.Cursor != 1 || r.Error == "" {
		t.Errorf("reply = %+v", r)
	}
	if len(f.sent) != 0 {
		t.Errorf("pressed %v", f.pressed())
	}
}

func TestMenuKeysNoMenuPressesNothing(t *testing.T) {
	f := newFakeMenuPane("Alpha")
	f.open = false
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": "whatever", "keys": keyList("esc")})
	if rec.Code != http.StatusConflict || len(f.sent) != 0 {
		t.Fatalf("status %d, pressed %v", rec.Code, f.pressed())
	}
	if r := decodeMenuReply(t, rec); r.Menu != nil {
		t.Errorf("menu = %+v", r.Menu)
	}
}

func TestMenuKeysRefusals(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta")
	srv := menuKeysServer(t, f)
	sig := currentSig(t, f)
	long := strings.Repeat("x", 201)
	many := make([]string, 65)
	for i := range many {
		many[i] = "down"
	}
	cases := map[string]map[string]any{
		"two escapes":       {"sig": sig, "keys": keyList("esc", "esc")},
		"keys after esc":    {"sig": sig, "keys": keyList("esc", "down")},
		"unknown key name":  {"sig": sig, "keys": keyList("F13")},
		"ctrl-c quits":      {"sig": sig, "keys": keyList("C-c")},
		"ctrl-d quits":      {"sig": sig, "keys": keyList("C-d")},
		"ctrl-z suspends":   {"sig": sig, "keys": keyList("C-z")},
		"control text":      {"sig": sig, "keys": []map[string]string{{"text": "a\x1b[A"}}},
		"newline text":      {"sig": sig, "keys": []map[string]string{{"text": "a\nb"}}},
		"oversized text":    {"sig": sig, "keys": []map[string]string{{"text": long}}},
		"too many keys":     {"sig": sig, "keys": keyList(many...)},
		"both key and text": {"sig": sig, "keys": []map[string]string{{"key": "down", "text": "x"}}},
		"neither":           {"sig": sig, "keys": []map[string]string{{}}},
		"no sig":            {"keys": keyList("down")},
		"nothing to do":     {"sig": sig},
		"goto out of range": {"sig": sig, "goto": map[string]int{"row": 2}},
		"goto negative":     {"sig": sig, "goto": map[string]int{"row": -1}},
		"multi-char key":    {"sig": sig, "keys": keyList("ab")},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postMenuKeys(srv, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
	if len(f.sent) != 0 {
		t.Errorf("a refused request pressed %v", f.pressed())
	}
}

func TestMenuKeysVocabularyReachesTmux(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta")
	srv := menuKeysServer(t, f)
	body := map[string]any{"sig": currentSig(t, f), "keys": []map[string]string{
		{"key": "up"}, {"key": "left"}, {"key": "right"}, {"key": "tab"}, {"key": "btab"}, {"key": "space"},
		{"key": "pgup"}, {"key": "pgdn"}, {"key": "home"}, {"key": "end"}, {"key": "backspace"},
		{"key": "C-s"}, {"key": "s"}, {"key": "/"}, {"text": "think"}, {"key": "esc"},
	}}
	if rec := postMenuKeys(srv, body); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := []string{"Up", "Left", "Right", "Tab", "BTab", "Space", "PPage", "NPage", "Home", "End", "BSpace", "C-s", "text:s", "text:/", "text:think", "Escape"}
	if got := f.pressed(); !reflect.DeepEqual(got, want) {
		t.Errorf("pressed %v\nwant    %v", got, want)
	}
}

func TestMenuKeysEscClosesOnce(t *testing.T) {
	f := newFakeMenuPane("Alpha")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "keys": keyList("down", "esc")})
	if rec.Code != http.StatusOK || f.closed != 1 {
		t.Fatalf("status %d closed %d", rec.Code, f.closed)
	}
	// the menu is gone, so a second Esc is refused before it opens /rewind
	rec = postMenuKeys(srv, map[string]any{"sig": "gone", "keys": keyList("esc")})
	if rec.Code != http.StatusConflict || f.closed != 1 {
		t.Fatalf("second esc: status %d closed %d", rec.Code, f.closed)
	}
}

func TestMenuGotoWalksToTheRowByText(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta", "Gamma", "Delta")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 3}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := f.pressed(); !reflect.DeepEqual(got, []string{"Down", "Down", "Down"}) {
		t.Errorf("pressed %v", got)
	}
	if r := decodeMenuReply(t, rec); r.Menu == nil || r.Menu.Rows[r.Menu.Cursor].Text != "Delta" {
		t.Errorf("menu = %+v", r.Menu)
	}
	// and back up again
	rec = postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 1}})
	if rec.Code != http.StatusOK || f.cursor != 1 {
		t.Fatalf("status %d cursor %d", rec.Code, f.cursor)
	}
}

func TestMenuGotoThenKeys(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta", "Gamma")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 1}, "keys": keyList("enter")})
	if rec.Code != http.StatusOK || f.picked != "Beta" {
		t.Fatalf("status %d picked %q", rec.Code, f.picked)
	}
}

func TestMenuGotoFollowsAScrollingList(t *testing.T) {
	rows := []string{"r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7"}
	f := newFakeMenuPane(rows...)
	f.view = 4 // r0..r3 visible, "↓ 4 more"
	srv := menuKeysServer(t, f)
	// r3 is the last visible row; moving there doesn't scroll.
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 3}})
	if rec.Code != http.StatusOK || f.cursor != 3 {
		t.Fatalf("status %d cursor %d", rec.Code, f.cursor)
	}
	// One more step scrolls the window: the indexes shift but r3 is still
	// found by its text, now at index 2.
	f.sent = nil
	f.cursor, f.top = 4, 1 // r1..r4 visible, cursor on r4 (index 3)
	rec = postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 1}})
	if rec.Code != http.StatusOK || rows[f.cursor] != "r2" {
		t.Fatalf("status %d cursor on %s: %s", rec.Code, rows[f.cursor], rec.Body)
	}
	if got := f.pressed(); !reflect.DeepEqual(got, []string{"Up", "Up"}) {
		t.Errorf("pressed %v", got)
	}
}

func TestMenuGotoGivesUpWhenNothingMoves(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta", "Gamma")
	f.stuck = true
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 2}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if n := len(f.pressed()); n > 3 {
		t.Errorf("kept pressing: %d presses", n)
	}
	if r := decodeMenuReply(t, rec); r.Menu == nil || r.Error == "" {
		t.Errorf("reply = %+v", r)
	}
}

func TestMenuGotoAlreadyThereSendsNothing(t *testing.T) {
	f := newFakeMenuPane("Alpha", "Beta")
	srv := menuKeysServer(t, f)
	rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "goto": map[string]int{"row": 0}})
	if rec.Code != http.StatusOK || len(f.sent) != 0 {
		t.Fatalf("status %d pressed %v", rec.Code, f.pressed())
	}
}

var _ = mock_web.NewMockClaudePane

func TestMenuKeysSecondEscOnAnUnchangedScreenIsRefused(t *testing.T) {
	// Claude hasn't redrawn after the first Esc: the screen (and sig) look
	// the same, but another Esc could land on the empty prompt and open
	// /rewind once the first one is processed.
	f := newFakeMenuPane("Alpha", "Beta")
	f.slow = true
	srv := menuKeysServer(t, f)
	sig := currentSig(t, f)
	if rec := postMenuKeys(srv, map[string]any{"sig": sig, "keys": keyList("esc")}); rec.Code != http.StatusOK {
		t.Fatalf("first esc: %d %s", rec.Code, rec.Body)
	}
	rec := postMenuKeys(srv, map[string]any{"sig": sig, "keys": keyList("esc")})
	if rec.Code != http.StatusConflict || f.closed != 1 {
		t.Fatalf("second esc: status %d, escapes pressed %d", rec.Code, f.closed)
	}
	// Other keys are fine on that screen...
	if rec := postMenuKeys(srv, map[string]any{"sig": sig, "keys": keyList("down")}); rec.Code != http.StatusOK {
		t.Fatalf("down: %d %s", rec.Code, rec.Body)
	}
	// ...and once the screen has changed, Esc is too.
	if rec := postMenuKeys(srv, map[string]any{"sig": currentSig(t, f), "keys": keyList("esc")}); rec.Code != http.StatusOK || f.closed != 2 {
		t.Fatalf("esc on a new screen: status %d, escapes %d", rec.Code, f.closed)
	}
}
