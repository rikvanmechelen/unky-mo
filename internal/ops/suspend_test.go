package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"go.uber.org/mock/gomock"
)

// --- I/O round-trip tests ---

func TestSuspendedStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	want := &SuspendedState{
		Sessions: []SuspendedSession{
			{
				WindowName:  "myproj",
				Cwd:         "/ws/myproj",
				SessionID:   "abc-123",
				AgentKey:    "c",
				ProjectName: "myproj",
			},
			{
				WindowName:  "myproj@feat",
				Cwd:         "/ws/myproj.worktrees/feat",
				SessionID:   "def-456",
				AgentKey:    "g",
				ProjectName: "@feat",
				Parent:      "myproj",
			},
		},
		TmuxSession: "mo",
		SuspendedAt: time.Now().Truncate(time.Second),
	}

	if err := WriteSuspendedState(path, want); err != nil {
		t.Fatalf("WriteSuspendedState: %v", err)
	}

	got, err := ReadSuspendedState(path)
	if err != nil {
		t.Fatalf("ReadSuspendedState: %v", err)
	}

	if len(got.Sessions) != len(want.Sessions) {
		t.Fatalf("session count: want %d, got %d", len(want.Sessions), len(got.Sessions))
	}
	for i, s := range got.Sessions {
		w := want.Sessions[i]
		if s.WindowName != w.WindowName {
			t.Errorf("[%d] WindowName: want %q, got %q", i, w.WindowName, s.WindowName)
		}
		if s.Cwd != w.Cwd {
			t.Errorf("[%d] Cwd: want %q, got %q", i, w.Cwd, s.Cwd)
		}
		if s.SessionID != w.SessionID {
			t.Errorf("[%d] SessionID: want %q, got %q", i, w.SessionID, s.SessionID)
		}
		if s.AgentKey != w.AgentKey {
			t.Errorf("[%d] AgentKey: want %q, got %q", i, w.AgentKey, s.AgentKey)
		}
		if s.ProjectName != w.ProjectName {
			t.Errorf("[%d] ProjectName: want %q, got %q", i, w.ProjectName, s.ProjectName)
		}
		if s.Parent != w.Parent {
			t.Errorf("[%d] Parent: want %q, got %q", i, w.Parent, s.Parent)
		}
	}
	if got.TmuxSession != want.TmuxSession {
		t.Errorf("TmuxSession: want %q, got %q", want.TmuxSession, got.TmuxSession)
	}
}

func TestHasSuspendedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	if HasSuspendedState(path) {
		t.Error("should be false when file does not exist")
	}

	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasSuspendedState(path) {
		t.Error("should be true when file exists")
	}
}

func TestReadSuspendedStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")

	got, err := ReadSuspendedState(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Error("missing file should return nil state")
	}
}

func TestWriteSuspendedStateAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	s := &SuspendedState{
		Sessions:    []SuspendedSession{{WindowName: "p", Cwd: "/p", SessionID: "x"}},
		TmuxSession: "mo",
	}
	if err := WriteSuspendedState(path, s); err != nil {
		t.Fatal(err)
	}

	// The tmp file should be cleaned up.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should be removed after atomic write")
	}

	// The final file should be valid JSON.
	data, _ := os.ReadFile(path)
	var check SuspendedState
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
}

// --- SuspendAll tests ---

