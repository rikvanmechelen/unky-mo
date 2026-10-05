package status

import (
	"encoding/json"
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
	StatusQuestion                 // Blocked on an interactive tool (e.g. AskUserQuestion)
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
	case StatusQuestion:
		return "question"
	case StatusExternal:
		return "external"
	default:
		return "none"
	}
}

// HookEventType identifies the kind of hook event received from Claude Code.
type HookEventType string

const (
	EventUserPromptSubmit  HookEventType = "UserPromptSubmit"
	EventStop              HookEventType = "Stop"
	EventPreToolUse        HookEventType = "PreToolUse"
	EventPermissionRequest HookEventType = "PermissionRequest"
	EventSessionStart      HookEventType = "SessionStart"
	EventSessionEnd        HookEventType = "SessionEnd"
	EventNotificationIdle  HookEventType = "NotificationIdle"
	EventNotificationPerm  HookEventType = "NotificationPermission"
)

// HookEvent represents a parsed hook event from Claude Code.
type HookEvent struct {
	Type        HookEventType
	SessionID   string
	ProjectPath string
	ToolName    string          // populated for PreToolUse
	ToolInput   json.RawMessage // populated for PreToolUse
	Source      string          // populated for SessionStart: "startup", "resume", "clear" or "compact"
}

// StatusChange is emitted when a session's status transitions.
type StatusChange struct {
	SessionID string
	Old       SessionStatus
	New       SessionStatus
}

// sessionState tracks the current status of a single session.
type sessionState struct {
	Status       SessionStatus
	LastHookAt   time.Time
	PendingTool  string          // tool name Claude is blocked on, set iff Status == StatusQuestion
	PendingInput json.RawMessage // that tool's raw input, set iff Status == StatusQuestion
	// FromAgent marks a StatusQuestion or StatusPermission that came from
	// the `claude agents --json` signal rather than a hook (so there's no
	// PendingTool/PendingInput to show). Only that same signal may clear it
	// on "busy" — see ProcessAgentStatus.
	FromAgent bool
}

