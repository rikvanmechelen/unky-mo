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
	ToolName      string          `json:"tool_name,omitempty"`
	HookInput     json.RawMessage `json:"hook_input,omitempty"`

	// Legacy format fields (from old notify-hook.sh / stop-hook.sh).
	Type      string `json:"type"`      // "session_stop" for legacy stop hook
	TmuxPane  string `json:"tmux_pane"` // both formats
	Timestamp string `json:"timestamp"` // both formats
}

// legacyNotificationPayload is the JSON Claude provides to Notification hooks.
type legacyNotificationPayload struct {
	Message          string `json:"message"`
	NotificationType string `json:"notificationType"`
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
		evt.ToolName = p.ToolName
	case "PermissionRequest":
		evt.Type = EventPermissionRequest
	case "SessionStart":
		evt.Type = EventSessionStart
	case "SessionEnd":
		evt.Type = EventSessionEnd
	case "Notification":
		// Notification hooks carry the notification type inside hook_input.
		if len(p.HookInput) > 0 {
			var np legacyNotificationPayload
			if json.Unmarshal(p.HookInput, &np) == nil {
				switch np.NotificationType {
				case "idle_prompt":
					evt.Type = EventNotificationIdle
				case "permission_prompt":
					evt.Type = EventNotificationPerm
				default:
					return HookEvent{}, fmt.Errorf("unknown notification type: %q", np.NotificationType)
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
		if err := json.Unmarshal(p.HookInput, &np); err == nil && np.NotificationType != "" {
			switch np.NotificationType {
			case "idle_prompt":
				evt.Type = EventNotificationIdle
			case "permission_prompt":
				evt.Type = EventNotificationPerm
			default:
				return HookEvent{}, fmt.Errorf("unknown notification type: %q", np.NotificationType)
			}
			return evt, nil
		}
	}

	return HookEvent{}, fmt.Errorf("unrecognized hook payload")
}
