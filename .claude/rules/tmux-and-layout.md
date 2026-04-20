---
paths:
  - "internal/tmux/**"
---

# tmux Layout & Gotchas

## Window/pane structure

```
tmux session "mo"
├── window 0: "mo" (main TUI — full screen)
├── window 1: "moma-apps-rails"
│   ├── pane 0: Claude Code session
│   ├── pane 1: sidebar (mo sidebar)
│   └── pane 2+: terminal splits (optional, via `t`)
├── window 2: "moma-apps-rails [2]"  (concurrent sibling — see Multi-session)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 3: "moma-go"
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 4: "unky-mo@feature-auth" (worktree session)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 5: "unky-mo@feature-auth [debug-oauth]" (sibling renamed via /rename)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
└── ...
```

## Gotchas

- **`split-window` without `-c` does not inherit the target pane's cwd.** Modern tmux (3.2+) uses the session's launch directory instead. Always pass `-c <path>` explicitly when creating panes that need a specific cwd. See `internal/tmux/client.go:SplitWindow`.
- **Format strings in `-c` don't expand against the `-t` target.** `tmux split-window -t foo:bar -c "#{pane_current_path}"` expands the format against whatever pane tmux considers "current" server-wide (typically the most recently active pane of the attached client), **not** against the target pane. Always pass a literal path string to `-c`.
- **`tmux display-message -p ...` without `-t` uses the attached client's focused pane**, not the calling pane. From a subprocess (like `mo sidebar`), use `TMUX_PANE` for pane-specific queries: `tmux display-message -t "$TMUX_PANE" -p '#{window_name}'`. See `internal/tui/sidebar/model.go:NewModel`.
