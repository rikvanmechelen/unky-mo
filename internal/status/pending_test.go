package status

import (
	"encoding/json"
	"testing"
	"time"
)

const (
	askLine    = `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","options":[{"label":"A"},{"label":"B"}],"multiSelect":true}]}}]}}`
	promptLine = `{"type":"user","message":{"role":"user","content":"go"}}`
)

func TestReadPendingToolQuestion(t *testing.T) {
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
			tool, input, ok := ReadPendingTool(writeTestJSONL(t, t.TempDir(), c.lines), true)
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
	if _, _, ok := ReadPendingTool(t.TempDir()+"/missing.jsonl", false); ok {
		t.Fatal("missing file read as a question")
	}
}

// A permission prompt's call is any tool's; ExitPlanMode's is only written
// once answered (step 1 of docs/plans/permission-answers.md), so its
// transcript has nothing open.
func TestReadPendingToolPermission(t *testing.T) {
	bash := func(id, cmd string) string {
		return `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"` + cmd + `"}}]}}`
	}
	result := func(id string) string {
		return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"x"}]}}`
	}
	cases := []struct {
		name     string
		lines    []string
		wantTool string
		wantCmd  string
	}{
		{"open Bash call, metadata after", []string{promptLine, bash("b1", "echo probe"),
			`{"type":"permission-mode","permissionMode":"default"}`, `{"type":"ai-title"}`}, "Bash", "echo probe"},
		{"answered", []string{promptLine, bash("b1", "ls"), result("b1")}, "", ""},
		{"two parallel calls: the newest", []string{promptLine, bash("b1", "first"), bash("b2", "second")}, "Bash", "second"},
		{"parallel, the newer one answered", []string{promptLine, bash("b1", "first"), bash("b2", "second"), result("b2")}, "Bash", "first"},
		{"ExitPlanMode open: only the answered ToolSearch is on disk", []string{promptLine,
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/p.md"}},{"type":"tool_use","id":"s1","name":"ToolSearch","input":{"query":"select:ExitPlanMode"}}]}}`,
			result("w1"), result("s1"), `{"type":"attachment"}`}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTestJSONL(t, t.TempDir(), c.lines)
			tool, input, ok := ReadPendingTool(path, false)
			if ok != (c.wantTool != "") || tool != c.wantTool {
				t.Fatalf("got %q %v, want %q", tool, ok, c.wantTool)
			}
			if !ok {
				return
			}
			var in struct{ Command string }
			if json.Unmarshal(input, &in) != nil || in.Command != c.wantCmd {
				t.Fatalf("input %s, want command %q", input, c.wantCmd)
			}
			// A question only ever looks for interactive tools.
			if _, _, ok := ReadPendingTool(path, true); ok {
				t.Fatal("interactiveOnly found a Bash call")
			}
		})
	}
}

func TestRecoverPending(t *testing.T) {
	m := NewManager()
	reads := 0
	read := func(st SessionStatus) (string, json.RawMessage, bool) {
		if st != StatusQuestion {
			t.Fatalf("read for %v, want StatusQuestion", st)
		}
		reads++
		return "AskUserQuestion", json.RawMessage(`{"questions":[]}`), true
	}

	// Not a question: nothing is read.
	m.ProcessHookEvent(HookEvent{Type: EventStop, SessionID: "s"})
	if _, _, ok := m.RecoverPending("s", read); ok || reads != 0 {
		t.Fatalf("ok %v reads %d", ok, reads)
	}

	// The agents signal's content-less question is filled in, once.
	m.ProcessAgentStatus("s", "waiting", "input needed", m.sessions["s"].LastHookAt.Add(1))
	for range 2 {
		tool, input, ok := m.RecoverPending("s", read)
		if !ok || tool != "AskUserQuestion" || string(input) != `{"questions":[]}` {
			t.Fatalf("got %q %s %v", tool, input, ok)
		}
	}
	if reads != 1 {
		t.Fatalf("read %d times, want 1", reads)
	}

	// A hook-captured question is never replaced.
	m.ProcessHookEvent(HookEvent{Type: EventPreToolUse, SessionID: "h", ToolName: "AskUserQuestion", ToolInput: json.RawMessage(`{"hook":1}`)})
	if _, input, _ := m.RecoverPending("h", read); string(input) != `{"hook":1}` || reads != 1 {
		t.Fatalf("input %s reads %d", input, reads)
	}
}

func TestRecoverPendingPermission(t *testing.T) {
	m := NewManager()
	var got SessionStatus
	read := func(st SessionStatus) (string, json.RawMessage, bool) {
		got = st
		return "Bash", json.RawMessage(`{"command":"ls"}`), true
	}
	m.ProcessAgentStatus("s", "waiting", "permission prompt", time.Now())
	tool, input, ok := m.RecoverPending("s", read)
	if !ok || got != StatusPermission || tool != "Bash" || string(input) != `{"command":"ls"}` {
		t.Fatalf("got %q %s %v (read for %v)", tool, input, ok, got)
	}
}
