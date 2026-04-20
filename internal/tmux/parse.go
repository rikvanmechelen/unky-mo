package tmux

import (
	"fmt"
	"strings"
)

// parseWindowList parses the output of
//
//	tmux list-windows -F '#{window_id}:#{window_index}:#{window_name}:#{@mo_instance_id}:#{pane_current_path}'
//
// into a slice of Window. pane_current_path is last so SplitN(5) preserves
// any ':' characters that appear in the path. The @mo_instance_id field is
// empty for windows that don't have the option set (pre-refactor windows).
func parseWindowList(out []byte) []Window {
	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 5)
		if len(parts) < 4 {
			continue
		}
		if len(parts) == 5 && isInstanceIDField(parts[3]) {
			// New 5-field format: id:index:name:instanceID:cwd
			windows = append(windows, Window{
				ID:         parts[0],
				Index:      parts[1],
				Name:       parts[2],
				InstanceID: parts[3],
				CWD:        parts[4],
			})
		} else {
			// Old 4-field format, or 5+ parts where field 3 is not a
			// valid instance ID (e.g. cwd contains colons). Re-join
			// fields 3+ as the CWD.
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
