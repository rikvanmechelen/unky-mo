package status

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestJSONL(t *testing.T, dir string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadJSONLStatus_EndTurn_ReturnsIdle(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"done","stop_reason":"end_turn"}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusIdle {
		t.Errorf("end_turn: got %v, want StatusIdle", got)
	}
}

func TestReadJSONLStatus_ToolUse_ReturnsActive(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"tool","stop_reason":"tool_use"}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusActive {
		t.Errorf("tool_use: got %v, want StatusActive", got)
	}
}

func TestReadJSONLStatus_UserPrompt_ReturnsActive(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"assistant","message":{"role":"assistant","content":"done","stop_reason":"end_turn"}}`,
		`{"type":"user","message":{"role":"user","content":"please refactor the auth module"}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusActive {
		t.Errorf("user prompt: got %v, want StatusActive", got)
	}
}

func TestReadJSONLStatus_ToolResult_ReturnsActive(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}],"stop_reason":"tool_use"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusActive {
		t.Errorf("tool_result: got %v, want StatusActive", got)
	}
}

func TestReadJSONLStatus_SlashCommand_ReturnsIdle(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"user","message":{"role":"user","content":"<local-command-stdout>result</local-command-stdout>"}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusIdle {
		t.Errorf("slash command: got %v, want StatusIdle", got)
	}
}

func TestReadJSONLStatus_SlashCommandArray_ReturnsIdle(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<command-stdout>result</command-stdout>"}]}}`,
	})
	if got := ReadJSONLStatus(path); got != StatusIdle {
		t.Errorf("slash command (array): got %v, want StatusIdle", got)
	}
}

func TestReadJSONLStatus_EmptyFile_ReturnsNone(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{})
	if got := ReadJSONLStatus(path); got != StatusNone {
		t.Errorf("empty file: got %v, want StatusNone", got)
	}
}

func TestReadJSONLStatus_MissingFile_ReturnsNone(t *testing.T) {
	if got := ReadJSONLStatus("/nonexistent/path.jsonl"); got != StatusNone {
		t.Errorf("missing file: got %v, want StatusNone", got)
	}
}

func TestReadJSONLStatus_OnlyMetadata_ReturnsNone(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"file-history-snapshot"}`,
		`{"type":"attachment"}`,
		`{"type":"permission-mode"}`,
	})
	if got := ReadJSONLStatus(path); got != StatusNone {
		t.Errorf("metadata-only: got %v, want StatusNone", got)
	}
}

func TestReadJSONLStatus_SkipsMetadataToFindAssistant(t *testing.T) {
	path := writeTestJSONL(t, t.TempDir(), []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"done","stop_reason":"end_turn"}}`,
		`{"type":"file-history-snapshot"}`,
		`{"type":"custom-title","customTitle":"test"}`,
	})
	if got := ReadJSONLStatus(path); got != StatusIdle {
		t.Errorf("metadata after end_turn: got %v, want StatusIdle", got)
	}
}
