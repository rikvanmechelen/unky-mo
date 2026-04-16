#!/bin/bash
# Unky Mo notification hook for Claude Code.
# Called by Claude Code's Notification hook (idle_prompt, permission_prompt).
# Receives JSON on stdin with: message, notificationType
# Forwards enriched JSON to Unky Mo's Unix socket.

SOCKET="/tmp/unky-mo.sock"
if [ ! -S "$SOCKET" ]; then
    exit 0  # Orchestrator not running, silently skip
fi

# Read hook input from stdin
INPUT=$(cat)

# Get session context
SESSION_ID="${CLAUDE_SESSION_ID:-unknown}"
PROJECT_PATH="$(pwd)"
TMUX_PANE="${TMUX_PANE:-}"

# Build and send JSON message
printf '{"hook_input":%s,"session_id":"%s","project_path":"%s","tmux_pane":"%s","timestamp":"%s"}\n' \
    "$INPUT" "$SESSION_ID" "$PROJECT_PATH" "$TMUX_PANE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    | nc -U "$SOCKET" 2>/dev/null

exit 0
