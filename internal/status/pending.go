package status

import (
	"encoding/json"
	"io"
	"os"
	"strings"
)

// pendingTailBytes bounds how much of a transcript ReadPendingTool reads:
// the open call is the last assistant line, followed only by the metadata
// Claude Code appends while it waits.
const pendingTailBytes = 1 << 20

// ReadPendingTool recovers the tool call a session is blocked on from its
// JSONL transcript: the newest tool_use with no tool_result after it, in
// the current turn — of an interactive tool (see isInteractiveTool) for a
// question, of any tool for a permission prompt. Claude Code (2.1.289)
// usually writes the assistant line carrying the tool_use before it
// opens the dialog, so the call is on disk while it waits — this is how a
// question or permission prompt survives a TUI restart, or a dropped
// hook, which would otherwise leave only `claude agents`' content-less
// status. The input is the tool_use's own, the same shape the hook
// forwards. ok is false when there's no such call: some calls
// (ExitPlanMode, a call retried after a rejection) are only written once
// answered, and a future Claude Code that stops writing calls early just
// falls back to the content-less status. With parallel calls open, the
// newest wins; it may not be the one the dialog asks about, which the web
// checks against the screen.
func ReadPendingTool(path string, interactiveOnly bool) (tool string, input json.RawMessage, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", nil, false
	}
	size := info.Size()
	n := min(size, pendingTailBytes)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return "", nil, false
	}
	return scanPendingTool(buf, interactiveOnly)
}

// scanPendingTool walks buf's lines backwards. tool_results seen on the
// way answer earlier tool_uses; a real prompt or an end_turn ends the
// search, since a call from an earlier turn can't still be open. A
// partial first line (the tail cut) fails to parse and is skipped.
func scanPendingTool(buf []byte, interactiveOnly bool) (string, json.RawMessage, bool) {
	answered := map[string]bool{}
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
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			ToolUseID string          `json:"tool_use_id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}
		// A string content (a typed prompt) leaves blocks empty.
		_ = json.Unmarshal(msg.Message.Content, &blocks)

		switch msg.Type {
		case "user":
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					answered[b.ToolUseID] = true
					results++
				}
			}
			if results == 0 {
				return "", nil, false // a prompt: no tool call of this turn is left
			}
		case "assistant":
			if msg.Message.StopReason == "end_turn" {
				return "", nil, false
			}
			// One API message can be split over several lines, a block
			// each; the newest open call wins.
			for j := len(blocks) - 1; j >= 0; j-- {
				b := blocks[j]
				if b.Type == "tool_use" && (!interactiveOnly || isInteractiveTool(b.Name)) && !answered[b.ID] && len(b.Input) > 0 {
					return b.Name, b.Input, true
				}
			}
		}
	}
	return "", nil, false
}

// RecoverPending fills in a question's or permission prompt's content
// when it has none — one set by `claude agents --json` rather than a hook
// (see ReadPendingTool). read gets the status (so a question only looks
// for interactive tools) and is only called in that case, so a session
// whose content is already known costs nothing. Returns the session's
// pending tool afterwards, like Pending.
func (m *Manager) RecoverPending(sessionID string, read func(SessionStatus) (string, json.RawMessage, bool)) (string, json.RawMessage, bool) {
	m.mu.RLock()
	s, found := m.sessions[sessionID]
	var st SessionStatus
	need := false
	if found {
		st = s.Status
		need = (st == StatusQuestion || st == StatusPermission) && s.PendingTool == ""
	}
	m.mu.RUnlock()
	if need {
		if tool, input, ok := read(st); ok {
			m.mu.Lock()
			// Re-checked: a hook may have landed while reading.
			if s, found := m.sessions[sessionID]; found && s.Status == st && s.PendingTool == "" {
				s.PendingTool, s.PendingInput = tool, input
			}
			m.mu.Unlock()
		}
	}
	return m.Pending(sessionID)
}
