package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

const rule = "──────────────────────────────────────────"

// screen builds a capture of Claude's pane: body lines, then the prompt box
// and footer the way Claude Code draws them.
func screen(body ...string) string {
	out := ""
	for _, l := range body {
		out += l + "\n"
	}
	return out + rule + "\n❯ \n" + rule + "\n  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt"
}

func TestParseSpinner(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   *spinnerView
	}{
		{
			name:   "elapsed and tokens",
			screen: screen("● Running a command…", "", "✻ Discombobulating… (9s · ↓ 413 tokens)", ""),
			want:   &spinnerView{Verb: "Discombobulating…", Parts: []string{"9s", "↓ 413 tokens"}, ElapsedSeconds: 9, Tokens: "↓ 413 tokens"},
		},
		{
			name:   "minutes, k tokens and an extra part",
			screen: screen("✽ Booping… (1m 44s · ↓ 3.3k tokens · thought for 4s)"),
			want:   &spinnerView{Verb: "Booping…", Parts: []string{"1m 44s", "↓ 3.3k tokens", "thought for 4s"}, ElapsedSeconds: 104, Tokens: "↓ 3.3k tokens"},
		},
		{
			name:   "hours",
			screen: screen("· Cogitating… (1h 2m · ↑ 12k tokens)"),
			want:   &spinnerView{Verb: "Cogitating…", Parts: []string{"1h 2m", "↑ 12k tokens"}, ElapsedSeconds: 3720, Tokens: "↑ 12k tokens"},
		},
		{
			name:   "no details yet",
			screen: screen("✢ Simmering…"),
			want:   &spinnerView{Verb: "Simmering…", Parts: []string{}, ElapsedSeconds: -1},
		},
		{
			name:   "multi-word custom verb with a todo list below it",
			screen: screen("✶ Reticulating splines… (12s · ↓ 20 tokens)", "  ⎿  ☐ write the parser", "     ☐ add tests"),
			want:   &spinnerView{Verb: "Reticulating splines…", Parts: []string{"12s", "↓ 20 tokens"}, ElapsedSeconds: 12, Tokens: "↓ 20 tokens"},
		},
		{
			name:   "finished turn has no ellipsis",
			screen: screen("✻ Worked for 3m 2s"),
			want:   nil,
		},
		{
			name:   "spinner-looking text typed into the prompt box is ignored",
			screen: "● done\n" + rule + "\n❯ ✻ Fake… (9s)\n" + rule + "\n  footer",
			want:   nil,
		},
		{
			name:   "nothing on screen",
			screen: "",
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSpinner(c.screen)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseSpinner:\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func getSpinner(srv *Server, windowID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+windowID+"/spinner", nil))
	return rec
}

func TestHandleSpinnerCapturesClaudePane(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@4", SessionID: "s1", Status: "active"}},
	}, nil)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("mo:@4.0").Return(screen("✳ Brewing… (5s · ↓ 10 tokens)"), nil)

	rec := getSpinner(NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo"), "@4")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var body struct{ Spinner *spinnerView }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Spinner == nil || body.Spinner.Verb != "Brewing…" || body.Spinner.ElapsedSeconds != 5 {
		t.Fatalf("spinner: got %+v", body.Spinner)
	}
}

func TestHandleSpinnerNullWhenNoneShown(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@4", SessionID: "s1", Status: "idle"}},
	}, nil)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("mo:@4.0").Return(screen("● All done."), nil)

	rec := getSpinner(NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo"), "@4")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"spinner\":null}\n" {
		t.Fatalf("want 200 {\"spinner\":null}, got %d %q", rec.Code, rec.Body)
	}
}

func TestHandleSpinner404WithoutLiveSession(t *testing.T) {
	// A window with no session (or an unknown one) never reaches tmux:
	// the mock ClaudePane has no expectations.
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@4"}},
	}, nil)
	pane := mock_web.NewMockClaudePane(ctrl)

	rec := getSpinner(NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo"), "@4")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestHandleSpinner502OnCaptureError(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@4", SessionID: "s1", Status: "active"}},
	}, nil)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("mo:@4.0").Return("", errors.New("no pane"))

	rec := getSpinner(NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo"), "@4")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: want 502, got %d", rec.Code)
	}
}
