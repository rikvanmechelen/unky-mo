package notify

import "time"

// NotificationType identifies the kind of notification.
type NotificationType string

const (
	NotifyIdlePrompt       NotificationType = "idle_prompt"
	NotifyPermissionPrompt NotificationType = "permission_prompt"
	NotifySessionStop      NotificationType = "session_stop"
)

// Notification represents a message received from a Claude Code hook.
type Notification struct {
	Type        NotificationType `json:"type"`
	SessionID   string           `json:"session_id"`
	ProjectPath string           `json:"project_path"`
	Message     string           `json:"message"`
	TmuxPane    string           `json:"tmux_pane,omitempty"`
	Timestamp   time.Time        `json:"timestamp"`
}
