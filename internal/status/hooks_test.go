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
	data := []byte(`{"hook_event_name":"PreToolUse","session_id":"s1","project_path":"/ws","tool_name":"Bash"}`)
	evt, err := ParseHookPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventPreToolUse || evt.ToolName != "Bash" {
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
