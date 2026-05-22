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

func TestHookEvent_SessionStart_SetsActive(t *testing.T) {
	mgr := NewManager()
	mgr.ProcessHookEvent(HookEvent{Type: EventSessionStart, SessionID: "s1"})
	if got := mgr.Status("s1"); got != StatusActive {
		t.Errorf("after SessionStart: got %v, want StatusActive", got)
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
