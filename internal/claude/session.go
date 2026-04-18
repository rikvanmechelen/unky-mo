package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// parentPID returns the PPID of the given process, or 0 if it can't be read.
// Reads /proc/<pid>/status (Linux-specific; returns 0 on other OSes).
func parentPID(pid int) int {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "PPid:") {
			var ppid int
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, "PPid:"), "%d", &ppid); err != nil {
				return 0
			}
			return ppid
		}
	}
	return 0
}

// IsDescendantOf reports whether pid has any ancestor in the given set,
// walking up the PPID chain. Returns false if pid == 0 or the chain ends
// before hitting a host (e.g. an orphaned process reparented to init).
func IsDescendantOf(pid int, hostPIDs map[int]bool) bool {
	for p := pid; p > 1; p = parentPID(p) {
		if hostPIDs[p] {
			return true
		}
	}
	return false
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
// When multiple sessions share a CWD (concurrent siblings), returns the first
// one encountered. Callers that need to enumerate should use SessionsForPath.
func SessionForPath(path string) *Session {
	sessions, _ := LiveSessions()
	for _, s := range sessions {
		if s.CWD == path {
			return &s
		}
	}
	return nil
}

// SessionsForPath returns every live session whose CWD matches path.
// Used by guards that must account for all concurrent sessions at a
// checkout (e.g. "refuse main-branch checkout if any session is running
// in the main worktree").
func SessionsForPath(path string) []Session {
	sessions, _ := LiveSessions()
	var matching []Session
	for _, s := range sessions {
		if s.CWD == path {
			matching = append(matching, s)
		}
	}
	return matching
}

// SessionMessage represents a single user or assistant message from a session.
type SessionMessage struct {
	Role    string // "user" or "assistant"
	Content string // truncated text content
}

// LastMessages returns the last N user/assistant messages from a session JSONL.
func LastMessages(projectPath, sessionID string, count int) []SessionMessage {
	dir := ProjectsDirForPath(projectPath)
	path := filepath.Join(dir, sessionID+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}

	// Read the last ~256KB to find recent messages
	readSize := int64(256 * 1024)
	if info.Size() < readSize {
		readSize = info.Size()
	}
	if readSize == 0 {
		return nil
	}

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, info.Size()-readSize)
	if err != nil {
		return nil
	}

	// Parse lines from the end, collecting user/assistant messages
	lines := strings.Split(string(buf), "\n")
	var messages []SessionMessage

	for i := len(lines) - 1; i >= 0 && len(messages) < count*2; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		if msg.Type != "user" && msg.Type != "assistant" {
			continue
		}

		text := extractTextContent(msg.Message.Content)
		if text == "" {
			continue
		}

		messages = append(messages, SessionMessage{
			Role:    msg.Type,
			Content: truncate(text, 120),
		})
	}

	// Reverse to get chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// Keep only the last `count` messages
	if len(messages) > count {
		messages = messages[len(messages)-count:]
	}

	return messages
}

