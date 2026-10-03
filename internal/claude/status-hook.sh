#!/bin/bash
# Unky Mo unified status hook for Claude Code.
# Handles all hook events: UserPromptSubmit, Stop, PreToolUse, PermissionRequest,
# Notification, SessionStart, SessionEnd.
# Receives JSON on stdin with hook_event_name and event-specific fields.
# Forwards enriched JSON to Unky Mo's Unix socket.

SOCKET="/tmp/unky-mo.sock"
if [ ! -S "$SOCKET" ]; then
    exit 0  # Orchestrator not running, silently skip
fi

# Read hook input from stdin
INPUT=$(cat)

# Get session context from environment + stdin
SESSION_ID="${CLAUDE_SESSION_ID:-unknown}"
PROJECT_PATH="$(pwd)"
TMUX_PANE="${TMUX_PANE:-}"

# Extract hook_event_name from the input JSON if present (Claude sets this).
# Fall back to a direct field if the script is invoked by a specific hook type.
HOOK_EVENT="${HOOK_EVENT_NAME:-}"

# Build and send enriched JSON message.
# The hook_event_name comes from Claude's stdin JSON; we add session context.
printf '{"hook_input":%s,"hook_event_name":"%s","session_id":"%s","project_path":"%s","tmux_pane":"%s","timestamp":"%s"}\n' \
    "$INPUT" "$HOOK_EVENT" "$SESSION_ID" "$PROJECT_PATH" "$TMUX_PANE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    | nc -U "$SOCKET" 2>/dev/null

exit 0