func TestSuspendAllHappyPath(t *testing.T) {
	ctx, _, claude := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	// Three sessions with live PIDs — IsAlive returns false immediately
	// (simulates fast exit after SIGINT).
	claude.EXPECT().IsAlive(gomock.Any()).Return(false).AnyTimes()

	sessions := []SessionToStop{
		{SuspendedSession: SuspendedSession{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1"}, PID: 1001},
		{SuspendedSession: SuspendedSession{WindowName: "proj2", Cwd: "/ws/p2", SessionID: "s2"}, PID: 1002},
		{SuspendedSession: SuspendedSession{WindowName: "proj3", Cwd: "/ws/p3", SessionID: "s3"}, PID: 1003},
	}

	res, err := SuspendAll(ctx, path, SuspendParams{
		Sessions:    sessions,
		TmuxSession: "mo",
	})
	if err != nil {
		t.Fatalf("SuspendAll: %v", err)
	}
	if res.Stopped != 3 {
		t.Errorf("Stopped: want 3, got %d", res.Stopped)
	}
	if res.Saved != 3 {
		t.Errorf("Saved: want 3, got %d", res.Saved)
	}

	// Verify file was written.
	state, err := ReadSuspendedState(path)
	if err != nil {
		t.Fatalf("ReadSuspendedState: %v", err)
	}
	if len(state.Sessions) != 3 {
		t.Errorf("saved sessions: want 3, got %d", len(state.Sessions))
	}
	if state.TmuxSession != "mo" {
		t.Errorf("TmuxSession: want %q, got %q", "mo", state.TmuxSession)
	}
}

func TestSuspendAllSkipsDeadPIDs(t *testing.T) {
	ctx, _, claude := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	// Only PID=1001 is "alive"; PID=0 sessions are already dead.
	claude.EXPECT().IsAlive(gomock.Any()).Return(false).AnyTimes()

	sessions := []SessionToStop{
		{SuspendedSession: SuspendedSession{WindowName: "p1", Cwd: "/p1", SessionID: "s1"}, PID: 1001},
		{SuspendedSession: SuspendedSession{WindowName: "p2", Cwd: "/p2", SessionID: "s2"}, PID: 0},
		{SuspendedSession: SuspendedSession{WindowName: "p3", Cwd: "/p3", SessionID: "s3"}, PID: 0},
	}

	res, err := SuspendAll(ctx, path, SuspendParams{Sessions: sessions, TmuxSession: "mo"})
	if err != nil {
		t.Fatal(err)
	}
	// Only the live PID was signaled.
	if res.Stopped != 1 {
		t.Errorf("Stopped: want 1, got %d", res.Stopped)
	}
	// All sessions are saved regardless of PID.
	if res.Saved != 3 {
		t.Errorf("Saved: want 3, got %d", res.Saved)
	}
}

func TestSuspendAllEmptySessions(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	res, err := SuspendAll(ctx, path, SuspendParams{TmuxSession: "mo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != 0 || res.Saved != 0 {
		t.Errorf("empty: want 0/0, got %d/%d", res.Stopped, res.Saved)
	}

	// File should still be written (empty sessions list).
	state, err := ReadSuspendedState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("state file should exist even with 0 sessions")
	}
}

// --- ResumeAll tests ---

// expectLaunchCeremony sets up the standard mock expectations for a single
// LaunchSession call (CreateWindow → SetWindowOption → PaneID → SendKeys →
// SetWindowHook → SplitWindow → SelectPane). No SwitchToWindow — resume
// uses SwitchFocus=false. agentKey may be empty.
func expectLaunchCeremony(tmux *mock_ops.MockTmuxClient, windowName, cwd, shellCmd, agentKey string) {
	tmux.EXPECT().CreateWindow(windowName, cwd).Return("mo:@1", nil)
	tmux.EXPECT().SetWindowOption(gomock.Any(), "@mo_instance_id", gomock.Any()).Return(nil)
	if agentKey != "" {
		tmux.EXPECT().SetWindowOption(gomock.Any(), "@mo_agent", agentKey).Return(nil)
	}
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec "+shellCmd).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), "pane-exited", gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), cwd, gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	// No SwitchToWindow — SwitchFocus=false
}

func TestResumeAllHappyPath(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	// Write a suspended state with 2 sessions.
	state := &SuspendedState{
		Sessions: []SuspendedSession{
			{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1", AgentKey: "c"},
			{WindowName: "proj2", Cwd: "/ws/p2", SessionID: "s2", AgentKey: "c"},
		},
		TmuxSession: "mo",
	}
	if err := WriteSuspendedState(path, state); err != nil {
		t.Fatal(err)
	}

	agents := []config.AgentConfig{
		{Name: "Claude", Key: "c", Cmd: "claude", ResumeCmd: "claude --resume", Default: true},
	}

	// Both windows don't exist yet.
	tmux.EXPECT().WindowExists("proj1").Return(false)
	tmux.EXPECT().WindowExists("proj2").Return(false)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	// Expect full launch ceremony for each.
	expectLaunchCeremony(tmux, "proj1", "/ws/p1", "claude --resume s1", "c")
	expectLaunchCeremony(tmux, "proj2", "/ws/p2", "claude --resume s2", "c")

	res, err := ResumeAll(ctx, path, RestoreParams{Agents: agents})
	if err != nil {
		t.Fatalf("ResumeAll: %v", err)
	}
	if res.Resumed != 2 {
		t.Errorf("Resumed: want 2, got %d", res.Resumed)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped: want 0, got %d", res.Skipped)
	}

	// File should be deleted.
	if HasSuspendedState(path) {
		t.Error("suspended.json should be deleted after resume")
	}
}

func TestResumeAllNoStateFile(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")

	res, err := ResumeAll(ctx, path, RestoreParams{})
	if err != nil {
		t.Fatalf("should not error on missing file: %v", err)
	}
	if res.Resumed != 0 {
		t.Errorf("Resumed: want 0, got %d", res.Resumed)
	}
}

func TestResumeAllPartialFailure(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	state := &SuspendedState{
		Sessions: []SuspendedSession{
			{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1"},
			{WindowName: "proj2", Cwd: "/ws/p2", SessionID: "s2"},
		},
		TmuxSession: "mo",
	}
	WriteSuspendedState(path, state)

	agents := config.DefaultAgents()

	tmux.EXPECT().WindowExists("proj1").Return(false)
	tmux.EXPECT().WindowExists("proj2").Return(false)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	// Session 1 launches fine (no agent key → no @mo_agent call).
	expectLaunchCeremony(tmux, "proj1", "/ws/p1", "claude --resume s1", "")

	// Session 2 fails at CreateWindow.
	tmux.EXPECT().CreateWindow("proj2", "/ws/p2").Return("", fmt.Errorf("tmux: duplicate window"))

	res, err := ResumeAll(ctx, path, RestoreParams{Agents: agents})
	// Partial failure is not a top-level error.
	if err != nil {
		t.Fatalf("partial failure should not be a top-level error: %v", err)
	}
	if res.Resumed != 1 {
		t.Errorf("Resumed: want 1, got %d", res.Resumed)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped: want 1, got %d", res.Skipped)
	}
	if len(res.Errors) != 1 {
		t.Errorf("Errors: want 1, got %d", len(res.Errors))
	}

	// File should still be deleted even on partial failure.
	if HasSuspendedState(path) {
		t.Error("suspended.json should be deleted even on partial failure")
	}
}

func TestResumeAllSkipsExistingWindows(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	state := &SuspendedState{
		Sessions: []SuspendedSession{
			{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1"},
		},
		TmuxSession: "mo",
	}
	WriteSuspendedState(path, state)

	agents := config.DefaultAgents()

	// Window already exists — skip it.
	tmux.EXPECT().WindowExists("proj1").Return(true)

	res, err := ResumeAll(ctx, path, RestoreParams{Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped: want 1, got %d", res.Skipped)
	}
	if res.Resumed != 0 {
		t.Errorf("Resumed: want 0, got %d", res.Resumed)
	}
}

func TestResumeAllAgentLookup(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	state := &SuspendedState{
		Sessions: []SuspendedSession{
			{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1", AgentKey: "g"},
		},
		TmuxSession: "mo",
	}
	WriteSuspendedState(path, state)

	agents := []config.AgentConfig{
		{Name: "Claude", Key: "c", Cmd: "claude", ResumeCmd: "claude --resume", Default: true},
		{Name: "Gemini", Key: "g", Cmd: "gemini", ResumeCmd: "gemini --resume"},
	}

	tmux.EXPECT().WindowExists("proj1").Return(false)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	// Should use gemini's resume command, not claude's.
	expectLaunchCeremony(tmux, "proj1", "/ws/p1", "gemini --resume s1", "g")

	res, err := ResumeAll(ctx, path, RestoreParams{Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resumed != 1 {
		t.Errorf("Resumed: want 1, got %d", res.Resumed)
	}
}

func TestResumeAllDefaultAgentFallback(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "suspended.json")

	// Session has an agent key that doesn't match any configured agent.
	state := &SuspendedState{
		Sessions: []SuspendedSession{
			{WindowName: "proj1", Cwd: "/ws/p1", SessionID: "s1", AgentKey: "z"},
		},
		TmuxSession: "mo",
	}
	WriteSuspendedState(path, state)

	agents := config.DefaultAgents() // only Claude with key "c"

	tmux.EXPECT().WindowExists("proj1").Return(false)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	// Falls back to default agent (Claude). AgentKey "z" is still passed
	// through to LaunchSession even though the agent wasn't found — it's
	// the session's stored key.
	expectLaunchCeremony(tmux, "proj1", "/ws/p1", "claude --resume s1", "z")

	res, err := ResumeAll(ctx, path, RestoreParams{Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resumed != 1 {
		t.Errorf("Resumed: want 1, got %d", res.Resumed)
	}
}
