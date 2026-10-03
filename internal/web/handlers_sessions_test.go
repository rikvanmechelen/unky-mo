package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

const (
	sessA = "aaaaaaaa-0000-0000-0000-000000000001"
	sessB = "bbbbbbbb-0000-0000-0000-000000000002"
)

var testAgents = []config.AgentConfig{
	{Key: "c", Name: "Claude", Cmd: "claude", ResumeCmd: "claude --resume", Default: true},
	{Key: "g", Name: "Gemini", Cmd: "gemini"},
}

type sessionFixture struct {
	srv      *Server
	state    *mock_web.MockStateReader
	trees    *mock_web.MockWorktreeReader
	history  *mock_web.MockSessionHistory
	sessions *mock_web.MockSessionOps
}

// newSessionFixture wires project "alpha" at /ws/alpha, with main checked out
// there and feat checked out in a worktree.
func newSessionFixture(t *testing.T, rows ...state.ProjectState) *sessionFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &sessionFixture{
		state:    mock_web.NewMockStateReader(ctrl),
		trees:    mock_web.NewMockWorktreeReader(ctrl),
		history:  mock_web.NewMockSessionHistory(ctrl),
		sessions: mock_web.NewMockSessionOps(ctrl),
	}
	projects := mock_web.NewMockProjectLister(ctrl)
	projects.EXPECT().LoadProjects().Return([]project.Project{{Name: "alpha", Path: "/ws/alpha"}}, nil).AnyTimes()
	f.trees.EXPECT().ListBranches("/ws/alpha").Return([]project.Branch{
		{Name: "main", IsMain: true},
		{Name: "feat", WorktreePath: "/ws/alpha.worktrees/feat"},
		{Name: "old"},
	}, nil).AnyTimes()
	f.state.EXPECT().Read().Return(&state.StateFile{Projects: rows}, nil).AnyTimes()
	f.srv = NewServer(Deps{
		State:     f.state,
		Projects:  projects,
		Worktrees: f.trees,
		History:   f.history,
		Sessions:  f.sessions,
		Agents:    testAgents,
	}, 0, "mo")
	return f
}

func (f *sessionFixture) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func decodeWindowID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
	var got launchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.WindowID
}

func liveRow(cwd, windowName, windowID, sessionID string, index int) state.ProjectState {
	return state.ProjectState{Name: "alpha", Path: cwd, WindowName: windowName, WindowID: windowID, SessionID: sessionID, Status: "idle", Index: index}
}

func TestHandleAgentsHidesCommands(t *testing.T) {
	f := newSessionFixture(t)
	rec := f.do(http.MethodGet, "/api/agents", "")
	if strings.Contains(rec.Body.String(), "--resume") || strings.Contains(rec.Body.String(), `"cmd"`) {
		t.Fatalf("agent commands leaked: %s", rec.Body)
	}
	var got []agentView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Default || got[1].Default {
		t.Fatalf("unexpected agents: %+v", got)
	}
}

func TestHandleProjectSessionsListsCheckouts(t *testing.T) {
	f := newSessionFixture(t,
		liveRow("/ws/alpha", "alpha [2]", "@7", sessB, 2),
		liveRow("/ws/alpha", "alpha", "@3", sessA, 0),
	)
	f.history.EXPECT().RecentSessions("/ws/alpha", recentSessionLimit).Return([]claude.RecentSession{
		{SessionID: sessA, Title: "first", LastActive: time.Unix(100, 0)},
		{SessionID: "cccccccc-0000-0000-0000-000000000003", Summary: "old one"},
	})
	f.history.EXPECT().RecentSessions("/ws/alpha.worktrees/feat", recentSessionLimit).Return(nil)

	rec := f.do(http.MethodGet, "/api/projects/alpha/sessions", "")
	var got []checkoutView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	// "old" isn't checked out anywhere, so only main + feat.
	if len(got) != 2 || got[0].Branch != "main" || got[1].Branch != "feat" {
		t.Fatalf("checkouts: %+v", got)
	}
	main := got[0]
	if len(main.Live) != 2 || main.Live[0].WindowID != "@3" || !main.Live[0].Primary || main.Live[1].Primary {
		t.Errorf("live (primary first): %+v", main.Live)
	}
	if main.Recent[1].Title != "old one" {
		t.Errorf("untitled session should fall back to its summary, got %q", main.Recent[1].Title)
	}
	if len(main.Recent) != 2 || !main.Recent[0].Live || main.Recent[0].WindowID != "@3" || main.Recent[1].Live {
		t.Errorf("recent: %+v", main.Recent)
	}
}

