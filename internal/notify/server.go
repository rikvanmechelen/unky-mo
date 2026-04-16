package notify

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"
)

// Server listens on a Unix domain socket for notifications from Claude Code hooks.
type Server struct {
	socketPath string
	listener   net.Listener
	msgChan    chan Notification
	done       chan struct{}
	wg         sync.WaitGroup
}

// NewServer creates a notification server.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		msgChan:    make(chan Notification, 64),
		done:       make(chan struct{}),
	}
}

// Start begins listening for notifications.
func (s *Server) Start() error {
	// Clean up stale socket
	os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = ln

	// Make socket writable by anyone (hooks run as the same user, but be safe)
	os.Chmod(s.socketPath, 0666)

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop shuts down the server and cleans up.
func (s *Server) Stop() {
	close(s.done)
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	os.Remove(s.socketPath)
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
		s.wg.Add(1)
		go s.handleConn(conn)
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
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var input hookInput
		if err := json.Unmarshal(line, &input); err != nil {
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

		select {
		case s.msgChan <- notif:
		default:
			// Drop if channel is full
		}
	}
}
