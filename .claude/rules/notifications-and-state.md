---
paths:
  - "internal/notify/**"
  - "internal/state/**"
---

# Notifications & State

## Notification flow

```
Claude hooks → notify-hook.sh → Unix socket → Main TUI → state file → Sidebars
```

- Hooks installed in `~/.claude/settings.json` with `# unky-mo` marker comment
- Notification types: `idle_prompt`, `permission_prompt` (from Claude), `session_stop` (from Stop hook)
- State file written atomically (temp + rename) on every 5s poll and on notification events

## State file schema

`internal/state/state.go` defines the shared JSON state written by the main TUI and polled by each sidebar every 1s. Path configurable via `Config.StateFilePath`, default `/tmp/unky-mo-state.json`.

- **`StateFile`**: `tmux_session` (string), `projects` ([]ProjectState), `updated_at` (time), `usage` (*UsageState, optional).
- **`ProjectState`** — one entry **per live Claude session**, plus one placeholder per zero-session known project. Concurrent siblings / renamed primaries produce separate entries, distinguished by `WindowID` (stable) / `WindowName` + `SessionID`. Key fields: `name`, `path`, `window_name`, `window_id` (stable tmux id, e.g. `@5` — survives renames), `status` (`"none"` | `"active"` | `"idle"` | `"permission"` | `"external"`), `parent`, `section` (`"projects"` | `"external"`), `session_id`, `index` (0 = primary, 2+ = sibling ordinal, -1 = custom-title window).
- **`UsageState`**: `five_hour_pct`, `seven_day_pct` (ints 0–100), their `*_resets_at` timestamps, `fetched_at`, `stale`, `auth_error`.

### Key rules

- **Sidebars prefer `window_id` over `window_name`** when matching their own row so the "current window" highlight survives `/rename` and ordinal shuffling; they fall back to name equality when either side has no id.
- **Writer is the main TUI — no sidebar ever writes.** Re-written on every 5s session-refresh tick, on every notification event, and after any user action that changes state.
- **Usage data is fetched by the main TUI only** (60s cadence); sidebars read it from the state file. Never add API calls to the sidebar.
