package tmux

import (
	"fmt"
	"strings"
)

// parseWindowList parses the output of
//
//	tmux list-windows -F '#{window_id}:#{window_index}:#{window_name}:#{@mo_instance_id}:#{@mo_agent}:#{pane_current_path}'
//
// into a slice of Window. pane_current_path is last so SplitN preserves
// any ':' characters that appear in the path.
//
// Supported formats (newest first):
//   - 6-field: id:index:name:instanceID:agentKey:cwd  (current)
//   - 5-field: id:index:name:instanceID:cwd           (pre-agent windows)
//   - 4-field: id:index:name:cwd                      (pre-instance-ID)
func parseWindowList(out []byte) []Window {
	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 6)
		if len(parts) < 4 {
			continue
		}
		if len(parts) == 6 && isInstanceIDField(parts[3]) && isAgentKeyField(parts[4]) {
			// 6-field format: id:index:name:instanceID:agentKey:cwd
			windows = append(windows, Window{
				ID:         parts[0],
				Index:      parts[1],
				Name:       parts[2],
				InstanceID: parts[3],
				AgentKey:   parts[4],
				CWD:        parts[5],
			})
		} else if len(parts) >= 5 && isInstanceIDField(parts[3]) && !isAgentKeyField(parts[4]) {
			// 5-field format (pre-agent): id:index:name:instanceID:cwd
			// Field 4 is not a valid agent key, so it's the start of the CWD.
			cwd := strings.Join(parts[4:], ":")
			windows = append(windows, Window{
				ID:         parts[0],
				Index:      parts[1],
				Name:       parts[2],
				InstanceID: parts[3],
				CWD:        cwd,
			})
		} else {
			// Old 4-field format, or fields where parts[3] is not a valid
			// instance ID (e.g. cwd contains colons). Re-join 3+ as CWD.
			cwd := strings.Join(parts[3:], ":")
			windows = append(windows, Window{
				ID:    parts[0],
				Index: parts[1],
				Name:  parts[2],
				CWD:   cwd,
			})
		}
	}
	return windows
}

// isInstanceIDField returns true if s is a valid instance ID field in the
// list-windows output: either empty (tmux option not set) or exactly 12
// lowercase hex characters.
func isInstanceIDField(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// isAgentKeyField returns true if s is a valid agent key field: either
// empty (no agent set) or a single lowercase letter.
func isAgentKeyField(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 1 {
		return false
	}
	return s[0] >= 'a' && s[0] <= 'z'
}

// parsePIDSet parses the output of `tmux list-panes … -F '#{pane_pid}'` into
// a set of PIDs. Malformed lines are silently dropped.
func parsePIDSet(out []byte) map[int]bool {
	pids := make(map[int]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil {
			continue
		}
		pids[pid] = true
	}
	return pids
}
