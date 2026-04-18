package notify

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("NotificationType %q: expected wire value %q", k, want)
		}
	}
}

// startServer runs a Server against a socket in t.TempDir(); callers must call
// Stop before the test ends.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.sock")
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
	path := filepath.Join(t.TempDir(), "notify.sock")
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
