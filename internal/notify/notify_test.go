package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationJSONRoundTrip(t *testing.T) {
	cases := []Notification{
		{
			Type:        NotifyIdlePrompt,
			SessionID:   "abc-123",
			ProjectPath: "/workspace/proj",
			Message:     "Claude is waiting",
			TmuxPane:    "%42",
			Timestamp:   time.Now().Truncate(time.Second),
		},
		{
			Type:        NotifyPermissionPrompt,
			SessionID:   "def-456",
			ProjectPath: "/workspace/other",
			Timestamp:   time.Now().Truncate(time.Second),
		},
		{
			Type:      NotifySessionStop,
			SessionID: "ghi-789",
			Timestamp: time.Now().Truncate(time.Second),
		},
	}

	for _, n := range cases {
		data, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got Notification
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Type != n.Type || got.SessionID != n.SessionID || got.ProjectPath != n.ProjectPath {
			t.Errorf("round-trip mismatch: got %+v want %+v", got, n)
		}
	}
}

func TestNotificationTypeConstantsMatchWire(t *testing.T) {
	// These string values are referenced by the hook script — they MUST be stable.
	cases := map[NotificationType]string{
		NotifyIdlePrompt:       "idle_prompt",
		NotifyPermissionPrompt: "permission_prompt",
		NotifySessionStop:      "session_stop",
		NotifyRestart:          "mo_restart",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("NotificationType %q: expected wire value %q", k, want)
		}
	}
}

// startServer runs a Server against a short socket path under /tmp (macOS caps
// Unix socket paths at 104 bytes; t.TempDir() paths exceed that).
func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mo-notify-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")
	srv := NewServer(path)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv, path
}

