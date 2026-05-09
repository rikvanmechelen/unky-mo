package claude

import "testing"

func TestExtractEvalCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "quoted form with env vars",
			cmd:  "/bin/zsh -c source /home/u/.claude/shell-snapshots/snapshot.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB 2>/dev/null || true && eval 'PORT=3000 bundle exec foreman start' < /dev/null && pwd -P >| /tmp/claude-abcd-cwd",
			want: "PORT=3000 bundle exec foreman start",
		},
		{
			name: "unquoted single-token command",
			cmd:  "/usr/bin/zsh -c source /home/rvanmech/.claude/shell-snapshots/snapshot-zsh-1778290973036-d2p3oq.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB 2>/dev/null || true && eval bin/dev < /dev/null && pwd -P >| /tmp/claude-2c35-cwd",
			want: "bin/dev",
		},
		{
			name: "unquoted multi-token command",
			cmd:  "/usr/bin/zsh -c source /snap.sh && eval npm run watch < /dev/null && pwd -P >| /tmp/claude-XXXX-cwd",
			want: "npm run watch",
		},
		{
			name: "no eval present",
			cmd:  "/usr/bin/zsh -c some_other_invocation",
			want: "",
		},
		{
			name: "trivial cd skipped",
			cmd:  "/usr/bin/zsh -c source /snap.sh && eval 'cd /tmp' < /dev/null && pwd -P >| /tmp/claude-XXXX-cwd",
			want: "",
		},
		{
			name: "trivial unquoted ls skipped",
			cmd:  "/usr/bin/zsh -c source /snap.sh && eval ls < /dev/null && pwd -P >| /tmp/claude-XXXX-cwd",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractEvalCommand(tc.cmd)
			if got != tc.want {
				t.Errorf("extractEvalCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractEvalCommandTruncates(t *testing.T) {
	long := "very_long_command_that_exceeds_eighty_characters_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	cmd := "/usr/bin/zsh -c snap && eval " + long + " < /dev/null && pwd -P >| /tmp/c"
	got := extractEvalCommand(cmd)
	if len(got) != 80 {
		t.Errorf("expected truncated length 80, got %d (%q)", len(got), got)
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("expected truncated suffix '...', got %q", got)
	}
}
