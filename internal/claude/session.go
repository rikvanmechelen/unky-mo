package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// Session represents a running Claude Code session from ~/.claude/sessions/{PID}.json
type Session struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
}

// SessionStatus represents the detected state of a Claude session.
type SessionStatus int

const (
	StatusUnknown    SessionStatus = iota
	StatusActive                   // Claude is running (PID alive)
	StatusIdle                     // Waiting for user input (via notification)
	StatusPermission               // Needs permission approval (via notification)
	StatusDead                     // PID no longer running
)

func (s SessionStatus) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusIdle:
		return "needs input"
	case StatusPermission:
		return "permission!"
	case StatusDead:
		return "dead"
	default:
		return "unknown"
	}
}

// SessionsDir returns the path to Claude's sessions directory.
func SessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "sessions")
}

// ReadSessions reads all session files from the sessions directory.
func ReadSessions() ([]Session, error) {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// IsAlive checks if a process with the given PID is still running.
func IsAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// LiveSessions returns only sessions whose PID is still alive.
func LiveSessions() ([]Session, error) {
	all, err := ReadSessions()
	if err != nil {
		return nil, err
	}
	var live []Session
	for _, s := range all {
		if IsAlive(s.PID) {
			live = append(live, s)
		}
	}
	return live, nil
}

// SessionForPath returns the active session for a given working directory, if any.
func SessionForPath(path string) *Session {
	sessions, _ := LiveSessions()
	for _, s := range sessions {
		if s.CWD == path {
			return &s
		}
	}
	return nil
}
