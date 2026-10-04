package notify

import "time"

// NotificationType identifies the kind of notification.
type NotificationType string

const (
	NotifyIdlePrompt       NotificationType = "idle_prompt"
	NotifyPermissionPrompt NotificationType = "permission_prompt"
	NotifySessionStop      NotificationType = "session_stop"
	// NotifyHookEvent is a V2 hook message (it carries hook_event_name),
	// passed through raw for status.ParseHookPayload to interpret.
	NotifyHookEvent NotificationType = "hook_event"
	// NotifyRestart asks the TUI to restart itself and every sidebar, as
	// ctrl+alt+r does. Sent by `mo restart` and the web dashboard.
	NotifyRestart NotificationType = "mo_restart"
)

// Notification represents a message received from a Claude Code hook.
type Notification struct {
	Type        NotificationType `json:"type"`
	SessionID   string           `json:"session_id"`
	ProjectPath string           `json:"project_path"`
	Message     string           `json:"message"`
	TmuxPane    string           `json:"tmux_pane,omitempty"`
	Timestamp   time.Time        `json:"timestamp"`
	// Raw is the original socket line, set for NotifyHookEvent.
	Raw []byte `json:"-"`
}
