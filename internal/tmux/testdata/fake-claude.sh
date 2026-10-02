#!/usr/bin/env bash
# fake-claude.sh — stand-in for the real `claude` binary used by integration
# tests. Writes the session marker + JSONL then blocks on stdin until killed.
#
# Also answers `claude agents --json [--all]` (the real CLI's own
# session-discovery command) by scanning the same sessions dir this script
# writes to when acting as a live session — so it can sit on $PATH as
# literally "claude" and serve both roles.
#
# Required env:
#   FAKE_CLAUDE_HOME — overrides HOME for session file placement
# Optional env:
#   FAKE_CLAUDE_CWD  — overrides the reported cwd (default: $PWD)
#
# Intentionally uses only POSIX + coreutils so it runs on CI without
# additional setup.

set -u

home="${FAKE_CLAUDE_HOME:-$HOME}"

# Discovery mode: `claude agents --json [--all]`. Dead sessions are
# distinguished only in behavior that matters to callers today — this fake
# doesn't model "finished background" sessions, so --all is a no-op here.
if [ "${1:-}" = "agents" ]; then
    sessions_dir="${home}/.claude/sessions"
    entries=""
    if [ -d "$sessions_dir" ]; then
        for f in "$sessions_dir"/*.json; do
            [ -e "$f" ] || continue
            candidate_pid=$(basename "$f" .json)
            kill -0 "$candidate_pid" 2>/dev/null || continue
            cwd=$(sed -n 's/.*"cwd":"\([^"]*\)".*/\1/p' "$f")
            session_id=$(sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p' "$f")
            started=$(sed -n 's/.*"startedAt":\([0-9]*\).*/\1/p' "$f")
            entry=$(printf '{"pid":%s,"cwd":"%s","kind":"interactive","startedAt":%s,"sessionId":"%s","name":"","status":"idle"}' \
                "$candidate_pid" "$cwd" "$started" "$session_id")
            if [ -z "$entries" ]; then
                entries="$entry"
            else
                entries="$entries,$entry"
            fi
        done
    fi
    printf '[%s]\n' "$entries"
    exit 0
fi

pid=$$
session_id="fake-${pid}-$(date +%s)"
cwd="${FAKE_CLAUDE_CWD:-$PWD}"

# Session marker — claude writes this to advertise the live process.
sessions_dir="${home}/.claude/sessions"
mkdir -p "$sessions_dir"
cat > "${sessions_dir}/${pid}.json" <<EOF
{"pid":${pid},"sessionId":"${session_id}","cwd":"${cwd}","startedAt":$(date +%s),"kind":"default","entrypoint":"claude","name":""}
EOF

# Encode cwd the way mo expects: replace / _ . with -
encoded=$(echo "$cwd" | sed -e 's|^/||' -e 's|/|-|g' -e 's|_|-|g' -e 's|\.|-|g')
projects_dir="${home}/.claude/projects/-${encoded}"
mkdir -p "$projects_dir"
jsonl="${projects_dir}/${session_id}.jsonl"

# Seed with one assistant turn so idle detection can fire.
cat > "$jsonl" <<EOF
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","content":"hi","stop_reason":"end_turn"}}
EOF

cleanup() {
  rm -f "${sessions_dir}/${pid}.json"
  exit 0
}
trap cleanup TERM INT HUP

# Sit on stdin until we're killed.
while read -r _; do :; done
cleanup
