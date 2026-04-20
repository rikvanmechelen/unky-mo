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
		if len(parts) < 5 {
			// Backwards compat: old 4-field format (no instance ID).
			if len(parts) == 4 {
				windows = append(windows, Window{
					ID:    parts[0],
					Index: parts[1],
					Name:  parts[2],
					CWD:   parts[3],
				})
			}
			continue
		}
		windows = append(windows, Window{
			ID:         parts[0],
			Index:      parts[1],
			Name:       parts[2],
			InstanceID: parts[3],
			CWD:        parts[4],
		})
	}
	return windows
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
