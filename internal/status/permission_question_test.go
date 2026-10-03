package status

import "testing"

// Hook messages as status-hook.sh delivers them: Claude Code's own stdin
// JSON wrapped under hook_input. Trimmed from payloads captured live from
// Claude Code 2.1.288 (MO_HOOK_DEBUG_LOG, Oct 2026) — only paths and ids
// shortened; field names and nesting are exactly what was sent.
const (
	capturedPreToolUseAsk = `{"hook_input":{"session_id":"s1","cwd":"/ws","permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which colour?","header":"Colour","options":[{"label":"Red","description":"Red"},{"label":"Blue","description":"Blue"}],"multiSelect":false}]},"tool_use_id":"toolu_1"},"hook_event_name":"PreToolUse","session_id":"s1","project_path":"/ws"}`
	capturedPermissionAsk = `{"hook_input":{"session_id":"s1","cwd":"/ws","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which colour?","header":"Colour","options":[{"label":"Red","description":"Red"},{"label":"Blue","description":"Blue"}],"multiSelect":false}]}},"hook_event_name":"PermissionRequest","session_id":"s1","project_path":"/ws"}`
	capturedPreToolUseBash = `{"hook_input":{"session_id":"s1","cwd":"/ws","permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"touch /tmp/probe","description":"Create probe file"},"tool_use_id":"toolu_2"},"hook_event_name":"PreToolUse","session_id":"s1","project_path":"/ws"}`
	capturedPermissionBash = `{"hook_input":{"session_id":"s1","cwd":"/ws","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /tmp/probe","description":"Create probe file"},"permission_suggestions":[{"type":"addDirectories","directories":["/tmp"],"destination":"session"}]},"hook_event_name":"PermissionRequest","session_id":"s1","project_path":"/ws"}`
	capturedNotificationPerm = `{"hook_input":{"session_id":"s1","cwd":"/ws","hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"},"hook_event_name":"Notification","session_id":"s1","project_path":"/ws"}`
	notificationIdle = `{"hook_input":{"session_id":"s1","cwd":"/ws","hook_event_name":"Notification","message":"Claude is waiting for your input","notification_type":"idle_prompt"},"hook_event_name":"Notification","session_id":"s1","project_path":"/ws"}`
)

func feed(t *testing.T, mgr *Manager, msgs ...string) {
	t.Helper()
	for _, m := range msgs {
		evt, err := ParseHookPayload([]byte(m))
		if err != nil {
			t.Fatalf("ParseHookPayload(%s): %v", m[:60], err)
		}
		mgr.ProcessHookEvent(evt)
	}
}

// Regression: AskUserQuestion's menu goes through Claude Code's permission
// flow, so its PermissionRequest (and the tool-less permission_prompt
// notification after it) used to overwrite the question with "permission"
// — mo web then showed a permission lock instead of the question banner.
func TestAskUserQuestionStaysAQuestionThroughPermissionHooks(t *testing.T) {
	mgr := NewManager()
	feed(t, mgr, capturedPreToolUseAsk, capturedPermissionAsk, capturedNotificationPerm)

	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Fatalf("status: got %v, want StatusQuestion", got)
	}
	tool, input, ok := mgr.PendingQuestion("s1")
	if !ok || tool != "AskUserQuestion" || len(input) == 0 {
		t.Errorf("pending question lost: tool=%q ok=%v input=%s", tool, ok, input)
	}
}

// The PermissionRequest alone (PreToolUse missed) is enough to show the
// question with its content.
func TestPermissionRequestForAskUserQuestionIsAQuestion(t *testing.T) {
	mgr := NewManager()
	feed(t, mgr, capturedPermissionAsk)
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Fatalf("status: got %v, want StatusQuestion", got)
	}
	if tool, _, ok := mgr.PendingQuestion("s1"); !ok || tool != "AskUserQuestion" {
		t.Errorf("pending question: tool=%q ok=%v", tool, ok)
	}
}

// A genuine permission prompt (same hook sequence, a non-interactive tool)
// is still a permission prompt.
func TestGenuinePermissionPromptIsPermission(t *testing.T) {
	mgr := NewManager()
	feed(t, mgr, capturedPreToolUseBash, capturedPermissionBash, capturedNotificationPerm)
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Fatalf("status: got %v, want StatusPermission", got)
	}
}

// A real permission prompt right after a question was answered: its
// PermissionRequest names the tool, so it wins over the old question.
func TestPermissionAfterQuestionIsPermission(t *testing.T) {
	mgr := NewManager()
	feed(t, mgr, capturedPreToolUseAsk, capturedPermissionAsk, capturedPermissionBash)
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Fatalf("status: got %v, want StatusPermission", got)
	}
}

// "Waiting for your input" must not clear an open menu (mo web would then
// let you type into it); it still marks an ordinary session idle.
func TestIdleNotificationDoesNotClearOpenMenus(t *testing.T) {
	mgr := NewManager()
	feed(t, mgr, capturedPreToolUseAsk, notificationIdle)
	if got := mgr.Status("s1"); got != StatusQuestion {
		t.Errorf("question: got %v, want StatusQuestion", got)
	}

	mgr = NewManager()
	feed(t, mgr, capturedPreToolUseBash, capturedPermissionBash, notificationIdle)
	if got := mgr.Status("s1"); got != StatusPermission {
		t.Errorf("permission: got %v, want StatusPermission", got)
	}

	mgr = NewManager()
	feed(t, mgr, capturedPreToolUseBash, notificationIdle)
	if got := mgr.Status("s1"); got != StatusIdle {
		t.Errorf("active session: got %v, want StatusIdle", got)
	}
}

// The notification type field Claude Code actually sends is snake_case
// notification_type; the camelCase legacy field still parses.
func TestNotificationTypeFieldNames(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want HookEventType
	}{
		{capturedNotificationPerm, EventNotificationPerm},
		{notificationIdle, EventNotificationIdle},
		{`{"hook_event_name":"Notification","session_id":"s1","hook_input":{"notificationType":"idle_prompt"}}`, EventNotificationIdle},
	} {
		evt, err := ParseHookPayload([]byte(c.msg))
		if err != nil || evt.Type != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.msg[:70], evt.Type, err, c.want)
		}
	}
}
