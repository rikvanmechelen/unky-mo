package status

import (
	"testing"
)

func TestParseHookPayload_UserPromptSubmit(t *testing.T) {
	data := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","project_path":"/ws/proj"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventUserPromptSubmit || evt.SessionID != "s1" || evt.ProjectPath != "/ws/proj" {
		t.Errorf("got %+v", evt)
	}
}

func TestParseHookPayload_Stop(t *testing.T) {
	data := []byte(`{"hook_event_name":"Stop","session_id":"s1","cwd":"/ws/proj"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventStop {
		t.Errorf("got %v, want EventStop", evt.Type)
	}
	// cwd should fall back to project_path.
	if evt.ProjectPath != "/ws/proj" {
		t.Errorf("ProjectPath: got %q", evt.ProjectPath)
	}
}

func TestParseHookPayload_PreToolUse(t *testing.T) {
	// Real wire shape: status-hook.sh forwards Claude's entire PreToolUse
	// stdin JSON wholesale into hook_input — tool_name is never promoted to
	// the top level.
	data := []byte(`{"hook_event_name":"PreToolUse","session_id":"s1","project_path":"/ws","hook_input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventPreToolUse || evt.ToolName != "Bash" {
		t.Errorf("got %+v", evt)
	}
	if string(evt.ToolInput) != `{"command":"ls"}` {
		t.Errorf("ToolInput: got %s", evt.ToolInput)
	}
}

func TestParseHookPayload_PreToolUse_MissingHookInput(t *testing.T) {
	// No hook_input at all — should still parse as PreToolUse, just with an
	// empty tool name/input, not error out.
	data := []byte(`{"hook_event_name":"PreToolUse","session_id":"s1","project_path":"/ws"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventPreToolUse || evt.ToolName != "" {
		t.Errorf("got %+v", evt)
	}
}

func TestParseHookPayload_PermissionRequest(t *testing.T) {
	data := []byte(`{"hook_event_name":"PermissionRequest","session_id":"s1","project_path":"/ws"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventPermissionRequest {
		t.Errorf("got %v", evt.Type)
	}
}

func TestParseHookPayload_SessionStart(t *testing.T) {
	data := []byte(`{"hook_event_name":"SessionStart","session_id":"s1","project_path":"/ws"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventSessionStart {
		t.Errorf("got %v", evt.Type)
	}
}

func TestParseHookPayload_SessionStartSource(t *testing.T) {
	data := []byte(`{"hook_event_name":"SessionStart","session_id":"s1","hook_input":{"hook_event_name":"SessionStart","source":"compact"}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventSessionStart || evt.Source != "compact" {
		t.Errorf("got type %v source %q", evt.Type, evt.Source)
	}
}

func TestParseHookPayload_SessionEnd(t *testing.T) {
	data := []byte(`{"hook_event_name":"SessionEnd","session_id":"s1","project_path":"/ws"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventSessionEnd {
		t.Errorf("got %v", evt.Type)
	}
}

func TestParseHookPayload_Notification_IdlePrompt(t *testing.T) {
	data := []byte(`{"hook_event_name":"Notification","session_id":"s1","project_path":"/ws","hook_input":{"message":"needs input","notificationType":"idle_prompt"}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventNotificationIdle {
		t.Errorf("got %v, want EventNotificationIdle", evt.Type)
	}
}

func TestParseHookPayload_Notification_PermissionPrompt(t *testing.T) {
	data := []byte(`{"hook_event_name":"Notification","session_id":"s1","project_path":"/ws","hook_input":{"message":"needs perm","notificationType":"permission_prompt"}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventNotificationPerm {
		t.Errorf("got %v, want EventNotificationPerm", evt.Type)
	}
}

func TestParseHookPayload_UnknownEvent_ReturnsError(t *testing.T) {
	data := []byte(`{"hook_event_name":"FutureEvent","session_id":"s1"}`)
	_, err := ParseHookPayload(data)
	if err == nil {
		t.Error("expected error for unknown event")
	}
}

func TestParseHookPayload_LegacyStopFormat(t *testing.T) {
	// Old stop-hook.sh format.
	data := []byte(`{"type":"session_stop","session_id":"s1","project_path":"/ws/proj","tmux_pane":"%5","timestamp":"2026-05-22T12:00:00Z"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventStop || evt.SessionID != "s1" {
		t.Errorf("legacy stop: got %+v", evt)
	}
}

func TestParseHookPayload_LegacyNotificationFormat(t *testing.T) {
	// Old notify-hook.sh format.
	data := []byte(`{"hook_input":{"message":"needs input","notificationType":"idle_prompt"},"session_id":"s1","project_path":"/ws/proj","tmux_pane":"%5","timestamp":"2026-05-22T12:00:00Z"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventNotificationIdle || evt.SessionID != "s1" {
		t.Errorf("legacy notification: got %+v", evt)
	}
}

func TestParseHookPayload_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := ParseHookPayload([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseHookPayload_EmptyPayload_ReturnsError(t *testing.T) {
	_, err := ParseHookPayload([]byte(`{}`))
	if err == nil {
		t.Error("expected error for empty payload")
	}
}

// The envelope's session_id comes from an env var in status-hook.sh that used
// to be the wrong name, yielding "unknown" — the session_id inside Claude's own
// stdin JSON (hook_input) is authoritative.
func TestParseHookPayload_SessionIDFromHookInput(t *testing.T) {
	data := []byte(`{"hook_event_name":"Stop","session_id":"unknown","hook_input":{"hook_event_name":"Stop","session_id":"real-id"}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.SessionID != "real-id" {
		t.Errorf("session id: got %q, want %q", evt.SessionID, "real-id")
	}
}

func TestParseHookPayload_SessionIDFallsBackToEnvelope(t *testing.T) {
	data := []byte(`{"hook_event_name":"Stop","session_id":"env-id","hook_input":{"hook_event_name":"Stop"}}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.SessionID != "env-id" {
		t.Errorf("session id: got %q, want %q", evt.SessionID, "env-id")
	}
}
