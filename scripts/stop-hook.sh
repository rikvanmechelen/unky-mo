#!/bin/bash
# Unky Mo stop hook for Claude Code.
# Called by Claude Code's Stop hook when a turn completes.
# Sends a session_stop notification to clear idle/permission status.

SOCKET="/tmp/unky-mo.sock"
if [ ! -S "$SOCKET" ]; then
    exit 0
fi

SESSION_ID="${CLAUDE_SESSION_ID:-unknown}"
PROJECT_PATH="$(pwd)"
TMUX_PANE="${TMUX_PANE:-}"

printf '{"type":"session_stop","session_id":"%s","project_path":"%s","tmux_pane":"%s","timestamp":"%s"}\n' \
    "$SESSION_ID" "$PROJECT_PATH" "$TMUX_PANE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    | nc -U "$SOCKET" 2>/dev/null

exit 0
