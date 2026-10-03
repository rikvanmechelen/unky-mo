package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/ops"
	"go.uber.org/mock/gomock"
)

const featPath = "/ws/alpha.worktrees/feat"

func TestCleanupRemovesWorktree(t *testing.T) {
	f := newSessionFixture(t)
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{{SessionID: sessA, CWD: "/ws/alpha"}}, nil)
	f.sessions.EXPECT().CleanupWorktree(ops.CleanupParams{ProjectPath: "/ws/alpha", Branch: "feat"}).
		Return(&ops.CleanupResult{Status: "Cleanup: feat (worktree removed)"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"feat"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestCleanupWithLiveSessionsNeedsConfirmation(t *testing.T) {
	f := newSessionFixture(t)
	live := claude.Session{SessionID: sessA, CWD: featPath, PID: 7}
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{live}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"feat"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
	var got cleanupConflict
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Live != 1 {
		t.Fatalf("conflict: %+v", got)
	}
}

func TestCleanupStopsConfirmedSessionsAndDeletesBranch(t *testing.T) {
	f := newSessionFixture(t)
	live := claude.Session{SessionID: sessA, CWD: featPath, PID: 7}
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{live}, nil)
	f.sessions.EXPECT().CleanupWorktree(ops.CleanupParams{
		ProjectPath:  "/ws/alpha",
		Branch:       "feat",
		DeleteBranch: true,
		Sessions:     []claude.Session{live},
	}).Return(&ops.CleanupResult{}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"feat","delete_branch":true,"stop_sessions":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestCleanupBranchWithoutWorktree(t *testing.T) {
	f := newSessionFixture(t)
	f.sessions.EXPECT().CleanupWorktree(ops.CleanupParams{ProjectPath: "/ws/alpha", Branch: "old", DeleteBranch: true}).
		Return(&ops.CleanupResult{}, nil)
	rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"old","delete_branch":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}

	// Without delete_branch there's nothing to remove.
	rec = f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"old"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}

func TestCleanupRefusesMainAndUnknown(t *testing.T) {
	f := newSessionFixture(t)
	if rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"main","delete_branch":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("main: want 400, got %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/projects/alpha/cleanup", `{"branch":"nope"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: want 404, got %d", rec.Code)
	}
}

func TestLiftLiveSessionStopsItAndMoves(t *testing.T) {
	f := newSessionFixture(t, liveRow("/ws/alpha", "alpha [focus]", "@3", sessA, 0))
	f.history.EXPECT().TranscriptExists("/ws/alpha", sessA).Return(true)
	f.sessions.EXPECT().IsDirty("/ws/alpha").Return(false, nil)
	f.sessions.EXPECT().LiveSessions().Return([]claude.Session{{SessionID: sessA, PID: 4242}}, nil)
	f.sessions.EXPECT().LiftSessionToWorktree(ops.LiftParams{
		ProjectName:  "alpha",
		SourcePath:   "/ws/alpha",
		SessionID:    sessA,
		SourcePID:    4242,
		SourceWindow: "alpha [focus]",
		NewBranch:    "spin-off",
	}).Return(&ops.LiftResult{NewWorktreePath: "/ws/alpha.worktrees/spin-off"}, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/lift", `{"branch":"main","session_id":"`+sessA+`","new_branch":"spin-off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestLiftDirtySourceAsksFirst(t *testing.T) {
	f := newSessionFixture(t)
	f.history.EXPECT().TranscriptExists("/ws/alpha", sessA).Return(true).Times(2)
	f.sessions.EXPECT().IsDirty("/ws/alpha").Return(true, nil)

	rec := f.do(http.MethodPost, "/api/projects/alpha/lift", `{"branch":"main","session_id":"`+sessA+`","new_branch":"spin-off"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}

	// Choosing "stash" skips the question and carries the changes along.
	f.sessions.EXPECT().LiftSessionToWorktree(gomock.Any()).DoAndReturn(func(p ops.LiftParams) (*ops.LiftResult, error) {
		if !p.StashAndPop || p.SourcePID != 0 || p.SourceWindow != "" {
			t.Errorf("lift params: %+v", p)
		}
		return &ops.LiftResult{}, nil
	})
	rec = f.do(http.MethodPost, "/api/projects/alpha/lift", `{"branch":"main","session_id":"`+sessA+`","new_branch":"spin-off","dirty":"stash"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestLiftRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"bad session id": `{"branch":"main","session_id":"x","new_branch":"a"}`,
		"bad branch":     `{"branch":"main","session_id":"` + sessA + `","new_branch":"-x"}`,
		"bad dirty mode": `{"branch":"main","session_id":"` + sessA + `","new_branch":"a","dirty":"nuke"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSessionFixture(t)
			if rec := f.do(http.MethodPost, "/api/projects/alpha/lift", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status: want 400, got %d", rec.Code)
			}
		})
	}
}

func TestLiftUnknownSessionInCheckout(t *testing.T) {
	f := newSessionFixture(t)
	f.history.EXPECT().TranscriptExists(featPath, sessA).Return(false)
	rec := f.do(http.MethodPost, "/api/projects/alpha/lift", `{"branch":"feat","session_id":"`+sessA+`","new_branch":"a"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}
