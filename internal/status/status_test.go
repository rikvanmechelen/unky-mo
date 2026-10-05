package status

import (
	"sync"
	"testing"
	"time"
)

func TestNewManager_EmptyStatus(t *testing.T) {
	mgr := NewManager()
	if got := mgr.Status("unknown"); got != StatusNone {
		t.Errorf("Status of unknown session: got %v, want StatusNone", got)
	}
}

func TestHookEvent_UserPromptSubmit_SetsActive(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("after UserPromptSubmit: got %v, want StatusActive", got)
	}
}

func TestHookEvent_Stop_SetsIdle(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("after Stop: got %v, want StatusIdle", got)
	}
}

func TestHookEvent_PermissionRequest_SetsPermission(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventPermissionRequest, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("after PermissionRequest: got %v, want StatusPermission", got)
	}
}

func TestHookEvent_PreToolUse_ReaffirmsActive(t *testing.T) {
	mgr := NewManager()
	// Already active — stays active.
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "Bash"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("PreToolUse on active: got %v, want StatusActive", got)
	}
}

func TestHookEvent_PreToolUse_RecoverFromIdle(t *testing.T) {
	mgr := NewManager()
	// Idle (missed UserPromptSubmit) — PreToolUse recovers to active.
	mgr.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Fatalf("setup: got %v, want StatusIdle", got)
	}
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("PreToolUse on idle: got %v, want StatusActive", got)
	}
}

func TestHookEvent_PreToolUse_AskUserQuestion_SetsQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{
		Type: EventPreToolUse, SessionID: "s1",
		ToolName: "AskUserQuestion", ToolInput: []byte(`{"questions":[{"question":"Which?"}]}`),
	})
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Errorf("after PreToolUse(AskUserQuestion): got %v, want StatusQuestion", got)
	}
	tool, input, ok := mgr.PendingQuestion("s1")
	if !ok || tool != "AskUserQuestion" || string(input) != `{"questions":[{"question":"Which?"}]}` {
		t.Errorf("PendingQuestion: got tool=%q input=%s ok=%v", tool, input, ok)
	}
}

func TestHookEvent_PreToolUse_OrdinaryTool_NoPendingQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "Bash"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("after PreToolUse(Bash): got %v, want StatusActive", got)
	}
	if _, _, ok := mgr.PendingQuestion("s1"); ok {
		t.Error("expected no pending question for an ordinary tool")
	}
}

func TestHookEvent_PreToolUse_ConsecutiveQuestions_RefreshesContent(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{
		Type: EventPreToolUse, SessionID: "s1",
		ToolName: "AskUserQuestion", ToolInput: []byte(`{"questions":[{"question":"First?"}]}`),
	})
	// A second AskUserQuestion while already in StatusQuestion: no status
	// transition, but the pending content must still refresh.
	mgr.ProcessHookEvent(HookEvent{
		Type: EventPreToolUse, SessionID: "s1",
		ToolName: "AskUserQuestion", ToolInput: []byte(`{"questions":[{"question":"Second?"}]}`),
	})
	_, input, ok := mgr.PendingQuestion("s1")
	if !ok || string(input) != `{"questions":[{"question":"Second?"}]}` {
		t.Errorf("expected refreshed pending question, got %s (ok=%v)", input, ok)
	}
}

func TestHookEvent_Stop_ClearsPendingQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{
		Type: EventPreToolUse, SessionID: "s1",
		ToolName: "AskUserQuestion", ToolInput: []byte(`{"questions":[{"question":"Which?"}]}`),
	})
	mgr.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Fatalf("after Stop: got %v, want StatusIdle", got)
	}
	if _, _, ok := mgr.PendingQuestion("s1"); ok {
		t.Error("expected pending question cleared after Stop")
	}
}

// A session starts at the prompt: startup, --resume and /clear all land on
// "waiting for input", so they must read idle (otherwise a fresh session
// shows "Working" and the web composer refuses its first message).
func TestHookEvent_SessionStart_SetsIdle(t *testing.T) {
	for _, source := range []string{"startup", "resume", "clear", ""} {
		mgr := NewManager()
		mgr.ProcessHookEvent(HookEvent{Type: EventSessionStart, SessionID: "s1", Source: source})
		if got := mgr.Status("s1"); got != StatusIdle {
			t.Errorf("after SessionStart(%q): got %v, want StatusIdle", source, got)
		}
	}
}

// Auto-compaction fires SessionStart mid-turn and Claude keeps working.
func TestHookEvent_SessionStartCompact_StaysActive(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventSessionStart, SessionID: "s1", Source: "compact"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("after compaction SessionStart: got %v, want StatusActive", got)
	}
}

func TestHookEvent_SessionEnd_RemovesSession(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventSessionEnd, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusNone {
		t.Errorf("after SessionEnd: got %v, want StatusNone", got)
	}
}

