package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// historyLine is one history.jsonl line as Claude Code 2.1.325 writes it.
func historyLine(display, project, session string, pastes string) string {
	if pastes == "" {
		pastes = "{}"
	}
	return fmt.Sprintf(`{"display":%q,"pastedContents":%s,"timestamp":1,"project":%q,"sessionId":%q}`, display, pastes, project, session)
}

func writeHistory(t *testing.T, lines ...string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPromptHistoryOrder(t *testing.T) {
	f := writeHistory(t,
		historyLine("old other", "/ws/foo", "s1", ""),
		historyLine("own first", "/ws/foo", "s2", ""),
		historyLine("elsewhere", "/ws/bar", "s2", ""),
		historyLine("again", "/ws/foo", "s1", ""),
		historyLine("own second", "/ws/foo", "s2", ""),
		historyLine("again", "/ws/foo", "s2", ""),
		`not json`,
		historyLine("", "/ws/foo", "s2", ""),
		historyLine("newest other", "/ws/foo", "s3", ""),
	)
	got := PromptHistory(f, "/ws/foo", "s2")
	// The session's own prompts first, newest first, then the project's;
	// a repeat is kept at its newest place; another project never appears.
	want := []string{"again", "own second", "own first", "newest other", "old other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := PromptHistory(f, "/ws/foo", ""); !reflect.DeepEqual(got, []string{"newest other", "again", "own second", "own first", "old other"}) {
		t.Errorf("no session: got %q", got)
	}
}

func TestPromptHistoryPastes(t *testing.T) {
	f := writeHistory(t,
		historyLine("see:\n[Pasted text #1 +2 lines] and [Pasted text #2]", "/ws/foo", "s",
			`{"1":{"id":1,"type":"text","content":"a\nb\nc"},"2":{"id":2,"type":"text","contentHash":"6e6e6ad70bf98a67"}}`),
		historyLine("[Pasted text #3] [Image #1]", "/ws/foo", "s", `{"1":{"id":1,"type":"image"}}`),
	)
	got := PromptHistory(f, "/ws/foo", "s")
	want := []string{"[Pasted text #3] [Image #1]", "see:\na\nb\nc and [Pasted text #2]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPromptHistoryCapAndTail(t *testing.T) {
	var lines []string
	// Padding pushes the start of the file past the tail that's read.
	pad := strings.Repeat("x", 1000)
	for range promptHistoryTail/1000 + 10 {
		lines = append(lines, historyLine(pad, "/ws/other", "s", ""))
	}
	lines = append(lines, historyLine("first", "/ws/foo", "s", ""))
	for i := range PromptHistoryMax + 20 {
		lines = append(lines, historyLine(fmt.Sprintf("p%d", i), "/ws/foo", "s", ""))
	}
	got := PromptHistory(writeHistory(t, lines...), "/ws/foo", "s")
	if len(got) != PromptHistoryMax || got[0] != fmt.Sprintf("p%d", PromptHistoryMax+19) {
		t.Errorf("got %d prompts starting %q", len(got), got[0])
	}
}

func TestPromptHistoryMissingFile(t *testing.T) {
	if got := PromptHistory(filepath.Join(t.TempDir(), "nope"), "/ws/foo", "s"); got != nil {
		t.Errorf("got %q", got)
	}
}
