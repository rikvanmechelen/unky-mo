#!/usr/bin/env bash
# fake-claude.sh — stand-in for the real `claude` binary used by integration
# tests. Writes the session marker + JSONL then blocks on stdin until killed.
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
