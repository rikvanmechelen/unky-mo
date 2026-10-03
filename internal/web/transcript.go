package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/rvanmech/unky-mo/internal/status"
)

// transcriptCursor tracks how much of a session's JSONL file has already
// been read and forwarded. readNew() is stateless except for the offset:
// calling it repeatedly as the file grows returns only the lines appended
// since the previous call. A fresh cursor (offset 0) reads the entire file
// — this is deliberately the same code path used for "give me the full
// backlog" and "give me what's new," so there is exactly one read/parse/
// filter path to reason about.
type transcriptCursor struct {
	path    string
	offset  int64
	partial []byte // bytes of a trailing line with no terminating '\n' yet
}

func newTranscriptCursor(path string) *transcriptCursor {
	return &transcriptCursor{path: path}
}

// readNew reads any bytes appended to the file since the last call (or
// since construction, for a fresh cursor), splits them into complete lines,
// and returns the ones whose top-level "type" is forwarded to the browser
// (user/assistant only — see filter below). An incomplete trailing line (the
// file was read mid-write) is held and prefixed onto the next call's data
// rather than returned or dropped.
func (c *transcriptCursor) readNew() ([]json.RawMessage, error) {
	f, err := os.Open(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		// A just-launched session has no transcript until its first turn
		// is written — that's "nothing yet", not an error.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < c.offset {
		// Unexpected truncation/rotation — JSONL session files are
		// append-only in normal operation, so this should not happen, but
		// restart from scratch rather than erroring the whole subscriber.
		c.offset = 0
		c.partial = nil
	}

	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return nil, err
	}
	chunk, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	c.offset += int64(len(chunk))

	data := append(c.partial, chunk...)
	lines := bytes.Split(data, []byte("\n"))
	complete := lines[:len(lines)-1]
	if len(data) > 0 && data[len(data)-1] == '\n' {
		c.partial = nil
	} else {
		c.partial = append([]byte(nil), lines[len(lines)-1]...)
	}

	var out []json.RawMessage
	for _, line := range complete {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		if envelope.Type != "user" && envelope.Type != "assistant" {
			continue
		}
		out = append(out, json.RawMessage(append([]byte(nil), line...)))
	}
	return out, nil
}

// transcriptHub fans out live-tailed JSONL messages to any number of SSE
// subscribers per Claude session. Unlike the terminal mirror this replaced,
// "give me full history" is just "read the file from the start" — cheap and
// safe to do independently per subscriber — so only the live-tail half
// (advancing past what's already been read) needs to be shared.
type transcriptHub struct {
	watcher *status.Watcher

	mu       sync.Mutex
	sessions map[string]*transcriptSession // keyed by Claude session ID
}

type transcriptSession struct {
	cursor      *transcriptCursor
	subscribers map[chan json.RawMessage]struct{}
}

func newTranscriptHub() *transcriptHub {
	h := &transcriptHub{sessions: make(map[string]*transcriptSession)}
	w, err := status.NewWatcher(h.onFileChange)
	if err != nil {
		// fsnotify init failure is effectively unrecoverable for this
		// feature but must not crash `mo web` — log and leave h.watcher
		// nil; subscribe() still works for backlog-only requests, live
		// tailing just never fires (extremely unlikely in practice —
		// fsnotify.NewWatcher only fails on fd/resource exhaustion).
		log.Printf("transcript hub: fsnotify unavailable, live updates disabled: %v", err)
	}
	h.watcher = w
	return h
}

// subscribe returns the full current backlog for sessionID/path plus a
// channel that receives newly-appended messages, and an unsubscribe func
// that must be called exactly once when the caller (an SSE handler) is done.
func (h *transcriptHub) subscribe(sessionID, path string) (backlog []json.RawMessage, ch chan json.RawMessage, unsubscribe func(), err error) {
	backlog, err = newTranscriptCursor(path).readNew()
	if err != nil {
		return nil, nil, nil, err
	}

	h.mu.Lock()
	ts, ok := h.sessions[sessionID]
	if !ok {
		ts = &transcriptSession{
			cursor:      newTranscriptCursor(path),
			subscribers: make(map[chan json.RawMessage]struct{}),
		}
		// Prime the shared tail cursor to the current EOF so it only ever
		// reports messages that arrive after this moment — backlog delivery
		// above already covered everything up to here. (A write landing in
		// the gap between this and WatchSession below is the same class of
		// race status.Watcher already accepts for status reconciliation —
		// self-heals on the next write.)
		_, _ = ts.cursor.readNew()
		h.sessions[sessionID] = ts
		// The watcher watches the transcript's directory, which won't exist
		// yet for a session just launched in a checkout Claude has never run
		// in (e.g. a fresh worktree) — fsnotify can't watch a missing dir,
		// and live updates would silently never arrive. Create it, exactly
		// as Claude itself would on its first write.
		ensureTranscriptDir(path)
		if h.watcher != nil {
			h.watcher.WatchSession(sessionID, path)
		}
	}
	ch = make(chan json.RawMessage, 256) // generous: a turn is a handful of JSONL lines, not a byte stream
	ts.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	unsubscribe = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		ts, ok := h.sessions[sessionID]
		if !ok {
			return
		}
		delete(ts.subscribers, ch)
		close(ch)
		if len(ts.subscribers) == 0 {
			delete(h.sessions, sessionID)
			if h.watcher != nil {
				h.watcher.UnwatchSession(sessionID)
			}
		}
	}
	return backlog, ch, unsubscribe, nil
}

// ensureTranscriptDir creates path's parent directory when it's missing,
// but only under an existing ~/.claude/projects-style root — never builds a
// whole tree from nothing.
func ensureTranscriptDir(path string) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err == nil {
		return
	}
	if _, err := os.Stat(filepath.Dir(dir)); err != nil {
		return
	}
	_ = os.Mkdir(dir, 0o755)
}

func (h *transcriptHub) onFileChange(sessionID, _ string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ts, ok := h.sessions[sessionID]
	if !ok {
		return
	}
	msgs, err := ts.cursor.readNew()
	if err != nil || len(msgs) == 0 {
		return
	}
	for ch := range ts.subscribers {
		for _, msg := range msgs {
			select {
			case ch <- msg:
			default: // stalled subscriber: drop rather than block the shared fsnotify-callback goroutine
			}
		}
	}
}
