package status

import (
	"encoding/json"
	"testing"
)

const (
	askLine    = `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","options":[{"label":"A"},{"label":"B"}],"multiSelect":true}]}}]}}`
	promptLine = `{"type":"user","message":{"role":"user","content":"go"}}`
)

func TestReadPendingQuestion(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		// What Claude Code 2.1.289 leaves while the dialog is open.
		{"open, metadata after", []string{promptLine, askLine,
			`{"type":"last-prompt"}`, `{"type":"ai-title"}`, `{"type":"mode","mode":"normal"}`,
			`{"type":"permission-mode","permissionMode":"plan"}`, `{"type":"atis-latch","atis":""}`}, true},
		{"answered", []string{promptLine, askLine,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`}, false},
		{"answered, then another tool runs", []string{promptLine, askLine,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"ls"}}]}}`}, false},
		{"split message: question then a parallel call", []string{promptLine, askLine,
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"x"}]}}`}, true},
		{"an earlier turn's question", []string{askLine, promptLine,
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]}}`}, false},
		{"turn ended", []string{askLine, `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"done"}}`}, false},
		{"no question", []string{promptLine}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool, input, ok := ReadPendingQuestion(writeTestJSONL(t, t.TempDir(), c.lines))
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if !ok {
				return
			}
			var in struct{ Questions []struct{ Question string } }
			if tool != "AskUserQuestion" || json.Unmarshal(input, &in) != nil || len(in.Questions) != 1 || in.Questions[0].Question != "Q?" {
				t.Fatalf("got %s %s", tool, input)
			}
		})
	}
	if _, _, ok := ReadPendingQuestion(t.TempDir() + "/missing.jsonl"); ok {
		t.Fatal("missing file read as a question")
	}
}

func TestRecoverPendingQuestion(t *testing.T) {
	m := NewManager()
	reads := 0
	read := func() (string, json.RawMessage, bool) {
		reads++
		return "AskUserQuestion", json.RawMessage(`{"questions":[]}`), true
	}

	// Not a question: nothing is read.
	m.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s"})
	if _, _, ok := m.RecoverPendingQuestion("s", read); ok || reads != 0 {
		t.Fatalf("ok %v reads %d", ok, reads)
	}

	// The agents signal's content-less question is filled in, once.
	m.ProcessAgentStatus("s", "waiting", "input needed", m.sessions["s"].LastHookAt.Add(1))
	for range 2 {
		tool, input, ok := m.RecoverPendingQuestion("s", read)
		if !ok || tool != "AskUserQuestion" || string(input) != `{"questions":[]}` {
			t.Fatalf("got %q %s %v", tool, input, ok)
		}
	}
	if reads != 1 {
		t.Fatalf("read %d times, want 1", reads)
	}

	// A hook-captured question is never replaced.
	m.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "h", ToolName: "AskUserQuestion", ToolInput: json.RawMessage(`{"hook":1}`)})
	if _, input, _ := m.RecoverPendingQuestion("h", read); string(input) != `{"hook":1}` || reads != 1 {
		t.Fatalf("input %s reads %d", input, reads)
	}
}
