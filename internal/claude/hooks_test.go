package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsPath resolves to ~/.claude/settings.json — redirected to a temp HOME.
func readSettingsFile(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(ClaudeSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInstallHooksOnEmptySettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if !HooksInstalled() {
		t.Fatal("HooksInstalled should report true after install")
	}
	settings := readSettingsFile(t)
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("hooks section missing")
	}
	notifRaw, _ := hooks["Notification"].([]interface{})
	if len(notifRaw) == 0 {
		t.Fatal("Notification hook missing")
	}
	// Marker present.
	dump, _ := json.Marshal(hooks)
	if !strings.Contains(string(dump), "unky-mo") {
		t.Error("marker should appear somewhere in hooks")
	}
	if !strings.Contains(string(dump), "/opt/notify.sh") {
		t.Error("notify script path should be written")
	}
}

func TestInstallHooksIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatal(err)
	}
	settings := readSettingsFile(t)
	hooks := settings["hooks"].(map[string]interface{})
	notif := hooks["Notification"].([]interface{})
	// Count mo-marked entries — must be exactly 1.
	markers := 0
	for _, e := range notif {
		if entryHasMarker(e) {
			markers++
		}
	}
	if markers != 1 {
		t.Errorf("want 1 mo-marked Notification entry, got %d (total %d)", markers, len(notif))
	}
}

func TestInstallHooksPreservesUnrelatedHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	preexisting := `{
  "hooks": {
    "Notification": [
      {
        "matcher": "idle_prompt",
        "hooks": [
          {"type":"command","command":"/usr/local/bin/my-own-hook.sh","timeout":3}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [{"type":"command","command":"/opt/log.sh","timeout":2}]
      }
    ]
  },
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(preexisting), 0644); err != nil {
		t.Fatal(err)
	}

	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	settings := readSettingsFile(t)
	if settings["theme"] != "dark" {
		t.Error("unrelated top-level keys should be preserved (theme missing/changed)")
	}
	hooks := settings["hooks"].(map[string]interface{})

	// UserPromptSubmit untouched.
	ups, _ := hooks["UserPromptSubmit"].([]interface{})
	if len(ups) != 1 {
		t.Errorf("UserPromptSubmit should have 1 entry, got %d", len(ups))
	}

	// Notification has both the user's hook AND mo's hook.
	notif, _ := hooks["Notification"].([]interface{})
	userHook, moHook := false, false
	for _, e := range notif {
		obj := e.(map[string]interface{})
		inner := obj["hooks"].([]interface{})
		for _, h := range inner {
			cmd := h.(map[string]interface{})["command"].(string)
			if strings.Contains(cmd, "my-own-hook.sh") {
				userHook = true
			}
			if strings.Contains(cmd, "unky-mo") {
				moHook = true
			}
		}
	}
	if !userHook {
		t.Error("user's Notification hook was clobbered")
	}
	if !moHook {
		t.Error("mo's Notification hook missing after install")
	}
}

func TestUninstallHooksRemovesOnlyMoEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Install first, then add a user hook alongside.
	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatal(err)
	}
	settings := readSettingsFile(t)
	hooks := settings["hooks"].(map[string]interface{})
	notif := hooks["Notification"].([]interface{})
	notif = append(notif, map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{"type": "command", "command": "/opt/my-hook.sh", "timeout": 3},
		},
	})
	hooks["Notification"] = notif
	data, _ := json.MarshalIndent(settings, "", "  ")
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Uninstall.
	if err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}
	if HooksInstalled() {
		t.Fatal("HooksInstalled should report false after uninstall")
	}
	after := readSettingsFile(t)
	ahooks, _ := after["hooks"].(map[string]interface{})
	if ahooks == nil {
		t.Fatal("user's unrelated hook got purged — hooks section should still exist")
	}
	anotif, _ := ahooks["Notification"].([]interface{})
	if len(anotif) != 1 {
		t.Fatalf("want 1 surviving hook, got %d", len(anotif))
	}
	// The surviving hook is the user's, not ours.
	inner := anotif[0].(map[string]interface{})["hooks"].([]interface{})
	cmd := inner[0].(map[string]interface{})["command"].(string)
	if !strings.Contains(cmd, "my-hook.sh") || strings.Contains(cmd, "unky-mo") {
		t.Errorf("surviving hook isn't the user's: %q", cmd)
	}
}

func TestUninstallHooksMissingFileIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := UninstallHooks(); err != nil {
		t.Errorf("uninstall with no settings file should not error: %v", err)
	}
}

func TestHooksInstalledFalseByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if HooksInstalled() {
		t.Error("no settings file → not installed")
	}
}

// --- V2 hooks tests ---

func TestInstallHooksV2_AddsExpandedHookSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InstallHooksV2("/opt/status-hook.sh"); err != nil {
		t.Fatal(err)
	}
	if !HooksV2Installed() {
		t.Fatal("HooksV2Installed should report true after install")
	}
	settings := readSettingsFile(t)
	hooks := settings["hooks"].(map[string]interface{})

	expected := []string{"UserPromptSubmit", "Stop", "PreToolUse", "Notification", "PermissionRequest", "SessionStart", "SessionEnd"}
	for _, name := range expected {
		entries, ok := hooks[name].([]interface{})
		if !ok || len(entries) == 0 {
			t.Errorf("hook type %q missing after V2 install", name)
		}
	}

	// Verify Notification has a matcher.
	notif := hooks["Notification"].([]interface{})
	entry := notif[len(notif)-1].(map[string]interface{})
	if entry["matcher"] != "idle_prompt|permission_prompt" {
		t.Errorf("Notification matcher: got %v", entry["matcher"])
	}
}

func TestInstallHooksV2_PreservesExistingHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0755)
	preexisting := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/my/hook.sh","timeout":2}]}]}}`
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(preexisting), 0644)

	if err := InstallHooksV2("/opt/status-hook.sh"); err != nil {
		t.Fatal(err)
	}

	settings := readSettingsFile(t)
	hooks := settings["hooks"].(map[string]interface{})
	ptEntries := hooks["PreToolUse"].([]interface{})
	// Should have 2 entries: the user's and ours.
	if len(ptEntries) != 2 {
		t.Errorf("PreToolUse: want 2 entries (user + mo), got %d", len(ptEntries))
	}
}

