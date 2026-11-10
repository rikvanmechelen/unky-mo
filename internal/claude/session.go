package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// sessionsCommander is the exec seam ReadSessions/LiveSessions shell out
// through. Overridden in tests with a gomock Commander.
var sessionsCommander moexec.Commander = moexec.DefaultCommander

// Session represents a running or recently-finished Claude Code session, as
// reported by `claude agents --json`.
type Session struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
	// Status/WaitingFor are Claude Code's own live view of the session
	// (see Agent). Fed into status.Manager as a reconciliation signal.
	Status     string `json:"status,omitempty"`
	WaitingFor string `json:"waitingFor,omitempty"`
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

// ReadSessions returns every session `claude agents --json --all` reports:
// live sessions plus recently-finished background ones. Callers that only
// want currently-running sessions should use LiveSessions.
func ReadSessions() ([]Session, error) {
	return sessionsViaCLI(true)
}

// sessionsViaCLI shells out to `claude agents --json` (the CLI's own
// session-discovery command) and maps its output onto Session.
func sessionsViaCLI(all bool) ([]Session, error) {
	agents, err := LiveAgents(context.Background(), sessionsCommander, all)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(agents))
	for _, a := range agents {
		sessions = append(sessions, Session{
			PID:        a.PID,
			SessionID:  a.SessionID,
			CWD:        a.CWD,
			StartedAt:  a.StartedAt,
			Kind:       a.Kind,
			Name:       a.Name,
			Status:     a.Status,
			WaitingFor: a.WaitingFor,
		})
	}
	return sessions, nil
}

// IsAlive checks if a process with the given PID is still running.
func IsAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// ppidTableTTL bounds how stale the ps(1) process table may be. A pid's
// parent effectively never changes (barring reparenting to init when the
// parent dies), and this only has to stay fresh relative to the TUI's 5s
// session poll, so one ps call covers a whole poll's worth of lookups.
const ppidTableTTL = 2 * time.Second

// maxAncestorHops caps the ancestry walk. A real chain is a handful of
// hops; the cap is insurance against a malformed table producing a cycle
// and hanging the poll goroutine.
const maxAncestorHops = 64

// ppidMissTTL throttles the re-read triggered when a pid isn't in the
// cached table. A miss usually means the table predates the process (a
// session launched since the last read, which the 1s sidebar poll would
// otherwise not see for a full ppidTableTTL), so it's worth a fresh look —
// but a miss is also what a dead pid looks like, and those must not spawn
// a ps per lookup.
const ppidMissTTL = 250 * time.Millisecond

var (
	ppidMu    sync.Mutex
	ppidCache map[int]int
	ppidAt    time.Time
)

// ppidTable returns a pid→ppid snapshot of every process, read from ps(1)
// at most once per ppidTableTTL. Used only on systems without /proc.
//
// Reading the whole table at once is deliberate: IsDescendantOf walks a
// chain per call and is itself called once per (window × session) on every
// poll, so a per-pid `ps -p` would spawn dozens of processes every 5s.
func ppidTable() map[int]int { return ppidTableFor(0) }

// ppidTableFor returns the cached table, re-reading it when stale — or,
// when want is a pid the cached table doesn't know and the table is at
// least ppidMissTTL old, to pick up a process created since the last read.
func ppidTableFor(want int) map[int]int {
	ppidMu.Lock()
	defer ppidMu.Unlock()
	if ppidCache != nil && time.Since(ppidAt) < ppidTableTTL {
		if _, known := ppidCache[want]; known || want == 0 || time.Since(ppidAt) < ppidMissTTL {
			return ppidCache
		}
	}
	// -A (all processes) is spelled the same on macOS and Linux, and
	// includes the detached daemons Claude Code's background agents run
	// under. Empty headers (`pid=`) keep the output to bare columns.
	out, _, err := sessionsCommander.Output(context.Background(), "", "ps", "-Ao", "pid=,ppid=")
	if err != nil {
		return ppidCache // keep the previous snapshot rather than losing the table
	}
	table := make(map[int]int, 512)
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		pidStr, ppidStr, ok := strings.Cut(strings.TrimSpace(s.Text()), " ")
		if !ok {
			continue
		}
		pid, err1 := strconv.Atoi(strings.TrimSpace(pidStr))
		ppid, err2 := strconv.Atoi(strings.TrimSpace(ppidStr))
		if err1 != nil || err2 != nil {
			continue
		}
		table[pid] = ppid
	}
	ppidCache, ppidAt = table, time.Now()
	return table
}

// resetPPIDTable drops the cached process table. Tests only.
func resetPPIDTable() {
	ppidMu.Lock()
	ppidCache, ppidAt = nil, time.Time{}
	ppidMu.Unlock()
}