func TestLaunchFreshIntoFreePrimary(t *testing.T) {
	f := newSessionFixture(t)
	f.sessions.EXPECT().WindowExists("alpha").Return(false)
	f.sessions.EXPECT().Launch(ops.LaunchParams{
		WindowName:    "alpha",
		Cwd:           "/ws/alpha",
		ShellCmd:      "claude",
		AgentKey:      "c",
		AttachSidebar: true,
		SwitchFocus:   false,
	}).Return(&ops.LaunchResult{Target: "mo:@9"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main"}`)
	if got := decodeWindowID(t, rec); got != "@9" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchResumeInWorktreeWithAgent(t *testing.T) {
	f := newSessionFixture(t)
	f.history.EXPECT().TranscriptExists("/ws/alpha.worktrees/feat", sessA).Return(true)
	f.sessions.EXPECT().WindowExists("alpha@feat").Return(false)
	f.sessions.EXPECT().Launch(gomock.Any()).DoAndReturn(func(p ops.LaunchParams) (*ops.LaunchResult, error) {
		if p.WindowName != "alpha@feat" || p.Cwd != "/ws/alpha.worktrees/feat" || p.ShellCmd != "claude --resume "+sessA {
			t.Errorf("launch params: %+v", p)
		}
		return &ops.LaunchResult{Target: "mo:@4"}, nil
	})

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"feat","resume_id":"`+sessA+`","agent":"c"}`)
	if got := decodeWindowID(t, rec); got != "@4" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchResumeLiveSessionTouchesNothing(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))
	f.history.EXPECT().TranscriptExists("/ws/alpha", sessA).Return(true)
	// No SessionOps expectations: any tmux call fails the test.

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main","resume_id":"`+sessA+`"}`)
	if got := decodeWindowID(t, rec); got != "@3" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchBusyPrimaryReturnsChoices(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
	var got conflictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Choices, ",") != "switch,replace,sibling" || got.Primary == nil || got.Primary.WindowID != "@3" {
		t.Fatalf("conflict: %+v", got)
	}
}

func TestLaunchModeSwitch(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))
	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main","mode":"switch"}`)
	if got := decodeWindowID(t, rec); got != "@3" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchModeSwitchWithFocus(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))
	f.sessions.EXPECT().SwitchToWindow("mo:@3").Return(nil)
	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main","mode":"switch","focus":true}`)
	if got := decodeWindowID(t, rec); got != "@3" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchModeReplaceParksPrimary(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha [focus]", "@3", sessA, 0))
	f.history.EXPECT().TranscriptExists("/ws/alpha", sessB).Return(true)
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{{SessionID: sessA, PID: 4242}}, nil)
	f.sessions.EXPECT().ParkAndLaunch(ops.ParkParams{
		PID:               4242,
		PrimaryWindowName: "alpha [focus]",
		Cwd:               "/ws/alpha",
		ResumeID:          sessB,
		ShellCmd:          "claude --resume " + sessB,
		AgentKey:          "c",
		NoSwitch:          true,
	}).Return(&ops.LaunchResult{Target: "mo:@11"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main","resume_id":"`+sessB+`","mode":"replace"}`)
	if got := decodeWindowID(t, rec); got != "@11" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchModeSibling(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha.worktrees/feat", "alpha@feat", "@5", sessA, 0))
	f.sessions.EXPECT().LaunchSibling(ops.SiblingParams{
		ProjectName: "alpha",
		Branch:      "feat",
		Cwd:         "/ws/alpha.worktrees/feat",
		ShellCmd:    "gemini",
		AgentKey:    "g",
		NoSwitch:    true,
	}).Return(&ops.LaunchResult{Target: "mo:@12"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"feat","agent":"g","mode":"sibling"}`)
	if got := decodeWindowID(t, rec); got != "@12" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchOccupiedWindowWithoutSessionOffersReplaceOrSibling(t *testing.T) {
	// Window exists but the state file doesn't know its session yet (e.g.
	// launched a second ago) — can't offer "switch".
	f := newSessionFixture(t)
	f.sessions.EXPECT().WindowExists("alpha").Return(true)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
	var got conflictResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if strings.Join(got.Choices, ",") != "replace,sibling" || got.Primary != nil {
		t.Fatalf("conflict: %+v", got)
	}
}

func TestLaunchCreatesWorktreeForUncheckedOutBranch(t *testing.T) {
	f := newSessionFixture(t)
	f.sessions.EXPECT().CreateWorktreeAndLaunch(ops.WorktreeParams{
		ProjectName: "alpha",
		ProjectPath: "/ws/alpha",
		Branch:      "new/thing",
		ShellCmd:    "claude",
		AgentKey:    "c",
		NoSwitch:    true,
	}).Return(&ops.WorktreeResult{Target: "mo:@20", Launched: true}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"new/thing"}`)
	if got := decodeWindowID(t, rec); got != "@20" {
		t.Errorf("window: %q", got)
	}
}

func TestLaunchWorktreeExistsConflict(t *testing.T) {
	f := newSessionFixture(t)
	f.sessions.EXPECT().CreateWorktreeAndLaunch(gomock.Any()).
		Return(&ops.WorktreeResult{ExistsConflict: true, Status: "worktree already exists"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"old"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

func TestLaunchRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"bad resume id":        `{"branch":"main","resume_id":"../../etc/passwd"}`,
		"unknown agent":        `{"branch":"main","agent":"x"}`,
		"unknown mode":         `{"branch":"main","mode":"nuke"}`,
		"flag-like branch":     `{"branch":"--upload-pack=evil"}`,
		"branch with space":    `{"branch":"a b"}`,
		"resume in new branch": `{"branch":"brand-new","resume_id":"` + sessA + `"}`,
		"malformed json":       `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSessionFixture(t)
			rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: want 400, got %d (%s)", rec.Code, rec.Body)
			}
		})
	}
}

func TestLaunchRejectsResumeIDFromAnotherCheckout(t *testing.T) {
	f := newSessionFixture(t)
	f.history.EXPECT().TranscriptExists("/ws/alpha", sessA).Return(false)
	rec := f.do(http.MethodPost, "/api/projects/alpha/sessions", `{"branch":"main","resume_id":"`+sessA+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestLaunchUnknownProject(t *testing.T) {
	f := newSessionFixture(t)
	rec := f.do(http.MethodPost, "/api/projects/nope/sessions", `{"branch":"main"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestStopSession(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))
	live := claude.Session{SessionID: sessA, PID: 4242}
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{{SessionID: sessB, PID: 1}, live}, nil)
	f.sessions.EXPECT().StopSessions([]claude.Session{live}).Return(1)

	rec := f.do(http.MethodDelete, "/api/sessions/@3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestStopSessionUnknownWindow(t *testing.T) {
	f := newSessionFixture(t)
	rec := f.do(http.MethodDelete, "/api/sessions/@99", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestStopSessionAlreadyExited(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha", "@3", sessA, 0))
	f.sessions.EXPECT().LiveSessions().Return(nil, nil)
	rec := f.do(http.MethodDelete, "/api/sessions/@3", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestValidateBranchName(t *testing.T) {
	for _, ok := range []string{"main", "feat/x", "OP-175-fix", "a.b"} {
		if err := validateBranchName(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a..b", "a b", "a~1", "x.lock", "x/", "/x", "a//b", ".hidden", "a/.b", "a@{1}", "a:b", "a\nb"} {
		if err := validateBranchName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
