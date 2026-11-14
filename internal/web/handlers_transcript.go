package web

import (
	"compress/gzip"
	"fmt"
	"io"
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
	w.Header().Set("Vary", "Accept-Encoding")
	rc := http.NewResponseController(w)

	// The backlog is the whole session, megabytes of JSON that compress
	// 3-4x, and on a phone it's what a session switch waits for. Level 1:
	// the stream is made per request, and higher levels save little more.
	// The compressor is flushed with every write the browser should see.
	var out io.Writer = w
	flush := func() { _ = rc.Flush() }
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer zw.Close()
		out = zw
		flush = func() {
			_ = zw.Flush()
			_ = rc.Flush()
		}
	}

	for _, msg := range backlog {
		fmt.Fprintf(out, "event: transcript\ndata: %s\n\n", msg)
	}
	// Tells the browser the stored history is complete, so it can render it
	// in one go instead of line by line.
	fmt.Fprint(out, "event: caught-up\ndata: {}\n\n")
	flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(out, "event: transcript\ndata: %s\n\n", msg)
			flush()
		}
	}
}
