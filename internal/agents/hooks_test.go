package agents

import "testing"

func TestNoopHookInstallerIsNoop(t *testing.T) {
	var h HookInstaller = &NoopHookInstaller{Agent: "Gemini"}
	if err := h.Install("/a", "/b"); err != nil {
		t.Errorf("Install: %v", err)
	}
	if err := h.Uninstall(); err != nil {
		t.Errorf("Uninstall: %v", err)
	}
	if h.IsInstalled() {
		t.Error("Noop should never report installed")
	}
	if h.SettingsPath() != "" {
		t.Error("Noop should return empty settings path")
	}
}