func TestHookEvent_NotificationIdle(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventNotificationIdle, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("after NotificationIdle: got %v, want StatusIdle", got)
	}
}

func TestHookEvent_NotificationPermission(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventNotificationPerm, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("after NotificationPermission: got %v, want StatusPermission", got)
	}
}

func TestMarkDead_RemovesSession(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.MarkDead("s1")
	if got := mgr.Status("s1"); got != StatusNone {
		t.Errorf("after MarkDead: got %v, want StatusNone", got)
	}
}

func TestMarkDead_UnknownSession_Noop(t *testing.T) {
	mgr := NewManager()
	mgr.MarkDead("nonexistent") // should not panic
}

func TestSubscribe_ReceivesChanges(t *testing.T) {
	mgr := NewManager()
	ch := mgr.Subscribe()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})

	select {
	case change := <-ch:
		if change.SessionID != "s1" || change.Old != StatusNone || change.New != StatusActive {
			t.Errorf("unexpected change: %+v", change)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for status change")
	}
}

func TestSubscribe_NoChangeNoDuplicate(t *testing.T) {
	mgr := NewManager()
	ch := mgr.Subscribe()

	// First event: None→Active (emitted).
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	<-ch

	// Second event: Active→Active (not emitted).
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1"})

	select {
	case change := <-ch:
		t.Errorf("should not emit on same-status transition, got %+v", change)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestSubscribe_SessionEnd_EmitsRemoval(t *testing.T) {
	mgr := NewManager()
	ch := mgr.Subscribe()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	<-ch // drain Active

	mgr.ProcessHookEvent(HookEvent{Type: EventSessionEnd, SessionID: "s1"})
	select {
	case change := <-ch:
		if change.Old != StatusActive || change.New != StatusNone {
			t.Errorf("SessionEnd change: %+v", change)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for SessionEnd change")
	}
}

func TestReconcile_OverridesStaleHookState(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusIdle }

	// Hook says Active.
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Fatalf("setup: got %v", got)
	}

	// JSONL says end_turn → correct to Idle.
	mgr.ProcessJSONLChange("s1", "/fake/path.jsonl")
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("after reconcile: got %v, want StatusIdle", got)
	}
}

func TestReconcile_DoesNotDowngradePermission(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusIdle }

	mgr.ProcessHookEvent(HookEvent{Type: EventPermissionRequest, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Fatalf("setup: got %v", got)
	}

	// JSONL says Idle, but Permission wins.
	mgr.ProcessJSONLChange("s1", "/fake/path.jsonl")
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("after reconcile: got %v, want StatusPermission (hooks win)", got)
	}
}

func TestReconcile_CreatesSessionFromJSONL(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusActive }

	// No hooks received yet — JSONL creates the session.
	mgr.ProcessJSONLChange("s1", "/fake/path.jsonl")
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("JSONL-created session: got %v, want StatusActive", got)
	}
}

func TestReconcile_NoneFromJSONL_NoChange(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusNone }

	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessJSONLChange("s1", "/fake/path.jsonl")
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("JSONL StatusNone should not change state: got %v", got)
	}
}

func TestAllStatuses(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s2"})

	all := mgr.AllStatuses()
	if len(all) != 2 {
		t.Fatalf("AllStatuses: got %d entries, want 2", len(all))
	}
	if all["s1"] != StatusActive {
		t.Errorf("s1: got %v, want StatusActive", all["s1"])
	}
	if all["s2"] != StatusIdle {
		t.Errorf("s2: got %v, want StatusIdle", all["s2"])
	}
}

func TestStatusString(t *testing.T) {
	cases := map[SessionStatus]string{
		StatusNone:       "none",
		StatusActive:     "active",
		StatusIdle:       "idle",
		StatusPermission: "permission",
		StatusQuestion:   "question",
		StatusExternal:   "external",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

func TestConcurrency_SafeUnderParallelWrites(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusIdle }
	ch := mgr.Subscribe()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "s1"
			if i%3 == 0 {
				mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: id})
			} else if i%3 == 1 {
				mgr.ProcessJSONLChange(id, "/fake/path.jsonl")
			} else {
				mgr.Status(id)
			}
		}(i)
	}
	wg.Wait()

	// Drain subscriber — just checking no panics occurred.
	// (Can't close a receive-only channel; just drain what's buffered.)
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestReconcile_DoesNotDowngradeQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.readJSONL = func(string) SessionStatus { return StatusActive }

	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: []byte(`{"questions":[]}`)})

	// Claude appends metadata (titles, attachments) while the menu is open.
	mgr.ProcessJSONLChange("s1", "/fake/path.jsonl")
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Errorf("after reconcile: got %v, want StatusQuestion", got)
	}
	if tool, _, ok := mgr.PendingQuestion("s1"); !ok || tool != "AskUserQuestion" {
		t.Errorf("pending question lost: tool=%q ok=%v", tool, ok)
	}
}

