package status

import (
	"encoding/json"
	"os"
	"strings"
)

// ReadJSONLStatus reads the tail of a session JSONL file and returns the
// inferred session status. Unlike the old IsSessionIdle, this function uses
// no time-based heuristics — it returns a pure snapshot of the last
// meaningful JSONL entry.
//
// Returns StatusNone if the file is missing, empty, or contains only metadata.
func ReadJSONLStatus(path string) SessionStatus {
	info, err := os.Stat(path)
	if err != nil {
		return StatusNone
	}

	f, err := os.Open(path)
	if err != nil {
		return StatusNone
	}
	defer f.Close()

	readSize := int64(128 * 1024)
	if info.Size() < readSize {
		readSize = info.Size()
	}
	if readSize == 0 {
		return StatusNone
	}

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, info.Size()-readSize)
	if err != nil {
		return StatusNone
	}

	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var msg struct {
			Type    string `json:"type"`
			Message struct {
				StopReason string          `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		switch msg.Type {
		case "file-history-snapshot", "attachment", "permission-mode",
			"custom-title", "deferred_tools_delta", "":
			continue
		case "assistant":
			if msg.Message.StopReason == "end_turn" {
				return StatusIdle
			}
			// tool_use or any other stop_reason = still working.
			return StatusActive
		case "user":
			if isToolResult(msg.Message.Content) {
				return StatusActive
			}
			if isSlashCommand(msg.Message.Content) {
				// Slash commands are synthetic entries that don't trigger
				// a Claude response — the session is idle.
				return StatusIdle
			}
			// Regular user prompt — Claude should be generating.
			return StatusActive
		default:
			continue
		}
	}

	return StatusNone
}

// isToolResult reports whether a user message's content contains a tool_result.
func isToolResult(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &items) == nil {
		for _, item := range items {
			if item.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// isSlashCommand reports whether a user message looks like a slash command.
func isSlashCommand(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.HasPrefix(s, "<local-command") || strings.HasPrefix(s, "<command")
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &items) == nil {
		for _, item := range items {
			if item.Type == "text" {
				return strings.HasPrefix(item.Text, "<local-command") || strings.HasPrefix(item.Text, "<command")
			}
		}
	}
	return false
}
