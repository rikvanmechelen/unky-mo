package notify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"path/filepath"
	"net"
	"os"
	"sync"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
)

// Server listens on a Unix domain socket for notifications from Claude Code hooks.
type Server struct {
	socketPath string
	listener   net.Listener
	msgChan    chan Notification
	done       chan struct{}
	wg         sync.WaitGroup
	stopOnce   sync.Once
	slots      chan struct{}
}

// NewServer creates a notification server.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		msgChan:    make(chan Notification, 64),
		done:       make(chan struct{}),
		slots:      make(chan struct{}, maxConns),
	}
}

// Start begins listening for notifications. The socket's directory must be
// one only this user can enter (config.PrivateStateDir), the socket itself
// is the owner's only, and on Linux a connection from another user is
// closed unread: hook events steer session status and carry prompts and
// tool input, so nobody else may send them, or take the path first to
// receive them.
func (s *Server) Start() error {
	if err := config.PrivateStateDir(filepath.Dir(s.socketPath)); err != nil {
		return fmt.Errorf("socket directory: %w", err)
	}
	// A stale socket from a previous run is removed; anything else at the
	// path is left alone and Listen reports it.
	if info, err := os.Lstat(s.socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
		os.Remove(s.socketPath)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = ln
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// maxConns caps the connections handled at once: hooks connect, write a
// line and hang up, so more than this is a flood.
const maxConns = 64

// Stop shuts down the server and cleans up. Safe to call more than once
// (the TUI stops it explicitly before re-exec'ing on restart, and again via
// defer if that exec fails).
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		if s.listener != nil {
			s.listener.Close()
		}
		s.wg.Wait()
		os.Remove(s.socketPath)
	})
}

// Messages returns the channel to receive notifications from.
func (s *Server) Messages() <-chan Notification {
	return s.msgChan
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}
		if !samePeer(conn) {
			conn.Close()
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			conn.Close() // flooded: drop it rather than queue goroutines
			continue
		}
		s.wg.Add(1)
		go func() {
			defer func() { <-s.slots }()
			s.handleConn(conn)
		}()
	}
}

// hookInput is the JSON structure sent by the notify-hook.sh script.
type hookInput struct {
	HookInput   json.RawMessage `json:"hook_input"`
	SessionID   string          `json:"session_id"`
	ProjectPath string          `json:"project_path"`
	TmuxPane    string          `json:"tmux_pane"`
	Timestamp   string          `json:"timestamp"`
	Type        string          `json:"type"` // direct type field for stop hook
	// HookEventName marks a V2 message from status-hook.sh.
	HookEventName string `json:"hook_event_name"`
}

// claudeHookPayload is the JSON Claude provides to notification hooks on stdin.
type claudeHookPayload struct {
	Message          string `json:"message"`
	NotificationType string `json:"notificationType"`
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	scanner := bufio.NewScanner(conn)
	// A PreToolUse payload carries the tool's whole input (e.g. a Write's
	// file content), so lines can be far beyond bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var input hookInput
		if err := json.Unmarshal(line, &input); err != nil {
			continue
		}

		// V2 hook messages are interpreted by status.ParseHookPayload, not
		// here — pass them through whole.
		if input.HookEventName != "" {
			s.deliver(Notification{
				Type:        NotifyHookEvent,
				SessionID:   input.SessionID,
				ProjectPath: input.ProjectPath,
				TmuxPane:    input.TmuxPane,
				Timestamp:   time.Now(),
				Raw:         append([]byte(nil), line...),
			})
			continue
		}

		notif := Notification{
			SessionID:   input.SessionID,
			ProjectPath: input.ProjectPath,
			TmuxPane:    input.TmuxPane,
			Timestamp:   time.Now(),
		}

		// Parse timestamp if provided
		if input.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, input.Timestamp); err == nil {
				notif.Timestamp = t
			}
		}

		// Determine notification type
		if input.Type != "" {
			// Direct type (from stop hook)
			notif.Type = NotificationType(input.Type)
		} else if len(input.HookInput) > 0 {
			// Parse Claude's hook payload
			var payload claudeHookPayload
			if err := json.Unmarshal(input.HookInput, &payload); err == nil {
				notif.Type = NotificationType(payload.NotificationType)
				notif.Message = payload.Message
			}
		}

		if notif.Type == "" {
			continue
		}
		s.deliver(notif)
	}
}

func (s *Server) deliver(n Notification) {
	select {
	case s.msgChan <- n:
	default:
		// Drop if channel is full
	}
}
