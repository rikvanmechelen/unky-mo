// Package agents defines agent-agnostic interfaces for session detection and
// management. Each coding agent (Claude, Gemini, etc.) implements
// SessionReader; the MultiReader aggregates multiple implementations.
package agents

import "time"

// Session is agent-agnostic session metadata.
type Session struct {
	AgentKey  string // mnemonic from config (e.g. "c", "g"); empty = Claude default
	PID       int
	SessionID string
	CWD       string
	StartedAt int64
	Kind      string
	Name      string
}

// SessionMessage is a single message from a session conversation.
type SessionMessage struct {
	Role    string
	Content string
}

// RecentSession is a historical session entry.
type RecentSession struct {
	AgentKey   string
	SessionID  string
	Title      string
	Summary    string
	GitBranch  string
	LastActive time.Time
	IsLive     bool
}

// ActiveShell represents a subprocess running under the agent.
type ActiveShell struct {
	PID        int
	Command    string
	OutputFile string
	StartTime  time.Time
}

// SessionReader is the interface each coding agent implements for session
// detection, idle detection, and history access. Agents that don't support
// a capability return zero values.
type SessionReader interface {
	// LiveSessions returns all currently running sessions for this agent.
	LiveSessions() ([]Session, error)

	// SessionForPath returns the live session at the given cwd, or nil.
	SessionForPath(path string) *Session

	// SessionsForPath returns all live sessions at the given cwd.
	SessionsForPath(path string) []Session

	// IsAlive reports whether the given PID is still running.
	IsAlive(pid int) bool

	// IsDescendantOf checks whether pid has an ancestor in hostPIDs.
	IsDescendantOf(pid int, hostPIDs map[int]bool) bool

	// IsSessionIdle reports whether the session is waiting for user input.
	IsSessionIdle(projectPath, sessionID string) bool

	// CustomTitleFor returns the user-set session title, or "".
	CustomTitleFor(projectPath, sessionID string) string

	// LastMessages returns the most recent conversation messages.
	LastMessages(projectPath, sessionID string, count int) []SessionMessage

	// RecentSessions returns historical sessions for the project.
	RecentSessions(projectPath string, maxResults int) []RecentSession

	// ProjectsDirForPath returns the agent's projects directory for the
	// given project path, or "" if the agent has no local storage.
	ProjectsDirForPath(projectPath string) string

	// ActiveShellsForSession returns subprocesses running under the agent.
	ActiveShellsForSession(projectPath string) []ActiveShell

	// FormatShellCommand formats a shell command for display.
	FormatShellCommand(cmd string, maxLen int) string
}
