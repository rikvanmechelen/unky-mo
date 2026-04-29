package agents

// HookInstaller is the interface for installing/uninstalling notification
// hooks for a coding agent. Claude implements this via ~/.claude/settings.json;
// agents without hook support use NoopHookInstaller.
type HookInstaller interface {
	Install(notifyScript, stopScript string) error
	Uninstall() error
	IsInstalled() bool
	SettingsPath() string // path to the agent's settings file (for display)
}

// NoopHookInstaller is a no-op implementation for agents that don't
// support notification hooks. Their sessions will show as "active"
// permanently (no idle/permission detection).
type NoopHookInstaller struct {
	Agent string // display name for messages
}

func (n *NoopHookInstaller) Install(string, string) error { return nil }
func (n *NoopHookInstaller) Uninstall() error             { return nil }
func (n *NoopHookInstaller) IsInstalled() bool            { return false }
func (n *NoopHookInstaller) SettingsPath() string         { return "" }
