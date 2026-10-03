package claude

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// statusHookScript is the V2 unified hook script, embedded so the installed
// hooks don't depend on where the unky-mo checkout lives.
//
//go:embed status-hook.sh
var statusHookScript []byte

// EnsureStatusHookScript writes the embedded status hook script to
// dir/status-hook.sh (0755) unless an identical copy is already there.
// Returns the script path and whether it was (re)written.
func EnsureStatusHookScript(dir string) (path string, changed bool, err error) {
	path = filepath.Join(dir, "status-hook.sh")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, statusHookScript) {
		return path, false, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", false, err
	}
	// Write-then-rename so a hook firing mid-update never runs a torn script.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, statusHookScript, 0755); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", false, err
	}
	return path, true, nil
}

const hookMarker = "unky-mo"

// HookConfig represents a single hook entry in Claude's settings.
type HookConfig struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// HookEntry represents a hook group with optional matcher.
type HookEntry struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []HookConfig `json:"hooks"`
}

// ClaudeSettingsPath returns the path to Claude's global settings.
func ClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// InstallHooks adds Unky Mo notification hooks to Claude's global settings.
// It preserves existing hooks and only adds/replaces the Unky Mo entries.
func InstallHooks(notifyScript, stopScript string) error {
	settings, err := readSettings()
	if err != nil {
		return fmt.Errorf("reading settings: %w", err)
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	// Add/replace Notification hook
	notifEntries := filterOutUnkyMo(hooks, "Notification")
	notifEntries = append(notifEntries, map[string]interface{}{
		"matcher": "idle_prompt|permission_prompt",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": notifyScript + " # " + hookMarker,
				"timeout": 5,
			},
		},
	})
	hooks["Notification"] = notifEntries

	// Add/replace Stop hook
	stopEntries := filterOutUnkyMo(hooks, "Stop")
	stopEntries = append(stopEntries, map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": stopScript + " # " + hookMarker,
				"timeout": 5,
			},
		},
	})
	hooks["Stop"] = stopEntries

	settings["hooks"] = hooks
	return writeSettings(settings)
}

// v2HookTypes lists all hook event types that InstallHooksV2 registers.
var v2HookTypes = []struct {
	name    string
	matcher string // empty = no matcher (fires for all events of this type)
}{
	{"UserPromptSubmit", ""},
	{"Stop", ""},
	{"PreToolUse", ""},
	{"Notification", "idle_prompt|permission_prompt"},
	{"PermissionRequest", ""},
	{"SessionStart", ""},
	{"SessionEnd", ""},
}

// InstallHooksV2 installs the expanded hook set using a single unified script.
// It replaces any existing V1 hooks (Notification + Stop) with the full set.
func InstallHooksV2(statusScript string) error {
	settings, err := readSettings()
	if err != nil {
		return fmt.Errorf("reading settings: %w", err)
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	for _, ht := range v2HookTypes {
		entries := filterOutUnkyMo(hooks, ht.name)
		hookEntry := map[string]interface{}{
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": v2HookCommand(ht.name, statusScript),
					"timeout": 5,
				},
			},
		}
		if ht.matcher != "" {
			hookEntry["matcher"] = ht.matcher
		}
		entries = append(entries, hookEntry)
		hooks[ht.name] = entries
	}

	settings["hooks"] = hooks
	return writeSettings(settings)
}

func v2HookCommand(hookType, statusScript string) string {
	return fmt.Sprintf("HOOK_EVENT_NAME=%s %s # %s", hookType, statusScript, hookMarker)
}

// EnsureHooksV2 installs the V2 hook set unless Claude's settings already
// hold exactly it for statusScript: one unky-mo entry per V2 hook type, with
// the expected command and matcher. Anything else — V1 leftovers, a missing
// type, a stale script path, duplicates — triggers a reinstall. Returns
// whether settings.json was rewritten.
func EnsureHooksV2(statusScript string) (bool, error) {
	settings, err := readSettings()
	if err != nil {
		return false, fmt.Errorf("reading settings: %w", err)
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooksV2UpToDate(hooks, statusScript) {
		return false, nil
	}
	if err := InstallHooksV2(statusScript); err != nil {
		return false, err
	}
	return true, nil
}

func hooksV2UpToDate(hooks map[string]interface{}, statusScript string) bool {
	for _, ht := range v2HookTypes {
		entries, _ := hooks[ht.name].([]interface{})
		var ours []map[string]interface{}
		for _, e := range entries {
			if entryHasMarker(e) {
				obj, _ := e.(map[string]interface{})
				ours = append(ours, obj)
			}
		}
		if len(ours) != 1 {
			return false
		}
		matcher, _ := ours[0]["matcher"].(string)
		if matcher != ht.matcher {
			return false
		}
		list, _ := ours[0]["hooks"].([]interface{})
		if len(list) != 1 {
			return false
		}
		h, _ := list[0].(map[string]interface{})
		if cmd, _ := h["command"].(string); cmd != v2HookCommand(ht.name, statusScript) {
			return false
		}
	}
	return true
}

// HooksV2Installed checks if the expanded V2 hook set is present.
func HooksV2Installed() bool {
	settings, err := readSettings()
	if err != nil {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return false
	}
	for _, ht := range v2HookTypes {
		if !hasUnkyMoHook(hooks, ht.name) {
			return false
		}
	}
	return true
}

// UninstallHooks removes Unky Mo hooks from Claude's global settings.
func UninstallHooks() error {
	settings, err := readSettings()
	if err != nil {
		return fmt.Errorf("reading settings: %w", err)
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return nil
	}

	// Remove both V1 (Notification, Stop) and V2 hook types.
	hookTypes := []string{"Notification", "Stop"}
	for _, ht := range v2HookTypes {
		hookTypes = append(hookTypes, ht.name)
	}
	seen := make(map[string]bool)
	for _, ht := range hookTypes {
		if seen[ht] {
			continue
		}
		seen[ht] = true
		hooks[ht] = filterOutUnkyMo(hooks, ht)
	}

	// Clean up empty arrays
	for key, val := range hooks {
		if arr, ok := val.([]interface{}); ok && len(arr) == 0 {
			delete(hooks, key)
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	return writeSettings(settings)
}

// HooksInstalled checks if Unky Mo hooks are present in Claude settings.
func HooksInstalled() bool {
	settings, err := readSettings()
	if err != nil {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return false
	}
	return hasUnkyMoHook(hooks, "Notification") && hasUnkyMoHook(hooks, "Stop")
}

func readSettings() (map[string]interface{}, error) {
	path := ClaudeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]interface{}), nil
		}
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return settings, nil
}

func writeSettings(settings map[string]interface{}) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := ClaudeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// filterOutUnkyMo removes entries with the unky-mo marker from a hook type.
func filterOutUnkyMo(hooks map[string]interface{}, hookType string) []interface{} {
	raw, ok := hooks[hookType]
	if !ok {
		return nil
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var filtered []interface{}
	for _, entry := range entries {
		if !entryHasMarker(entry) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func hasUnkyMoHook(hooks map[string]interface{}, hookType string) bool {
	raw, ok := hooks[hookType]
	if !ok {
		return false
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return false
	}
	for _, entry := range entries {
		if entryHasMarker(entry) {
			return true
		}
	}
	return false
}

func entryHasMarker(entry interface{}) bool {
	obj, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	hooksList, ok := obj["hooks"].([]interface{})
	if !ok {
		return false
	}
	for _, h := range hooksList {
		hobj, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		cmd, _ := hobj["command"].(string)
		if len(cmd) > 0 && contains(cmd, hookMarker) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