func TestAgentStatus_WaitingInputNeeded_SetsQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})

	mgr.ProcessAgentStatus("s1", "waiting", "input needed", time.Now())
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Fatalf("got %v, want StatusQuestion", got)
	}
	if tool, input, ok := mgr.PendingQuestion("s1"); !ok || tool != "" || input != nil {
		t.Errorf("PendingQuestion = (%q, %s, %v), want empty content with ok", tool, input, ok)
	}
}

func TestAgentStatus_UnknownSession_WaitingInputNeeded_SetsQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessAgentStatus("s1", "waiting", "input needed", time.Now())
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Errorf("got %v, want StatusQuestion", got)
	}
}

func TestAgentStatus_WaitingPermissionPrompt_SetsPermission(t *testing.T) {
	mgr := NewManager()
	// After a TUI restart the JSONL reads the open tool_use as active.
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessAgentStatus("s1", "waiting", "permission prompt", time.Now())
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Fatalf("got %v, want StatusPermission", got)
	}

	mgr.ProcessAgentStatus("s1", "busy", "", time.Now())
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("after busy: got %v, want StatusActive", got)
	}
	mgr.ProcessAgentStatus("s1", "waiting", "permission prompt", time.Now())
	mgr.ProcessAgentStatus("s1", "idle", "", time.Now())
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("after idle: got %v, want StatusIdle", got)
	}
}

func TestAgentStatus_PermissionPromptKeepsQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: []byte(`{"q":1}`)})
	mgr.ProcessAgentStatus("s1", "waiting", "permission prompt", time.Now())
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Errorf("got %v, want StatusQuestion", got)
	}
}

func TestAgentStatus_HookPermissionSurvivesBusyAndIdle(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPermissionRequest, SessionID: "s1", ToolName: "Bash"})
	mgr.ProcessAgentStatus("s1", "busy", "", time.Now())
	mgr.ProcessAgentStatus("s1", "idle", "", time.Now())
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("got %v, want StatusPermission (hooks own a hook-sourced permission)", got)
	}
}

func TestAgentStatus_OtherWaitingReasons_Ignored(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessAgentStatus("s1", "waiting", "dialog open", time.Now())
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("got %v, want StatusActive", got)
	}
}

func TestAgentStatus_DoesNotOverridePermission(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPermissionRequest, SessionID: "s1"})
	mgr.ProcessAgentStatus("s1", "waiting", "input needed", time.Now())
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("got %v, want StatusPermission", got)
	}
}

func TestAgentStatus_KeepsHookQuestionContent(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: []byte(`{"q":1}`)})
	mgr.ProcessAgentStatus("s1", "waiting", "input needed", time.Now())
	if tool, input, _ := mgr.PendingQuestion("s1"); tool != "AskUserQuestion" || string(input) != `{"q":1}` {
		t.Errorf("content replaced: tool=%q input=%s", tool, input)
	}
}

func TestAgentStatus_StaleSnapshotIgnored(t *testing.T) {
	mgr := NewManager()
	observedAt := time.Now()
	mgr.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s1"}) // hook lands after the snapshot
	mgr.ProcessAgentStatus("s1", "waiting", "input needed", observedAt)
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("got %v, want StatusIdle (hook is fresher)", got)
	}
}

func TestAgentStatus_Busy_ClearsOnlyAgentSourcedQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessAgentStatus("agent", "waiting", "input needed", time.Now())
	mgr.ProcessAgentStatus("agent", "busy", "", time.Now())
	if got := mgr.Status("agent"); got != StatusActive {
		t.Errorf("agent-sourced: got %v, want StatusActive", got)
	}

	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "hook", ToolName: "AskUserQuestion"})
	mgr.ProcessAgentStatus("hook", "busy", "", time.Now())
	if got := mgr.Status("hook"); got != StatusQuestion {
		t.Errorf("hook-sourced: got %v, want StatusQuestion (busy may predate the menu)", got)
	}
}

func TestAgentStatus_Idle_ClearsAnyQuestion(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "s1", ToolName: "AskUserQuestion"})
	mgr.ProcessAgentStatus("s1", "idle", "", time.Now())
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("got %v, want StatusIdle", got)
	}
	if _, _, ok := mgr.PendingQuestion("s1"); ok {
		t.Error("pending question should be cleared")
	}
}

func TestAgentStatus_NonQuestion_NotTouched(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventUserPromptSubmit, SessionID: "s1"})
	mgr.ProcessAgentStatus("s1", "idle", "", time.Now())
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("got %v, want StatusActive (JSONL/hooks own non-question states)", got)
	}
}
