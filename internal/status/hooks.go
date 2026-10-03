package status

import (
	"encoding/json"
	"fmt"
)

// hookPayload is the unified JSON structure sent by the status-hook.sh script.
// It wraps Claude Code's stdin JSON and adds session context.
type hookPayload struct {
	// New unified format fields.
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	ProjectPath   string          `json:"project_path"`
	CWD           string          `json:"cwd"`
	HookInput     json.RawMessage `json:"hook_input,omitempty"`

	// Legacy format fields (from old notify-hook.sh / stop-hook.sh).
	Type      string `json:"type"`      // "session_stop" for legacy stop hook
	TmuxPane  string `json:"tmux_pane"` // both formats
	Timestamp string `json:"timestamp"` // both formats
}

// legacyNotificationPayload is the JSON Claude provides to Notification hooks.
// Claude Code sends `notification_type` (verified against a captured
// payload, Oct 2026); the camelCase `notificationType` is what the old
// notify-hook.sh sent, still accepted for the legacy format.
type legacyNotificationPayload struct {
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	LegacyType       string `json:"notificationType"`
}

// kind returns the notification type from whichever field carried it.
func (np legacyNotificationPayload) kind() string {
	if np.NotificationType != "" {
		return np.NotificationType
	}
	return np.LegacyType
}

// toolFromHookInput reads tool_name/tool_input out of the forwarded hook
// payload (PreToolUse and PermissionRequest both carry them there, never at
// the top level).
func toolFromHookInput(hookInput json.RawMessage, evt *HookEvent) {
	if len(hookInput) == 0 {
		return
	}
	var tp struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(hookInput, &tp) == nil {
		evt.ToolName = tp.ToolName
		evt.ToolInput = tp.ToolInput
	}
}

// ParseHookPayload parses a hook message (from the Unix socket) into a HookEvent.
// Supports both the new unified format (hook_event_name field) and the legacy
// format (type field from old notify-hook.sh / stop-hook.sh).
func ParseHookPayload(data []byte) (HookEvent, error) {
	var p hookPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return HookEvent{}, fmt.Errorf("parse hook payload: %w", err)
	}

	evt := HookEvent{
		SessionID:   p.SessionID,
		ProjectPath: p.ProjectPath,
	}
	// Claude's own stdin JSON (forwarded wholesale as hook_input) always
	// carries the real session_id. The envelope's copy comes from an env var
	// in status-hook.sh, which for a long time read the wrong name
	// (CLAUDE_SESSION_ID — Claude Code sets CLAUDE_CODE_SESSION_ID) and sent
	// "unknown" for every event, so no hook ever reached its session. Trust
	// hook_input first; it also covers hook scripts installed before the fix.
	if len(p.HookInput) > 0 {
		var in struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(p.HookInput, &in) == nil && in.SessionID != "" {
			evt.SessionID = in.SessionID
		}
	}
	if evt.ProjectPath == "" {
		evt.ProjectPath = p.CWD
	}

	// New unified format: hook_event_name is set.
	if p.HookEventName != "" {
		return parseUnifiedEvent(p, evt)
	}

	// Legacy format: "type" field or Notification hook_input.
	return parseLegacyEvent(p, evt)
}

func parseUnifiedEvent(p hookPayload, evt HookEvent) (HookEvent, error) {
	switch p.HookEventName {
	case "UserPromptSubmit":
		evt.Type = EventUserPromptSubmit
	case "Stop":
		evt.Type = EventStop
	case "PreToolUse":
		evt.Type = EventPreToolUse
		// Claude's own PreToolUse stdin JSON carries tool_name/tool_input;
		// status-hook.sh forwards that JSON wholesale into hook_input rather
		// than promoting fields to the top level, so parse it from there
		// (same pattern as the Notification case below) — a top-level
		// "tool_name" is never actually sent.
		toolFromHookInput(p.HookInput, &evt)
	case "PermissionRequest":
		evt.Type = EventPermissionRequest
		// Claude Code routes AskUserQuestion's menu through the permission
		// flow, so its PermissionRequest names that tool — ProcessHookEvent
		// needs the name to keep such a session a question.
		toolFromHookInput(p.HookInput, &evt)
	case "SessionStart":
		evt.Type = EventSessionStart
		if len(p.HookInput) > 0 {
			var sp struct {
				Source string `json:"source"`
			}
			if json.Unmarshal(p.HookInput, &sp) == nil {
				evt.Source = sp.Source
			}
		}
	case "SessionEnd":
		evt.Type = EventSessionEnd
	case "Notification":
		// Notification hooks carry the notification type inside hook_input.
		if len(p.HookInput) > 0 {
			var np legacyNotificationPayload
			if json.Unmarshal(p.HookInput, &np) == nil {
				switch np.kind() {
				case "idle_prompt":
					evt.Type = EventNotificationIdle
				case "permission_prompt":
					evt.Type = EventNotificationPerm
				default:
					return HookEvent{}, fmt.Errorf("unknown notification type: %q", np.kind())
				}
				return evt, nil
			}
		}
		return HookEvent{}, fmt.Errorf("Notification event missing hook_input")
	default:
		return HookEvent{}, fmt.Errorf("unknown hook event: %q", p.HookEventName)
	}
	return evt, nil
}

func parseLegacyEvent(p hookPayload, evt HookEvent) (HookEvent, error) {
	// Legacy stop hook: {"type":"session_stop", ...}
	if p.Type == "session_stop" {
		evt.Type = EventStop
		return evt, nil
	}

	// Legacy notification hook: {"hook_input": {"message":"...", "notificationType":"..."}, ...}
	if len(p.HookInput) > 0 {
		var np legacyNotificationPayload
		if err := json.Unmarshal(p.HookInput, &np); err == nil && np.kind() != "" {
			switch np.kind() {
			case "idle_prompt":
				evt.Type = EventNotificationIdle
			case "permission_prompt":
				evt.Type = EventNotificationPerm
			default:
				return HookEvent{}, fmt.Errorf("unknown notification type: %q", np.kind())
			}
			return evt, nil
		}
	}

	return HookEvent{}, fmt.Errorf("unrecognized hook payload")
}
