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

	fileSize := info.Size()
	if fileSize == 0 {
		return StatusNone
	}

	// Progressive read: start with 128KB, double up to 1MB if we only
	// find skippable entries (file-history-snapshot floods can exceed 128KB).
	for readSize := int64(128 * 1024); readSize <= 1024*1024; readSize *= 2 {
		if readSize > fileSize {
			readSize = fileSize
		}

		buf := make([]byte, readSize)
		_, err = f.ReadAt(buf, fileSize-readSize)
		if err != nil {
			return StatusNone
		}

		if st := scanJSONLTail(buf); st != StatusNone {
			return st
		}

		// Already read the entire file — no point retrying.
		if readSize >= fileSize {
			break
		}
	}

	return StatusNone
}

// scanJSONLTail walks the lines in buf backwards and returns the status
// implied by the last meaningful JSONL entry, or StatusNone if the buffer
// contains only skippable metadata.
func scanJSONLTail(buf []byte) SessionStatus {
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
