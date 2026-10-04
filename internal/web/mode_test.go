package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// footerScreen is a capture of Claude's pane with the given footer line
// under the prompt box.
func footerScreen(footer string) string {
	return "● Done.\n\n" + rule + "\n❯ \n" + rule + "\n" + footer
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   *modeView
	}{
		{"auto while working", footerScreen("  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents"), &modeView{"auto", "auto mode"}},
		{"manual", footerScreen("  ⏸ manual mode on · ? for shortcuts · ← for agents"), &modeView{"manual", "manual mode"}},
		{"accept edits", footerScreen("  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents"), &modeView{"acceptEdits", "accept edits"}},
		{"plan", footerScreen("  ⏸ plan mode on (shift+tab to cycle) · ← for agents"), &modeView{"plan", "plan mode"}},
		{"bypass", footerScreen("  ⏵⏵ bypass permissions on (shift+tab to cycle)"), &modeView{"bypassPermissions", "bypass permissions"}},
		{"unknown label keeps its words", footerScreen("  ⏵⏵ turbo mode on (shift+tab to cycle)"), &modeView{"", "turbo mode"}},
		{"no indicator", footerScreen("  ? for shortcuts"), nil},
		{"no prompt box", "Do you want to proceed?\n❯ 1. Yes\n  2. No", nil},
		{
			// A mode line in the transcript, above the box, isn't the footer.
			"ignores text above the box",
			"  ⏸ plan mode on (shift+tab to cycle)\n" + footerScreen("  ? for shortcuts"),
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseMode(c.screen); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseMode: got %+v, want %+v", got, c.want)
			}
		})
	}
}

func modeServer(t *testing.T, status string) (*Server, *mock_web.MockClaudePane) {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@4", SessionID: "s1", Status: status}},
	}, nil).AnyTimes()
	pane := mock_web.NewMockClaudePane(ctrl)
	srv := NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo")
	srv.modeSettle = 0
	return srv, pane
}

// fakeCycle makes the mock pane behave like Claude Code: Capture shows the
// current mode's footer, CycleMode advances it. Returns the press count.
func fakeCycle(pane *mock_web.MockClaudePane, footers ...string) *int {
	i, presses := 0, 0
	pane.EXPECT().Capture("mo:@4.0").DoAndReturn(func(string) (string, error) {
		return footerScreen(footers[i%len(footers)]), nil
	}).AnyTimes()
	pane.EXPECT().CycleMode("mo:@4.0").DoAndReturn(func(string) error {
		i++
		presses++
		return nil
	}).AnyTimes()
	return &presses
}

var probedCycle = []string{
	"  ⏵⏵ auto mode on (shift+tab to cycle)",
	"  ⏸ manual mode on · ? for shortcuts",
	"  ⏵⏵ accept edits on (shift+tab to cycle)",
	"  ⏸ plan mode on (shift+tab to cycle)",
}

func postMode(srv *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/@4/mode", strings.NewReader(body)))
	return rec
}

func modeFrom(t *testing.T, rec *httptest.ResponseRecorder) *modeView {
	t.Helper()
	var body struct{ Mode *modeView }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Mode
}

func TestHandleModeReadsFooter(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	fakeCycle(pane, probedCycle...)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/@4/mode", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if m := modeFrom(t, rec); m == nil || m.Mode != "auto" {
		t.Fatalf("mode: got %+v", m)
	}
}

func TestSetModeCyclesUntilMatch(t *testing.T) {
	srv, pane := modeServer(t, "active")
	presses := fakeCycle(pane, probedCycle...)
	rec := postMode(srv, `{"mode":"plan"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if m := modeFrom(t, rec); m.Mode != "plan" || *presses != 3 {
		t.Fatalf("got %+v after %d presses, want plan after 3", m, *presses)
	}
}

func TestSetModeAlreadyThereSendsNothing(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	presses := fakeCycle(pane, probedCycle...)
	if rec := postMode(srv, `{"mode":"auto"}`); rec.Code != http.StatusOK || *presses != 0 {
		t.Fatalf("got %d after %d presses, want 200 after 0", rec.Code, *presses)
	}
}

func TestSetModeNextStepsOnce(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	presses := fakeCycle(pane, probedCycle...)
	rec := postMode(srv, `{"next":true}`)
	if m := modeFrom(t, rec); rec.Code != http.StatusOK || m.Mode != "manual" || *presses != 1 {
		t.Fatalf("got %d %+v after %d presses, want 200 manual after 1", rec.Code, m, *presses)
	}
}

func TestSetModeNotInCycleIs409(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	presses := fakeCycle(pane, probedCycle...)
	rec := postMode(srv, `{"mode":"bypassPermissions"}`)
	// One full lap (4 presses) brings it back to auto, which it has seen.
	if rec.Code != http.StatusConflict || *presses != 4 {
		t.Fatalf("got %d after %d presses, want 409 after 4: %s", rec.Code, *presses, rec.Body)
	}
}

func TestSetModeFooterNeverChangesIs409(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().Capture("mo:@4.0").Return(footerScreen(probedCycle[0]), nil).AnyTimes()
	pane.EXPECT().CycleMode("mo:@4.0").Return(nil).Times(1)
	if rec := postMode(srv, `{"mode":"plan"}`); rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

func TestSetModeRejectsWithoutPressing(t *testing.T) {
	// No CycleMode (or Capture) expectations: any tmux call fails the test.
	cases := []struct {
		name, status, body string
		want               int
	}{
		{"question has the keyboard", "question", `{"mode":"plan"}`, http.StatusConflict},
		{"permission has the keyboard", "permission", `{"mode":"plan"}`, http.StatusConflict},
		{"unknown mode", "idle", `{"mode":"yolo"}`, http.StatusBadRequest},
		{"empty body", "idle", `{}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := modeServer(t, c.status)
			if rec := postMode(srv, c.body); rec.Code != c.want {
				t.Fatalf("status: want %d, got %d", c.want, rec.Code)
			}
		})
	}
}

func TestSetModeCaptureErrorIs502(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().Capture("mo:@4.0").Return("", errors.New("no pane"))
	if rec := postMode(srv, `{"mode":"plan"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("status: want 502, got %d", rec.Code)
	}
}
