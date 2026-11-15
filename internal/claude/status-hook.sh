#!/bin/bash
# Unky Mo unified status hook for Claude Code.
# Handles all hook events: UserPromptSubmit, Stop, PreToolUse, PermissionRequest,
# Notification, SessionStart, SessionEnd.
# Claude's JSON for the event arrives on stdin; `mo hooks send` wraps it with
# this session's context and forwards it to Unky Mo's Unix socket. The
# envelope is built in Go: built here with printf, a quote or backslash in
# the checkout path made it invalid JSON and every event was dropped.

# Both filled in at install time: the TUI's socket (in a directory only this
# user can enter) and the mo binary that installed this script.
SOCKET='__MO_SOCKET__'
MO_BIN='__MO_BIN__'
if [ ! -S "$SOCKET" ] || [ ! -x "$MO_BIN" ]; then
    exit 0  # Orchestrator not running (or mo moved): silently skip
fi

# Claude Code exports CLAUDE_CODE_SESSION_ID (the stdin JSON's session_id is
# authoritative and the Go side prefers it; this is just the envelope copy).
exec "$MO_BIN" hooks send --socket "$SOCKET" \
    --event "${HOOK_EVENT_NAME:-}" \
    --session "${CLAUDE_CODE_SESSION_ID:-${CLAUDE_SESSION_ID:-unknown}}" \
    --cwd "$PWD" \
    --pane "${TMUX_PANE:-}" 2>/dev/null
