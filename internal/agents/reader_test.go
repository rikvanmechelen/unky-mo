package agents

import "testing"

func TestGenericReaderImplementsInterface(t *testing.T) {
	var r SessionReader = &GenericReader{AgentKey: "g"}
	sessions, err := r.LiveSessions()
	if err != nil || len(sessions) != 0 {
		t.Errorf("generic: want nil/empty, got %v, %v", sessions, err)
	}
	if r.IsSessionIdle("/x", "id") {
		t.Error("generic should never report idle")
	}
	if r.SessionForPath("/x") != nil {
		t.Error("generic should return nil session")
	}
}

func TestGenericReaderFormatShellCommand(t *testing.T) {
	r := &GenericReader{}
	if got := r.FormatShellCommand("short", 20); got != "short" {
		t.Errorf("short: %q", got)
	}
	got := r.FormatShellCommand("very long command string here", 10)
	if len([]rune(got)) > 10 {
		t.Errorf("truncated too long: %q (runes=%d)", got, len([]rune(got)))
	}
}

func TestMultiReaderDelegatesToPrimary(t *testing.T) {
	primary := &fakeReader{liveSessions: []Session{{SessionID: "s1", AgentKey: "c"}}}
	mr := NewMultiReader(primary, nil)

	sessions, _ := mr.LiveSessions()
	if len(sessions) != 1 || sessions[0].SessionID != "s1" {
		t.Errorf("want primary session, got %v", sessions)
	}
}

func TestMultiReaderReaderForAgent(t *testing.T) {
	primary := &fakeReader{}
	gemini := &fakeReader{liveSessions: []Session{{SessionID: "g1", AgentKey: "g"}}}
	mr := NewMultiReader(primary, map[string]SessionReader{"g": gemini})

	r := mr.ReaderForAgent("g")
	sessions, _ := r.LiveSessions()
	if len(sessions) != 1 || sessions[0].AgentKey != "g" {
		t.Errorf("want gemini sessions, got %v", sessions)
	}

	// Unknown key falls back to primary.
	r2 := mr.ReaderForAgent("x")
	if r2 != primary {
		t.Error("unknown key should fall back to primary")
	}
}

func TestMultiReaderAllLiveSessions(t *testing.T) {
	primary := &fakeReader{liveSessions: []Session{{SessionID: "c1"}}}
	gemini := &fakeReader{liveSessions: []Session{{SessionID: "g1"}, {SessionID: "g2"}}}
	mr := NewMultiReader(primary, map[string]SessionReader{"g": gemini})

	all, _ := mr.AllLiveSessions()
	if len(all) != 3 {
		t.Errorf("want 3 total sessions, got %d", len(all))
	}
}

// fakeReader is a minimal SessionReader for tests.
type fakeReader struct {
	liveSessions []Session
}

func (f *fakeReader) LiveSessions() ([]Session, error)            { return f.liveSessions, nil }
func (f *fakeReader) SessionForPath(string) *Session               { return nil }
func (f *fakeReader) SessionsForPath(string) []Session             { return nil }
func (f *fakeReader) IsAlive(int) bool                             { return false }
func (f *fakeReader) IsDescendantOf(int, map[int]bool) bool        { return false }
func (f *fakeReader) IsSessionIdle(string, string) bool            { return false }
func (f *fakeReader) CustomTitleFor(string, string) string         { return "" }
func (f *fakeReader) LastMessages(string, string, int) []SessionMessage { return nil }
func (f *fakeReader) RecentSessions(string, int) []RecentSession   { return nil }
func (f *fakeReader) ProjectsDirForPath(string) string             { return "" }
func (f *fakeReader) ActiveShellsForSession(string) []ActiveShell  { return nil }
func (f *fakeReader) FormatShellCommand(cmd string, _ int) string  { return cmd }