// seedPPIDTable installs a process table as though it had been read age ago,
// so cache-staleness paths are testable without sleeping. Tests only.
func seedPPIDTable(table map[int]int, age time.Duration) {
	ppidMu.Lock()
	ppidCache, ppidAt = table, time.Now().Add(-age)
	ppidMu.Unlock()
}

// parentPID returns the PPID of the given process, or 0 if it can't be
// determined. Reads /proc/<pid>/status on Linux; falls back to a cached
// ps(1) process table (via the same sessionsCommander seam ReadSessions
// and LiveSessions use) on OSes with no /proc, macOS included.
//
// IsDescendantOf's ancestry walk depends on this working everywhere mo
// runs: a Claude process more than one fork removed from its tmux pane PID
// (e.g. Claude Code's background-agent chain, pane -> daemon run ->
// bg-pty-host -> session) was wrongly classified as StatusExternal while
// this silently returned 0 past the first hop on macOS.
func parentPID(pid int) int {
	if f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := s.Text()
			if strings.HasPrefix(line, "PPid:") {
				var ppid int
				if _, err := fmt.Sscanf(strings.TrimPrefix(line, "PPid:"), "%d", &ppid); err == nil {
					return ppid
				}
				return 0
			}
		}
		return 0
	}
	return ppidTableFor(pid)[pid]
}

// IsDescendantOf reports whether pid has any ancestor in the given set,
// walking up the PPID chain. Returns false if pid == 0 or the chain ends
// before hitting a host (e.g. an orphaned process reparented to init).
func IsDescendantOf(pid int, hostPIDs map[int]bool) bool {
	if len(hostPIDs) == 0 {
		return false
	}
	for p, hops := pid, 0; p > 1 && hops < maxAncestorHops; p, hops = parentPID(p), hops+1 {
		if hostPIDs[p] {
			return true
		}
	}
	return false
}

// LiveSessions returns only currently-running sessions. `claude agents
// --json` already filters to live PIDs; the IsAlive check here is a cheap
// defensive re-check against the gap between that snapshot and our use of it.
func LiveSessions() ([]Session, error) {
	sessions, err := sessionsViaCLI(false)
	if err != nil {
		return nil, err
	}
	var live []Session
	for _, s := range sessions {
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
				StopReason string          `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
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
			// end_turn = Claude finished, waiting for input.
			if msg.Message.StopReason == "end_turn" {
				return true
			}
			// tool_use = Claude is mid-turn. Normally still working, but
			// some tools block on the user (AskUserQuestion, permission
			// prompts that the hook missed) and never write a follow-up
			// tool_result on their own. If the JSONL hasn't advanced in
			// >120s the tool is almost certainly waiting on input — treat
			// as idle so the session lights up instead of staying orange.
			return time.Since(info.ModTime()) > 120*time.Second
		case "user":
			// User messages come in three flavors:
			//   1. tool_result — Claude is mid-turn, always still working
			//   2. Regular prompt — Claude should be generating a response
			//   3. Slash commands (/compact etc.) — synthetic entries that
			//      may have no assistant follow-up
			//
			// For (1) and (2), Claude is actively working and may take
			// several minutes on complex tasks before writing the next
			// JSONL entry. Only slash commands (3) use the staleness
			// fallback since they can legitimately leave the session idle
			// with no assistant response.
			if isUserToolResult(msg.Message.Content) || !isUserSlashCommand(msg.Message.Content) {
				return false
			}
			return time.Since(info.ModTime()) > 120*time.Second
		default:
			continue
		}
	}

	// Fallback: if JSONL hasn't been written to in >120s, the session is
	// likely stuck on a permission prompt or similar (no assistant message).
	return time.Since(info.ModTime()) > 120*time.Second
}

// isUserToolResult reports whether a user message's content contains a
// tool_result entry (meaning Claude is mid-turn processing tool output).
func isUserToolResult(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	// Content is either a JSON array of objects or a plain string.
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &items) == nil {
		for _, item := range items {
			if item.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// isUserSlashCommand reports whether a user message's content looks like a
// slash command (/compact, /review, etc.). These write synthetic user entries
// with content starting with <local-command-*> or <command-*> and may have
// no assistant follow-up.
func isUserSlashCommand(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	// Content can be a plain string or an array with text items.
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.HasPrefix(s, "<local-command") || strings.HasPrefix(s, "<command")
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &items) == nil {
		for _, item := range items {
			if item.Type == "text" {
				return strings.HasPrefix(item.Text, "<local-command") || strings.HasPrefix(item.Text, "<command")
			}
		}
	}
	return false
}

// RecentSession represents a historical Claude Code session from the JSONL files.
type RecentSession struct {
	SessionID  string
	Title      string // descriptive name from Claude (e.g. "unky-mo-session-orchestrator")
	Summary    string // first user message, truncated
	GitBranch  string
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
