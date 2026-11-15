package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// maxMessageSize bounds one socket message (one line).
const maxMessageSize = 16 * 1024 * 1024

// Send writes one hook message read from r to the socket at socketPath —
// what status-hook.sh calls (as `mo hooks send`) instead of depending on an
// external `nc`. The message is compacted to a single line first, since the
// server reads one JSON message per line and Claude's hook payload may be
// pretty-printed.
func Send(socketPath string, r io.Reader) error {
	return SendLogged(socketPath, r, "")
}

// SendLogged is Send, but first appends the compacted message to logPath
// when it's non-empty — a debugging aid for checking exactly which hook
// events Claude Code fires and what they carry (see `mo hooks send` and
// MO_HOOK_DEBUG_LOG). A logging failure never blocks delivery.
func SendLogged(socketPath string, r io.Reader, logPath string) error {
	data, err := io.ReadAll(io.LimitReader(r, maxMessageSize))
	if err != nil {
		return fmt.Errorf("read message: %w", err)
	}
	var line bytes.Buffer
	if err := json.Compact(&line, bytes.TrimSpace(data)); err != nil {
		return fmt.Errorf("message is not valid JSON: %w", err)
	}
	line.WriteByte('\n')

	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(line.Bytes())
			_ = f.Close()
		}
	}

	return writeLine(socketPath, line.Bytes())
}

// HookContext is what the hook script knows about the session an event
// came from, besides Claude's own JSON.
type HookContext struct {
	Event, SessionID, Cwd, Pane string
}

// SendHook wraps one hook event's JSON (read from r, Claude's stdin) in the
// envelope the server reads, and sends it. Claude's JSON goes in as is
// when it's valid, else as null; the rest is encoded here, so no character
// in a path can break the message.
func SendHook(socketPath string, r io.Reader, hc HookContext, logPath string) error {
	data, err := io.ReadAll(io.LimitReader(r, maxMessageSize))
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	input := json.RawMessage("null")
	if trimmed := bytes.TrimSpace(data); json.Valid(trimmed) && len(trimmed) > 0 {
		input = trimmed
	}
	msg, err := json.Marshal(struct {
		HookInput     json.RawMessage `json:"hook_input"`
		HookEventName string          `json:"hook_event_name"`
		SessionID     string          `json:"session_id"`
		ProjectPath   string          `json:"project_path"`
		TmuxPane      string          `json:"tmux_pane"`
		Timestamp     string          `json:"timestamp"`
	}{input, hc.Event, hc.SessionID, hc.Cwd, hc.Pane, time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return SendLogged(socketPath, bytes.NewReader(msg), logPath)
}

// ErrTUINotRunning means nothing is listening on the socket.
var ErrTUINotRunning = errors.New("the mo TUI isn't running")

// SendRestart asks the running TUI to restart itself and every sidebar
// (what ctrl+alt+r does), which also restarts mo web. It returns once the
// message is written, before the restart happens.
func SendRestart(socketPath string) error {
	if err := writeLine(socketPath, []byte(`{"type":"`+string(NotifyRestart)+`"}`+"\n")); err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return ErrTUINotRunning
		}
		return err
	}
	return nil
}

func writeLine(socketPath string, line []byte) error {
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write(line)
	return err
}
