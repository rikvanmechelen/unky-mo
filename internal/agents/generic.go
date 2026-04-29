package agents

import "time"

// GenericReader is a fallback SessionReader for agents that don't have
// dedicated session detection. It provides no idle detection, no history,
// and no shell tracking — sessions are detected purely by PID liveness
// via the tmux pane walk in the main polling loop.
type GenericReader struct {
	AgentKey string
}

func (g *GenericReader) LiveSessions() ([]Session, error) { return nil, nil }
func (g *GenericReader) SessionForPath(string) *Session    { return nil }
func (g *GenericReader) SessionsForPath(string) []Session  { return nil }
func (g *GenericReader) IsAlive(pid int) bool              { return false }
func (g *GenericReader) IsDescendantOf(int, map[int]bool) bool { return false }
func (g *GenericReader) IsSessionIdle(string, string) bool { return false }
func (g *GenericReader) CustomTitleFor(string, string) string { return "" }
func (g *GenericReader) LastMessages(string, string, int) []SessionMessage { return nil }
func (g *GenericReader) RecentSessions(string, int) []RecentSession { return nil }
func (g *GenericReader) ProjectsDirForPath(string) string { return "" }
func (g *GenericReader) ActiveShellsForSession(string) []ActiveShell { return nil }
func (g *GenericReader) FormatShellCommand(cmd string, maxLen int) string {
	if len(cmd) > maxLen {
		return cmd[:maxLen-1] + "…"
	}
	return cmd
}

var _ SessionReader = (*GenericReader)(nil) // compile-time check

// MultiReader aggregates session data from multiple agent-specific readers.
// The main TUI uses this as its single SessionReader, delegating to the
// appropriate reader per agent.
type MultiReader struct {
	Primary SessionReader            // Claude reader (used for backward-compat methods)
	Readers map[string]SessionReader // agent key → reader
}

// NewMultiReader builds a MultiReader with a primary (Claude) reader and
// optional additional readers for other agents.
func NewMultiReader(primary SessionReader, extras map[string]SessionReader) *MultiReader {
	return &MultiReader{Primary: primary, Readers: extras}
}

// Backward-compat: delegate most methods to the primary (Claude) reader since
// the existing codebase calls these on the single ClaudeReader. As the
// refactor progresses, callers will switch to agent-aware queries.

func (m *MultiReader) LiveSessions() ([]Session, error) {
	if m.Primary != nil {
		return m.Primary.LiveSessions()
	}
	return nil, nil
}

func (m *MultiReader) SessionForPath(path string) *Session {
	if m.Primary != nil {
		return m.Primary.SessionForPath(path)
	}
	return nil
}

func (m *MultiReader) SessionsForPath(path string) []Session {
	if m.Primary != nil {
		return m.Primary.SessionsForPath(path)
	}
	return nil
}

func (m *MultiReader) IsAlive(pid int) bool {
	if m.Primary != nil {
		return m.Primary.IsAlive(pid)
	}
	return false
}

func (m *MultiReader) IsDescendantOf(pid int, hostPIDs map[int]bool) bool {
	if m.Primary != nil {
		return m.Primary.IsDescendantOf(pid, hostPIDs)
	}
	return false
}

func (m *MultiReader) IsSessionIdle(projectPath, sessionID string) bool {
	if m.Primary != nil {
		return m.Primary.IsSessionIdle(projectPath, sessionID)
	}
	return false
}

func (m *MultiReader) CustomTitleFor(projectPath, sessionID string) string {
	if m.Primary != nil {
		return m.Primary.CustomTitleFor(projectPath, sessionID)
	}
	return ""
}

func (m *MultiReader) LastMessages(projectPath, sessionID string, count int) []SessionMessage {
	if m.Primary != nil {
		return m.Primary.LastMessages(projectPath, sessionID, count)
	}
	return nil
}

func (m *MultiReader) RecentSessions(projectPath string, maxResults int) []RecentSession {
	if m.Primary != nil {
		return m.Primary.RecentSessions(projectPath, maxResults)
	}
	return nil
}

func (m *MultiReader) ProjectsDirForPath(projectPath string) string {
	if m.Primary != nil {
		return m.Primary.ProjectsDirForPath(projectPath)
	}
	return ""
}

func (m *MultiReader) ActiveShellsForSession(projectPath string) []ActiveShell {
	if m.Primary != nil {
		return m.Primary.ActiveShellsForSession(projectPath)
	}
	return nil
}

func (m *MultiReader) FormatShellCommand(cmd string, maxLen int) string {
	if m.Primary != nil {
		return m.Primary.FormatShellCommand(cmd, maxLen)
	}
	if len(cmd) > maxLen {
		return cmd[:maxLen-1] + "…"
	}
	return cmd
}

var _ SessionReader = (*MultiReader)(nil)

// ReaderForAgent returns the reader for a specific agent key, falling back
// to the primary when no dedicated reader is registered.
func (m *MultiReader) ReaderForAgent(agentKey string) SessionReader {
	if r, ok := m.Readers[agentKey]; ok {
		return r
	}
	if m.Primary != nil {
		return m.Primary
	}
	return &GenericReader{AgentKey: agentKey}
}

// AllLiveSessions aggregates sessions from all registered readers.
func (m *MultiReader) AllLiveSessions() ([]Session, error) {
	var all []Session
	if m.Primary != nil {
		if sessions, err := m.Primary.LiveSessions(); err == nil {
			all = append(all, sessions...)
		}
	}
	for _, r := range m.Readers {
		if sessions, err := r.LiveSessions(); err == nil {
			all = append(all, sessions...)
		}
	}
	return all, nil
}

func init() {
	_ = time.Now // prevent unused import
}