// IsSessionIdle checks whether a live session is waiting for user input or
// is stuck on a permission prompt. Checks the last assistant message's
// stop_reason: "end_turn" = idle (Claude finished), "tool_use" = still working.
// Falls back to JSONL staleness (>120s with no writes) for edge cases like
// permission prompts that don't produce assistant messages.
func IsSessionIdle(projectPath, sessionID string) bool {
	dir := ProjectsDirForPath(projectPath)
	path := filepath.Join(dir, sessionID+".jsonl")

	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	readSize := int64(128 * 1024)
	if info.Size() < readSize {
		readSize = info.Size()
	}
	if readSize == 0 {
		return false
	}

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, info.Size()-readSize)
	if err != nil {
		return false
	}

	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var msg struct {
			Type    string `json:"type"`
			Message struct {
				StopReason string `json:"stop_reason"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		switch msg.Type {
		case "file-history-snapshot", "attachment", "permission-mode",
			"custom-title", "deferred_tools_delta", "":
			continue
		case "assistant":
			// end_turn = Claude finished, waiting for input
			// tool_use = Claude is mid-turn, running a tool
			return msg.Message.StopReason == "end_turn"
		case "user":
			// User sent something. Normally Claude would be mid-response —
			// but /compact and other slash commands write synthetic user
			// entries (content starts with <local-command-*> / <command-*>)
			// with no assistant follow-up, leaving the session actually
			// idle. If the JSONL hasn't advanced in >120s, the session is
			// waiting for real input.
			return time.Since(info.ModTime()) > 120*time.Second
		default:
			continue
		}
	}

	// Fallback: if JSONL hasn't been written to in >120s, the session is
	// likely stuck on a permission prompt or similar (no assistant message).
	return time.Since(info.ModTime()) > 120*time.Second
}

// RecentSession represents a historical Claude Code session from the JSONL files.
type RecentSession struct {
	SessionID string
	Title     string // descriptive name from Claude (e.g. "unky-mo-session-orchestrator")
	Summary   string // first user message, truncated
	GitBranch string
	LastActive time.Time
	IsLive     bool // PID is still running
}

// DisplayName returns the best available name for the session.
func (rs RecentSession) DisplayName() string {
	if rs.Title != "" {
		return rs.Title
	}
	if len(rs.SessionID) >= 8 {
		return rs.SessionID[:8] + "..."
	}
	return rs.SessionID
}

// ProjectsDirForPath returns the Claude projects subdirectory for a given project path.
// Claude encodes paths by replacing "/", "_", and "." with "-".
// e.g. /Users/rvanmech/workspace/mla_wrapper_app -> ~/.claude/projects/-Users-rvanmech-workspace-mla-wrapper-app
// e.g. /Users/.../unky-mo.worktrees/testing_worktrees -> -Users-...-unky-mo-worktrees-testing-worktrees
func ProjectsDirForPath(projectPath string) string {
	home, _ := os.UserHomeDir()
	encoded := strings.TrimPrefix(projectPath, "/")
	encoded = strings.ReplaceAll(encoded, "/", "-")
	encoded = strings.ReplaceAll(encoded, "_", "-")
	encoded = strings.ReplaceAll(encoded, ".", "-")
	encoded = "-" + encoded
	return filepath.Join(home, ".claude", "projects", encoded)
}

// RecentSessions returns historical sessions for a project, sorted by most recent first.
// maxResults limits how many to return (0 = all).
func RecentSessions(projectPath string, maxResults int) []RecentSession {
	dir := ProjectsDirForPath(projectPath)
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

		title, summary, branch := parseSessionJSONL(fullPath)

		results = append(results, RecentSession{
			SessionID:  sessionID,
			Title:      title,
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

// SessionTitle returns the custom title for a session JSONL, or "" if none is set.
func SessionTitle(path string) string {
	title, _, _ := parseSessionJSONL(path)
	return title
}

// CustomTitleFor returns the most recent custom-title entry from the JSONL
// for the given (projectPath, sessionID), or "" if none has been set.
// Used to sync tmux window names with /rename calls inside Claude.
func CustomTitleFor(projectPath, sessionID string) string {
	path := filepath.Join(ProjectsDirForPath(projectPath), sessionID+".jsonl")
	return SessionTitle(path)
}

// parseSessionJSONL reads the first user message and git branch from a session file.
// It only reads the first few lines to stay fast on large files.
func parseSessionJSONL(path string) (title, summary, branch string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // handle long lines

	for scanner.Scan() {
		line := scanner.Bytes()

		// Quick check for custom-title lines (can appear anywhere in the file)
		// These are small JSON objects, so parsing is cheap.
		if len(line) < 200 && strings.Contains(string(line), `"custom-title"`) {
			var ct struct {
				Type        string `json:"type"`
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &ct) == nil && ct.Type == "custom-title" && ct.CustomTitle != "" {
				title = ct.CustomTitle // keep updating — last one wins
			}
			continue
		}

		// Only parse the first 30 lines for summary/branch (these are large JSON objects)
		if summary != "" && branch != "" {
			continue
		}

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

		if summary == "" && msg.Type == "user" {
			summary = extractTextContent(msg.Message.Content)
		}

		if branch == "" && msg.GitBranch != "" {
			branch = msg.GitBranch
		}
	}

	return title, summary, branch
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
