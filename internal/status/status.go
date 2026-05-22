package status

import (
	"sync"
	"time"
)

// SessionStatus represents the detected state of a Claude session.
type SessionStatus int

const (
	StatusNone       SessionStatus = iota
	StatusActive                   // Claude is processing (generating, running tools)
	StatusIdle                     // Waiting for user input
	StatusPermission               // Needs permission approval
	StatusExternal                 // Live Claude running outside mo's tmux
)

func (s SessionStatus) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusIdle:
		return "idle"
	case StatusPermission:
		return "permission"
	case StatusExternal:
		return "external"
	default:
		return "none"
	}
}

// HookEventType identifies the kind of hook event received from Claude Code.
type HookEventType string

const (
	EventUserPromptSubmit   HookEventType = "UserPromptSubmit"
	EventStop               HookEventType = "Stop"
	EventPreToolUse         HookEventType = "PreToolUse"
	EventPermissionRequest  HookEventType = "PermissionRequest"
	EventSessionStart       HookEventType = "SessionStart"
	EventSessionEnd         HookEventType = "SessionEnd"
	EventNotificationIdle   HookEventType = "NotificationIdle"
	EventNotificationPerm   HookEventType = "NotificationPermission"
)

// HookEvent represents a parsed hook event from Claude Code.
type HookEvent struct {
	Type        HookEventType
	SessionID   string
	ProjectPath string
	ToolName    string // populated for PreToolUse
}

// StatusChange is emitted when a session's status transitions.
type StatusChange struct {
	SessionID string
	Old       SessionStatus
	New       SessionStatus
}

// sessionState tracks the current status of a single session.
type sessionState struct {
	Status     SessionStatus
	LastHookAt time.Time
}

// Manager is the central source of truth for all session statuses.
// It receives signals from hooks, JSONL watchers, and PID liveness checks.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*sessionState
	subs     []chan StatusChange

	// readJSONL is the function used to read JSONL status for reconciliation.
	// Injected so tests can substitute a fake.
	readJSONL func(path string) SessionStatus
}

// NewManager creates a new session status manager.
func NewManager() *Manager {
	return &Manager{
		sessions:  make(map[string]*sessionState),
		readJSONL: ReadJSONLStatus,
	}
}

// Status returns the current status for a session, or StatusNone if unknown.
func (m *Manager) Status(sessionID string) SessionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[sessionID]; ok {
		return s.Status
	}
	return StatusNone
}

// AllStatuses returns a snapshot of all tracked session statuses.
func (m *Manager) AllStatuses() map[string]SessionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]SessionStatus, len(m.sessions))
	for id, s := range m.sessions {
		out[id] = s.Status
	}
	return out
}

// Subscribe returns a channel that receives status changes. The channel
// has a buffer of 64; slow consumers will miss events (non-blocking send).
func (m *Manager) Subscribe() <-chan StatusChange {
	ch := make(chan StatusChange, 64)
	m.mu.Lock()
	m.subs = append(m.subs, ch)
	m.mu.Unlock()
	return ch
}

// ProcessHookEvent applies a hook event to the state machine.
func (m *Manager) ProcessHookEvent(evt HookEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var newStatus SessionStatus
	remove := false

	switch evt.Type {
	case EventUserPromptSubmit, EventPreToolUse, EventSessionStart:
		newStatus = StatusActive
	case EventStop, EventNotificationIdle:
		newStatus = StatusIdle
	case EventPermissionRequest, EventNotificationPerm:
		newStatus = StatusPermission
	case EventSessionEnd:
		remove = true
	default:
		return
	}

	if remove {
		if s, ok := m.sessions[evt.SessionID]; ok {
			old := s.Status
			delete(m.sessions, evt.SessionID)
			m.emit(StatusChange{SessionID: evt.SessionID, Old: old, New: StatusNone})
		}
		return
	}

	s, ok := m.sessions[evt.SessionID]
	if !ok {
		s = &sessionState{}
		m.sessions[evt.SessionID] = s
	}
	old := s.Status
	if old == newStatus {
		// No transition — don't emit.
		s.LastHookAt = time.Now()
		return
	}
	s.Status = newStatus
	s.LastHookAt = time.Now()
	m.emit(StatusChange{SessionID: evt.SessionID, Old: old, New: newStatus})
}

// ProcessJSONLChange reconciles the status of a session by re-reading its
// JSONL file. This is called by the fsnotify watcher when the file changes.
// JSONL reconciliation can correct a stale hook state (e.g., hook was dropped)
// but it does NOT override Permission status (hooks are authoritative for that).
func (m *Manager) ProcessJSONLChange(sessionID, path string) {
	jsonlStatus := m.readJSONL(path)
	if jsonlStatus == StatusNone {
		return // can't determine — don't change anything
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		// Session not yet tracked by hooks — create from JSONL.
		m.sessions[sessionID] = &sessionState{Status: jsonlStatus}
		m.emit(StatusChange{SessionID: sessionID, Old: StatusNone, New: jsonlStatus})
		return
	}

	// Permission is authoritative from hooks — JSONL can't downgrade it.
	if s.Status == StatusPermission {
		return
	}

	old := s.Status
	if old == jsonlStatus {
		return
	}
	s.Status = jsonlStatus
	m.emit(StatusChange{SessionID: sessionID, Old: old, New: jsonlStatus})
}

// MarkDead removes a session whose PID is no longer alive.
func (m *Manager) MarkDead(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		old := s.Status
		delete(m.sessions, sessionID)
		m.emit(StatusChange{SessionID: sessionID, Old: old, New: StatusNone})
	}
}

// emit sends a status change to all subscribers (non-blocking).
// Must be called with m.mu held.
func (m *Manager) emit(change StatusChange) {
	for _, ch := range m.subs {
		select {
		case ch <- change:
		default:
		}
	}
}
