package claude

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ActiveShell represents a running Bash tool subprocess of a Claude session.
type ActiveShell struct {
	PID     int
	Command string // the user-visible command (extracted from Claude's eval wrapper)
}

// ActiveShells returns the currently running shell subprocesses for the given
// Claude PID. These are the Bash tool invocations that Claude is running.
func ActiveShells(claudePID int) []ActiveShell {
	if claudePID <= 0 {
		return nil
	}

	// Get all processes with their PID, PPID, and full command
	out, err := exec.Command("ps", "-eo", "pid,ppid,command").Output()
	if err != nil {
		return nil
	}

	var shells []ActiveShell
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid != claudePID {
			continue
		}

		// Reconstruct the full command string
		cmd := strings.Join(fields[2:], " ")

		// Claude's Bash tool runs commands as:
		//   /bin/zsh -c source .../.claude/shell-snapshots/... && eval 'COMMAND' ...
		// Extract the actual command from the eval wrapper
		if strings.Contains(cmd, ".claude/shell-snapshots") {
			if extracted := extractEvalCommand(cmd); extracted != "" {
				pid, _ := strconv.Atoi(fields[0])
				shells = append(shells, ActiveShell{
					PID:     pid,
					Command: extracted,
				})
			}
		}
	}

	return shells
}

// extractEvalCommand pulls the user command from Claude's shell wrapper.
// Input:  /bin/zsh -c source .../.claude/shell-snapshots/... && eval 'PORT=3000 bundle exec foreman start' ...
// Output: PORT=3000 bundle exec foreman start
func extractEvalCommand(cmd string) string {
	// Find "eval '" and extract until the closing quote
	idx := strings.Index(cmd, "eval '")
	if idx < 0 {
		return ""
	}
	rest := cmd[idx+6:] // skip "eval '"
	end := strings.Index(rest, "'")
	if end < 0 {
		return rest // no closing quote, return what we have
	}
	extracted := rest[:end]

	// Clean up: remove common wrappers
	extracted = strings.TrimSpace(extracted)

	// Truncate long commands
	if len(extracted) > 80 {
		extracted = extracted[:77] + "..."
	}

	// Skip trivial/one-shot commands (these would have exited already anyway)
	lower := strings.ToLower(extracted)
	if strings.HasPrefix(lower, "cd ") || extracted == "pwd" || extracted == "ls" {
		return ""
	}

	return extracted
}

// ActiveShellsForSession returns shells for a live session identified by project path.
func ActiveShellsForSession(projectPath string) []ActiveShell {
	session := SessionForPath(projectPath)
	if session == nil {
		return nil
	}
	return ActiveShells(session.PID)
}

// FormatShellCommand returns a short display version of a shell command.
func FormatShellCommand(cmd string, maxLen int) string {
	// Strip environment variable prefixes for display
	parts := strings.Fields(cmd)
	display := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.Contains(p, "=") && !strings.HasPrefix(p, "-") && len(display) == 0 {
			continue // skip env vars at the start
		}
		display = append(display, p)
	}
	result := strings.Join(display, " ")
	if result == "" {
		result = cmd
	}

	if len(result) > maxLen {
		return result[:maxLen-3] + "..."
	}
	return fmt.Sprintf("%s", result)
}
