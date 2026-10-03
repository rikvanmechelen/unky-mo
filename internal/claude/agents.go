package claude

import (
	"context"
	"encoding/json"
	"fmt"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// Agent represents one entry from `claude agents --json`, the CLI's own
// session-discovery command (added after this package's PID-file-based
// Session type was written). Unlike Session, which is parsed from
// ~/.claude/sessions/{PID}.json, Agent carries a live Status ("busy" or
// "idle" observed so far) reported directly by Claude Code.
type Agent struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"` // "interactive" or "background"
	StartedAt int64  `json:"startedAt"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"` // "busy", "idle", or "waiting"
	// WaitingFor qualifies Status "waiting" — e.g. "input needed" while an
	// AskUserQuestion menu is open, "dialog open" for other modal dialogs.
	WaitingFor string `json:"waitingFor,omitempty"`
}

// LiveAgents lists sessions via `claude agents --json` instead of reading
// ~/.claude/sessions/{PID}.json files directly. With all=true, finished
// background sessions are included too. Requires `claude` on PATH.
//
// Not yet wired into LiveSessions/ReadSessions: the integration test harness's
// fake-claude.sh only emulates the PID-file side of session discovery, and
// the hot 5s TUI/sidebar poll currently reads small local files rather than
// spawning a subprocess. Candidate call sites (CLI import/cleanup commands,
// a reconciliation signal into status.Manager) run once per invocation, not
// on the poll loop.
func LiveAgents(ctx context.Context, cmd moexec.Commander, all bool) ([]Agent, error) {
	args := []string{"agents", "--json"}
	if all {
		args = append(args, "--all")
	}
	stdout, stderr, err := cmd.Output(ctx, "", "claude", args...)
	if err != nil {
		return nil, fmt.Errorf("claude %v: %w (%s)", args, err, stderr)
	}
	var agents []Agent
	if err := json.Unmarshal(stdout, &agents); err != nil {
		return nil, fmt.Errorf("parsing claude agents --json output: %w", err)
	}
	return agents, nil
}