func TestServerAcceptsStopHookMessage(t *testing.T) {
	srv, path := startServer(t)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := map[string]string{
		"type":         "session_stop",
		"session_id":   "sess-1",
		"project_path": "/workspace/x",
		"tmux_pane":    "%1",
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case got := <-srv.Messages():
		if got.Type != NotifySessionStop {
			t.Errorf("type: want %q, got %q", NotifySessionStop, got.Type)
		}
		if got.SessionID != "sess-1" || got.ProjectPath != "/workspace/x" || got.TmuxPane != "%1" {
			t.Errorf("fields wrong: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for notification")
	}
}

func TestServerAcceptsClaudeHookPayload(t *testing.T) {
	srv, path := startServer(t)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Simulate notify-hook.sh wrapping Claude's stdin payload.
	claudePayload := map[string]string{
		"message":          "Claude needs input",
		"notificationType": "idle_prompt",
	}
	claudeJSON, _ := json.Marshal(claudePayload)

	outer := map[string]interface{}{
		"hook_input":   json.RawMessage(claudeJSON),
		"session_id":   "sess-2",
		"project_path": "/workspace/y",
		"tmux_pane":    "%2",
	}
	b, _ := json.Marshal(outer)
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case got := <-srv.Messages():
		if got.Type != NotifyIdlePrompt {
			t.Errorf("want idle_prompt, got %q", got.Type)
		}
		if got.Message != "Claude needs input" {
			t.Errorf("message: %q", got.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestServerDropsMalformedLine(t *testing.T) {
	srv, path := startServer(t)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// Malformed JSON should not block subsequent valid messages.
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	good := []byte(`{"type":"session_stop","session_id":"s"}` + "\n")
	if _, err := conn.Write(good); err != nil {
		t.Fatalf("write good: %v", err)
	}
	select {
	case got := <-srv.Messages():
		if got.Type != NotifySessionStop {
			t.Errorf("want session_stop, got %q", got.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout — malformed line may have killed the connection")
	}
}

func TestServerCleansUpSocketOnStop(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mo-notify-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")
	srv := NewServer(path)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket should exist while running: %v", err)
	}
	srv.Stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket should be removed after Stop, got err=%v", err)
	}
}

// A V2 message from status-hook.sh is passed through raw for
// status.ParseHookPayload, sent via Send exactly as the hook script does —
// including a pretty-printed payload and one far over bufio's 64KB default
// line limit (a PreToolUse Write carries the whole file).
func TestSendDeliversV2HookEventRaw(t *testing.T) {
	srv, path := startServer(t)

	big := strings.Repeat("x", 200*1024)
	msg := `{
  "hook_input": {"hook_event_name": "PreToolUse", "session_id": "sess-1",
                 "tool_name": "Write", "tool_input": {"content": "` + big + `"}},
  "hook_event_name": "PreToolUse",
  "session_id": "unknown",
  "project_path": "/workspace/x"
}`
	if err := Send(path, strings.NewReader(msg)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-srv.Messages():
		if got.Type != NotifyHookEvent || got.ProjectPath != "/workspace/x" {
			t.Fatalf("got %+v", got)
		}
		if bytes.ContainsRune(got.Raw, '\n') || !bytes.Contains(got.Raw, []byte(`"tool_name":"Write"`)) || !bytes.Contains(got.Raw, []byte(big)) {
			t.Fatalf("raw message should be the whole payload, compacted to one line (len %d)", len(got.Raw))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for hook event")
	}
}

func TestSendNoServerIsError(t *testing.T) {
	if err := Send(filepath.Join(t.TempDir(), "missing.sock"), strings.NewReader(`{}`)); err == nil {
		t.Fatal("expected an error when nothing is listening")
	}
}

func TestSendRestartDeliversRestart(t *testing.T) {
	srv, path := startServer(t)
	if err := SendRestart(path); err != nil {
		t.Fatalf("SendRestart: %v", err)
	}
	select {
	case got := <-srv.Messages():
		if got.Type != NotifyRestart {
			t.Fatalf("type: want %q, got %q", NotifyRestart, got.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for restart notification")
	}
}

func TestSendRestartNoServerIsTUINotRunning(t *testing.T) {
	err := SendRestart(filepath.Join(t.TempDir(), "missing.sock"))
	if !errors.Is(err, ErrTUINotRunning) {
		t.Fatalf("want ErrTUINotRunning, got %v", err)
	}
}

func TestSendRejectsNonJSON(t *testing.T) {
	_, path := startServer(t)
	if err := Send(path, strings.NewReader("not json")); err == nil {
		t.Fatal("expected an error for a non-JSON message")
	}
}

// The TUI stops the server explicitly before re-exec'ing on restart, and the
// deferred Stop runs again if that exec fails — a second Stop must not panic.
func TestServerStopIsIdempotent(t *testing.T) {
	srv, _ := startServer(t)
	srv.Stop()
	srv.Stop()
}

// SendLogged appends each compacted message to the debug log, even when no
// server is listening, and keeps delivering normally when it is.
func TestSendLoggedAppendsMessages(t *testing.T) {
	srv, path := startServer(t)
	logPath := filepath.Join(t.TempDir(), "hooks.log")

	if err := SendLogged(path, strings.NewReader(`{"hook_event_name": "Stop",  "session_id": "s1"}`), logPath); err != nil {
		t.Fatalf("SendLogged: %v", err)
	}
	select {
	case <-srv.Messages():
	case <-time.After(2 * time.Second):
		t.Fatal("message not delivered")
	}
	_ = SendLogged(filepath.Join(t.TempDir(), "missing.sock"), strings.NewReader(`{"hook_event_name":"Stop","session_id":"s2"}`), logPath)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	want := `{"hook_event_name":"Stop","session_id":"s1"}` + "\n" + `{"hook_event_name":"Stop","session_id":"s2"}` + "\n"
	if string(data) != want {
		t.Errorf("log:\n%s\nwant:\n%s", data, want)
	}
}

// The socket is the owner's alone, in a directory only the owner can enter,
// and a stale socket from an earlier run is replaced.
func TestServerSocketIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	sock := filepath.Join(dir, "hook.sock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	s := NewServer(sock)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if info, _ := os.Stat(sock); info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", info.Mode().Perm())
	}
	// Our own connections still get through.
	if err := Send(sock, strings.NewReader(`{"type":"idle","session_id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-s.Messages():
		if n.SessionID != "s1" {
			t.Errorf("got %+v", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message from this user not delivered")
	}
}

// A regular file at the socket path isn't removed to make room.
func TestServerLeavesNonSocketAlone(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hook.sock")
	if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(sock)
	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatal("started over a regular file")
	}
	if b, _ := os.ReadFile(sock); string(b) != "x" {
		t.Error("the file was replaced")
	}
}

// SendHook builds the envelope itself, so no character in a path breaks
// it (the hook script's printf version dropped every event of a checkout
// whose path had a quote or a backslash), and invalid stdin becomes null.
func TestSendHookEnvelope(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hook.sock")
	s := NewServer(sock)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	hc := HookContext{Event: "Stop", SessionID: "env-id", Cwd: `/ws/it"s \back`, Pane: "%3"}
	for _, in := range []string{`{"session_id":"s1","hook_event_name":"Stop"}`, "not json"} {
		if err := SendHook(sock, strings.NewReader(in), hc, ""); err != nil {
			t.Fatal(err)
		}
		select {
		case n := <-s.Messages():
			var env struct {
				HookInput   json.RawMessage `json:"hook_input"`
				ProjectPath string          `json:"project_path"`
				Event       string          `json:"hook_event_name"`
			}
			if err := json.Unmarshal(n.Raw, &env); err != nil || env.ProjectPath != hc.Cwd || env.Event != "Stop" || n.TmuxPane != "%3" {
				t.Errorf("%q: %s (%v)", in, n.Raw, err)
			}
			if in == "not json" && string(env.HookInput) != "null" {
				t.Errorf("invalid input forwarded: %s", env.HookInput)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: not delivered", in)
		}
	}
}