// isInteractiveTool reports whether a tool is known to block waiting on a
// human choice rather than running to completion on its own. Only
// AskUserQuestion today; easy to extend if others turn out to behave the
// same way.
func isInteractiveTool(name string) bool {
	return name == "AskUserQuestion"
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

// PendingQuestion returns the tool name + raw tool input Claude is currently
// blocked on for sessionID, if its status is StatusQuestion.
func (m *Manager) PendingQuestion(sessionID string) (tool string, input json.RawMessage, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, found := m.sessions[sessionID]; found && s.Status == StatusQuestion {
		return s.PendingTool, s.PendingInput, true
	}
	return "", nil, false
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
	var pendingTool string
	var pendingInput json.RawMessage
	remove := false

	switch evt.Type {
	case EventPreToolUse:
		if isInteractiveTool(evt.ToolName) {
			newStatus = StatusQuestion
			pendingTool = evt.ToolName
			pendingInput = evt.ToolInput
		} else {
			newStatus = StatusActive
		}
	case EventUserPromptSubmit:
		newStatus = StatusActive
	case EventSessionStart:
		// A session starts (or resumes, or /clears) at the prompt, waiting
		// for input — not working. Treating it as active left a freshly
		// launched session reading "Working" until its first turn ended,
		// which also locked the web composer out of sending that turn.
		// The exception is auto-compaction, which fires SessionStart
		// mid-turn while Claude carries on with the work.
		newStatus = StatusIdle
		if evt.Source == "compact" {
			newStatus = StatusActive
		}
	case EventStop:
		newStatus = StatusIdle
	case EventNotificationIdle:
		// "Waiting for your input" must not clear a menu that's still open:
		// marking it idle would let mo web type into a pending dialog.
		if cur := m.sessions[evt.SessionID]; cur != nil && (cur.Status == StatusQuestion || cur.Status == StatusPermission) {
			return
		}
		newStatus = StatusIdle
	case EventPermissionRequest:
		// Claude Code shows AskUserQuestion's menu through the permission
		// flow: PreToolUse(AskUserQuestion) → PermissionRequest naming the
		// same tool → Notification(permission_prompt). That's a question,
		// not a permission prompt (verified against captured payloads).
		if isInteractiveTool(evt.ToolName) {
			newStatus = StatusQuestion
			pendingTool = evt.ToolName
			pendingInput = evt.ToolInput
		} else {
			newStatus = StatusPermission
		}
	case EventNotificationPerm:
		// The notification names no tool. While a question is showing it's
		// that question's own prompt notification (it follows the
		// PermissionRequest above); a genuine permission prompt has already
		// been set by its PermissionRequest.
		if cur := m.sessions[evt.SessionID]; cur != nil && cur.Status == StatusQuestion {
			return
		}
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
	// Always refresh the pending question, even when the status itself
	// isn't transitioning (e.g. two AskUserQuestion calls back-to-back both
	// land on StatusQuestion — the second one's content must still replace
	// the first's, even though there's no status change to emit for it).
	s.PendingTool = pendingTool
	s.PendingInput = pendingInput
	s.FromAgent = false
	if old == newStatus {
		// No status transition — don't emit, but the pending-question
		// refresh above still applies.
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

	// Permission and Question are authoritative from hooks/the agents
	// signal — JSONL can't downgrade them. A tool blocked on a human is never
	// in the transcript, yet Claude Code keeps appending metadata entries
	// (titles, mode, attachments) while it waits, which would otherwise read
	// as "active" and wipe the pending question.
	if s.Status == StatusPermission || s.Status == StatusQuestion {
		return
	}

	old := s.Status
	if old == jsonlStatus {
		return
	}
	s.Status = jsonlStatus
	m.emit(StatusChange{SessionID: sessionID, Old: old, New: jsonlStatus})
}

// Values reported by `claude agents --json` (claude.Agent.Status/WaitingFor).
const (
	agentStatusBusy         = "busy"
	agentStatusIdle         = "idle"
	agentStatusWaiting      = "waiting"
	agentWaitingInputNeeded = "input needed"
	// Claude Code's default for a dialog with no reason of its own: tool
	// permission prompts, ExitPlanMode's plan approval.
	agentWaitingPermission = "permission prompt"
)

// ProcessAgentStatus reconciles a session against Claude Code's own live
// status, as reported by `claude agents --json` at observedAt. This is the
// backstop for StatusQuestion and StatusPermission when the hooks didn't reach
// us (hooks not installed / dropped, or the TUI restarted while a dialog was
// open): Claude reports "waiting" + "input needed" while an AskUserQuestion
// menu is open, and "waiting" + "permission prompt" for a permission dialog.
// Only those pairs count — other "waiting" reasons ("dialog open", …) are
// left alone — and neither replaces a question or permission already set.
//
// A hook that arrived after observedAt is fresher than this snapshot, so the
// snapshot is ignored. Leaving a blocked state: "idle" clears any question
// and a permission this signal set (the session is neither working nor
// blocked); "busy" clears only a state this signal set itself — a
// hook-sourced one can briefly read "busy" before Claude publishes its
// waiting state, and clearing it would lose its content.
func (m *Manager) ProcessAgentStatus(sessionID, agentStatus, waitingFor string, observedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if ok && s.LastHookAt.After(observedAt) {
		return
	}
	old := StatusNone
	if ok {
		old = s.Status
	}

	var newStatus SessionStatus
	switch {
	case agentStatus == agentStatusWaiting && (waitingFor == agentWaitingInputNeeded || waitingFor == agentWaitingPermission):
		if old == StatusQuestion || old == StatusPermission {
			return
		}
		newStatus = StatusQuestion
		if waitingFor == agentWaitingPermission {
			newStatus = StatusPermission
		}
	case old != StatusQuestion && old != StatusPermission:
		return
	case agentStatus == agentStatusIdle && (old == StatusQuestion || s.FromAgent):
		newStatus = StatusIdle
	case agentStatus == agentStatusBusy && s.FromAgent:
		newStatus = StatusActive
	default:
		return
	}

	if !ok {
		s = &sessionState{}
		m.sessions[sessionID] = s
	}
	s.Status = newStatus
	s.PendingTool, s.PendingInput = "", nil
	s.FromAgent = newStatus == StatusQuestion || newStatus == StatusPermission
	m.emit(StatusChange{SessionID: sessionID, Old: old, New: newStatus})
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
