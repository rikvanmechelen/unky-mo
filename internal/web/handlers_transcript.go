package web

import (
	"fmt"
	"net/http"
)

// handleTranscript streams a session's conversation as Server-Sent Events:
// the full existing backlog first, then a "transcript" event per new
// JSONL message as the session continues, until the client disconnects.
// Every event payload is the original JSONL line verbatim (see
// transcript.go) — the browser switches on its own "type" field.
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	jsonlPath, sessionID, _, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}

	s.streamTranscript(w, r, sessionID, jsonlPath)
}

// streamTranscript subscribes to the JSONL file at path (under hub key
// key) and streams it as "transcript" events until the client goes away.
func (s *Server) streamTranscript(w http.ResponseWriter, r *http.Request, key, path string) {
	backlog, ch, unsubscribe, err := s.transcripts.subscribe(key, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	rc := http.NewResponseController(w)

	for _, msg := range backlog {
		fmt.Fprintf(w, "event: transcript\ndata: %s\n\n", msg)
	}
	_ = rc.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: transcript\ndata: %s\n\n", msg)
			_ = rc.Flush()
		}
	}
}
