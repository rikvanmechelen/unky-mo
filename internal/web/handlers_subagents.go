package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// subagentView is one of a session's Agent-tool subagents, for the chat
// view's background-agents strip and its Agent tool cards.
type subagentView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	ToolUseID   string `json:"tool_use_id"`
	Background  bool   `json:"background"`
	// State is "running", "waiting" (on background work of its own),
	// "done", or a stop status such as "killed"; Running covers the first
	// two.
	State   string `json:"state"`
	Running bool   `json:"running"`
	// Started and LastActivity are RFC 3339; empty before the first line.
	Started      string `json:"started"`
	LastActivity string `json:"last_activity"`
	ToolUses     int    `json:"tool_uses"`
	LastTool     string `json:"last_tool"`
	LastDetail   string `json:"last_detail"`
}

// sessionSubagents lists the subagents of the live session in windowID
// through a short cache, so several open tabs share one read.
func (s *Server) sessionSubagents(windowID string) ([]claude.Subagent, bool, error) {
	row, ok := s.windowRow(windowID)
	if !ok || row.SessionID == "" || row.Path == "" {
		return nil, false, nil
	}
	v, err := s.agentsCache.get(row.Path+"\x00"+row.SessionID, func() (any, error) {
		return s.deps.Subagents.List(row.Path, row.SessionID)
	})
	if err != nil {
		return nil, true, err
	}
	agents, _ := v.([]claude.Subagent)
	return agents, true, nil
}

func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	agents, ok, err := s.sessionSubagents(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	row, _ := s.windowRow(windowID)
	out := make([]subagentView, 0, len(agents))
	for _, a := range agents {
		out = append(out, subagentView{
			ID:           a.ID,
			Type:         a.AgentType,
			Description:  a.Description,
			ToolUseID:    a.ToolUseID,
			Background:   a.Background,
			State:        a.State,
			Running:      a.Running,
			Started:      formatTime(a.StartedAt),
			LastActivity: formatTime(a.LastActivity),
			ToolUses:     a.ToolUses,
			LastTool:     a.LastTool,
			// File paths read better relative to the checkout.
			LastDetail: strings.TrimPrefix(a.LastToolDetail, strings.TrimSuffix(row.Path, "/")+"/"),
		})
	}
	writeJSON(w, out)
}

// handleSubagentTranscript streams one subagent's transcript like
// handleTranscript does the session's. The agent must be one the session's
// listing returned; the file path comes from that listing, never from the
// request.
func (s *Server) handleSubagentTranscript(w http.ResponseWriter, r *http.Request) {
	windowID, agentID := r.PathValue("windowID"), r.PathValue("agentID")
	agents, ok, err := s.sessionSubagents(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	row, _ := s.windowRow(windowID)
	for _, a := range agents {
		if a.ID != agentID {
			continue
		}
		// ?once=1 answers with the lines read so far as one JSON array
		// instead of a stream: the Overview tab's intent trace reads a
		// finished agent's edits once, without holding a connection open.
		if r.URL.Query().Get("once") == "1" {
			lines, err := newTranscriptCursor(a.TranscriptPath).readNew()
			if err != nil {
				writeError(w, http.StatusBadGateway, err)
				return
			}
			if lines == nil {
				lines = []json.RawMessage{}
			}
			writeJSON(w, lines)
			return
		}
		s.streamTranscript(w, r, row.SessionID+"/agent-"+a.ID, a.TranscriptPath)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("no subagent %s in window %s", agentID, windowID))
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
