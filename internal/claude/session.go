package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
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

// RecentSession represents a historical Claude Code session from the JSONL files.
type RecentSession struct {
	SessionID  string
	Summary    string // first user message, truncated
	GitBranch  string
	LastActive time.Time
	IsLive     bool // PID is still running
}

// projectsDirForPath returns the Claude projects subdirectory for a given project path.
// e.g. /Users/rvanmech/workspace/my-app -> ~/.claude/projects/-Users-rvanmech-workspace-my-app
func projectsDirForPath(projectPath string) string {
	home, _ := os.UserHomeDir()
	encoded := "-" + strings.ReplaceAll(strings.TrimPrefix(projectPath, "/"), "/", "-")
	return filepath.Join(home, ".claude", "projects", encoded)
}

// RecentSessions returns historical sessions for a project, sorted by most recent first.
// maxResults limits how many to return (0 = all).
func RecentSessions(projectPath string, maxResults int) []RecentSession {
	dir := projectsDirForPath(projectPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Build set of live session IDs for marking
	liveSessions, _ := LiveSessions()
	liveSet := make(map[string]bool)
	for _, s := range liveSessions {
		liveSet[s.SessionID] = true
	}

	var results []RecentSession
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		fullPath := filepath.Join(dir, entry.Name())

		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary, branch := parseSessionJSONL(fullPath)

		results = append(results, RecentSession{
			SessionID:  sessionID,
			Summary:    summary,
			GitBranch:  branch,
			LastActive: info.ModTime(),
			IsLive:     liveSet[sessionID],
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].LastActive.After(results[j].LastActive)
	})

	if maxResults > 0 && len(results) > maxResults {
		results = results[:maxResults]
	}

	return results
}

// parseSessionJSONL reads the first user message and git branch from a session file.
// It only reads the first few lines to stay fast on large files.
func parseSessionJSONL(path string) (summary, branch string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // handle long lines

	linesRead := 0
	for scanner.Scan() {
		linesRead++
		if linesRead > 30 { // only scan first 30 lines
			break
		}

		line := scanner.Bytes()
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			GitBranch string `json:"gitBranch"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		// Extract first user message as summary
		if summary == "" && msg.Type == "user" {
			summary = extractTextContent(msg.Message.Content)
		}

		// Extract git branch from assistant messages
		if branch == "" && msg.GitBranch != "" {
			branch = msg.GitBranch
		}

		if summary != "" && branch != "" {
			break
		}
	}

	return summary, branch
}

func extractTextContent(raw json.RawMessage) string {
	// Try as plain string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return truncate(s, 80)
	}

	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return truncate(b.Text, 80)
			}
		}
	}

	return ""
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = stripXMLTags(s)
	s = strings.TrimSpace(s)
	// Collapse multiple spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

// stripXMLTags removes XML/HTML tags from a string.
func stripXMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}
