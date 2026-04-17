package claude

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ActiveShell represents a running Bash tool subprocess of a Claude session.
type ActiveShell struct {
	PID        int
	Command    string // the user-visible command (extracted from Claude's eval wrapper)
	OutputFile string // path to the output file (if found via lsof)
	StartTime  string // approximate start time from ps
}

// ActiveShells returns the currently running shell subprocesses for the given
// Claude PID. These are the Bash tool invocations that Claude is running.
func ActiveShells(claudePID int) []ActiveShell {
	if claudePID <= 0 {
		return nil
	}

	// Get all processes with their PID, PPID, start time, and full command
	out, err := exec.Command("ps", "-eo", "pid,ppid,lstart,command").Output()
	if err != nil {
		return nil
	}

	var shells []ActiveShell
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid != claudePID {
			continue
		}

		// lstart has 5 fields: "Mon Apr 17 16:59:00 2026"
		// Command starts at field index 7
		cmd := strings.Join(fields[7:], " ")
		startTime := strings.Join(fields[2:7], " ")

		if strings.Contains(cmd, ".claude/shell-snapshots") {
			if extracted := extractEvalCommand(cmd); extracted != "" {
				pid, _ := strconv.Atoi(fields[0])
				shell := ActiveShell{
					PID:       pid,
					Command:   extracted,
					StartTime: startTime,
				}
				// Find the output file via lsof
				shell.OutputFile = findOutputFile(pid)
				shells = append(shells, shell)
			}
		}
	}

	return shells
}

// findOutputFile looks up the .output file that a shell process is writing to.
func findOutputFile(pid int) string {
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && strings.Contains(line, ".output") {
			return line[1:] // strip "n" prefix
		}
	}
	return ""
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