func TestInstallHooksV2_ReplacesV1Hooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Install V1 first.
	if err := InstallHooks("/opt/notify.sh", "/opt/stop.sh"); err != nil {
		t.Fatal(err)
	}
	if !HooksInstalled() {
		t.Fatal("V1 not installed")
	}

	// Install V2 — should replace V1 markers.
	if err := InstallHooksV2("/opt/status-hook.sh"); err != nil {
		t.Fatal(err)
	}

	settings := readSettingsFile(t)
	hooks := settings["hooks"].(map[string]interface{})
	dump, _ := json.Marshal(hooks)
	dumpStr := string(dump)

	// Old V1 scripts should be gone.
	if strings.Contains(dumpStr, "notify.sh") {
		t.Error("V1 notify script should have been replaced")
	}
	if strings.Contains(dumpStr, "stop.sh # unky-mo") {
		// The V2 Stop hook will contain "status-hook.sh # unky-mo", not "stop.sh"
		t.Error("V1 stop script should have been replaced")
	}

	// V2 script should be present.
	if !strings.Contains(dumpStr, "status-hook.sh") {
		t.Error("V2 status-hook.sh should be present")
	}
}

func TestUninstallHooks_RemovesBothV1AndV2(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Install V2.
	if err := InstallHooksV2("/opt/status-hook.sh"); err != nil {
		t.Fatal(err)
	}
	if !HooksV2Installed() {
		t.Fatal("V2 not installed")
	}

	// Uninstall should remove all.
	if err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}
	if HooksInstalled() {
		t.Error("V1 check should be false after uninstall")
	}
	if HooksV2Installed() {
		t.Error("V2 check should be false after uninstall")
	}
}
