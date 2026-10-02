package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// handlePrompt injects a prompt into a live session's tmux pane, gated on
// the session currently being idle or blocked on an interactive question
// (status "question" — e.g. AskUserQuestion) — answering is exactly what's
// needed to unblock those. A genuine "permission" status stays rejected: we
// don't yet know the tool/input Claude Code's PermissionRequest hook carries,
// so there's nothing to safely render or confirm the user is answering. The
// actual response is not in this response body — it streams back over the
// session's already-open /api/transcript/{windowID} SSE connection.
func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")

	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("empty prompt"))
		return
	}
	if strings.ContainsAny(text, "\r\n") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("multi-line prompts are not supported yet"))
		return
	}

	_, _, status, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if status != "idle" && status != "question" {
		writeError(w, http.StatusConflict, fmt.Errorf("session is %s, not idle", status))
		return
	}

	target := s.tmuxSession + ":" + windowID + ".0"
	if err := s.deps.Prompts.SendLiteralText(target, text); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to deliver prompt: %w", err))
		return
	}
	writeJSON(w, map[string]any{})
}
