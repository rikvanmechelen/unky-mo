package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// subagentFixture points $HOME at a temp dir and returns the project path
// plus its session's subagents dir (created) and parent transcript path.
func subagentFixture(t *testing.T) (project, dir, parent string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	project = "/ws/proj"
	dir = SubagentsDir(project, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return project, dir, filepath.Join(ProjectsDirForPath(project), "s1.jsonl")
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

const (
	subPrompt  = `{"type":"user","timestamp":"2026-10-04T00:07:30Z","uuid":"u1","parentUuid":null,"message":{"role":"user","content":"research"}}` + "\n"
	subFetch   = `{"type":"assistant","timestamp":"2026-10-04T00:08:00Z","uuid":"a1","parentUuid":"u1","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://example.com","prompt":"x"}}]}}` + "\n"
	subResult  = `{"type":"user","timestamp":"2026-10-04T00:08:05Z","uuid":"u2","parentUuid":"a1","message":{"content":[{"type":"tool_result","content":"ok"}]}}` + "\n"
	subHandoff = `{"type":"user","timestamp":"2026-10-04T00:13:04Z","uuid":"u3","parentUuid":"a2","toolEndsTurn":true,"message":{"content":[{"type":"tool_result","content":"delivered"}]}}` + "\n"
	subEndTurn = `{"type":"assistant","timestamp":"2026-10-04T00:09:00Z","uuid":"a3","parentUuid":"u2","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}` + "\n"
)

func TestSubagentsNoneWhenDirMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := NewSubagentReader().List("/ws/none", "s1")
	if err != nil || got != nil {
		t.Fatalf("want nil, nil; got %v, %v", got, err)
	}
}

func TestSubagentsRunningThenHandback(t *testing.T) {
	project, dir, _ := subagentFixture(t)
	path := filepath.Join(dir, "agent-abc.jsonl")
	appendFile(t, filepath.Join(dir, "agent-abc.meta.json"),
		`{"agentType":"general-purpose","description":"Research editors","toolUseId":"toolu_1","requestShape":"background"}`)
	appendFile(t, path, subPrompt+subFetch+subResult)

	r := NewSubagentReader()
	got, err := r.List(project, "s1")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	a := got[0]
	if a.ID != "abc" || a.AgentType != "general-purpose" || a.Description != "Research editors" ||
		a.ToolUseID != "toolu_1" || !a.Background || a.TranscriptPath != path {
		t.Errorf("identity: %+v", a)
	}
	if !a.Running || a.ToolUses != 1 || a.LastTool != "WebFetch" || a.LastToolDetail != "https://example.com" {
		t.Errorf("progress: %+v", a)
	}
	if a.StartedAt.Format("15:04:05") != "00:07:30" || a.LastActivity.Format("15:04:05") != "00:08:05" {
		t.Errorf("times: %v → %v", a.StartedAt, a.LastActivity)
	}

	// The hand-back tool call ends the agent's turn; only the new line is
	// read, but the counts carry over.
	appendFile(t, path, subHandoff)
	got, _ = r.List(project, "s1")
	if got[0].Running || got[0].ToolUses != 1 {
		t.Errorf("after hand-back: %+v", got[0])
	}
}

func TestSubagentsEndTurnFinishesAndResumeRestarts(t *testing.T) {
	project, dir, _ := subagentFixture(t)
	path := filepath.Join(dir, "agent-x.jsonl")
	appendFile(t, path, subPrompt+subFetch+subResult+subEndTurn)

	r := NewSubagentReader()
	got, _ := r.List(project, "s1")
	if got[0].Running {
		t.Fatalf("end_turn should finish it: %+v", got[0])
	}
	// No meta file: still listed, just without a description.
	if got[0].Description != "" || got[0].Background {
		t.Errorf("meta: %+v", got[0])
	}

	// SendMessage to a finished agent resumes it.
	appendFile(t, path, `{"type":"user","timestamp":"2026-10-04T00:20:00Z","uuid":"u9","parentUuid":"a3","message":{"content":"more"}}`+"\n")
	got, _ = r.List(project, "s1")
	if !got[0].Running {
		t.Errorf("resumed agent should be running: %+v", got[0])
	}
}

func TestSubagentsTaskNotificationStopsAgent(t *testing.T) {
	project, dir, parent := subagentFixture(t)
	appendFile(t, filepath.Join(dir, "agent-k.jsonl"), subPrompt+subFetch)
	// An unrelated, partial line in the parent must not break anything.
	appendFile(t, parent, `{"type":"user","timestamp":"2026-10-04T00:07:00Z","message":{"content":"hi"}}`+"\n")

	r := NewSubagentReader()
	if got, _ := r.List(project, "s1"); !got[0].Running {
		t.Fatalf("want running before the notification: %+v", got[0])
	}

	// Stopped mid-turn: the transcript never ends a turn, but the parent is
	// told it stopped.
	appendFile(t, parent, `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-04T00:08:30Z","content":"<task-notification>\n<task-id>k</task-id>\n<status>killed</status>\n</task-notification>"}`+"\n")
	if got, _ := r.List(project, "s1"); got[0].Running || got[0].State != "killed" {
		t.Errorf("notified agent should be killed: %+v", got[0])
	}

	// Activity after the notification means it was resumed.
	appendFile(t, filepath.Join(dir, "agent-k.jsonl"), `{"type":"user","timestamp":"2026-10-04T00:09:00Z","uuid":"u5","message":{"content":"again"}}`+"\n")
	if got, _ := r.List(project, "s1"); !got[0].Running {
		t.Errorf("resumed agent should be running: %+v", got[0])
	}
}

func TestSubagentsPartialLineAndOrder(t *testing.T) {
	project, dir, _ := subagentFixture(t)
	later := filepath.Join(dir, "agent-b.jsonl")
	appendFile(t, later, `{"type":"user","timestamp":"2026-10-04T01:00:00Z","uuid":"z","message":{"content":"x"}}`+"\n")
	earlier := filepath.Join(dir, "agent-a.jsonl")
	// The tool call is mid-write: no newline yet.
	appendFile(t, earlier, subPrompt+subFetch[:40])

	r := NewSubagentReader()
	got, _ := r.List(project, "s1")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("want oldest first: %+v", got)
	}
	if got[0].ToolUses != 0 {
		t.Errorf("partial line counted: %+v", got[0])
	}
	appendFile(t, earlier, subFetch[40:])
	got, _ = r.List(project, "s1")
	if got[0].ToolUses != 1 || got[0].LastTool != "WebFetch" {
		t.Errorf("completed line not counted: %+v", got[0])
	}
}

func TestToolDetail(t *testing.T) {
	r := NewSubagentReader()
	project, dir, _ := subagentFixture(t)
	appendFile(t, filepath.Join(dir, "agent-d.jsonl"),
		`{"type":"assistant","timestamp":"2026-10-04T00:08:00Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"  go test ./...\necho done"}}]}}`+"\n")
	got, _ := r.List(project, "s1")
	if got[0].LastToolDetail != "go test ./..." {
		t.Errorf("detail: %q", got[0].LastToolDetail)
	}
}

func TestSubagentsWaitingOnOwnBackgroundWork(t *testing.T) {
	project, dir, parent := subagentFixture(t)
	path := filepath.Join(dir, "agent-w.jsonl")
	// Told to hand back its report, the agent starts a background shell and
	// ends its turn without reporting.
	appendFile(t, path, subPrompt+
		`{"type":"user","timestamp":"2026-10-04T00:07:31Z","uuid":"r1","message":{"content":"<system-reminder>Your final report is delivered through SubagentHandback</system-reminder>"}}`+"\n"+
		subFetch+subResult+subEndTurn)
	// The parent is told it stopped ("completed"), with the report pending.
	appendFile(t, parent, `{"type":"queue-operation","timestamp":"2026-10-04T00:09:01Z","content":"<task-notification>\n<task-id>w</task-id>\n<status>completed</status>\n</task-notification>"}`+"\n")

	r := NewSubagentReader()
	got, _ := r.List(project, "s1")
	if got[0].State != SubagentWaiting || !got[0].Running {
		t.Fatalf("want waiting: %+v", got[0])
	}

	// Its shell reports, it resumes and hands back.
	appendFile(t, path, `{"type":"user","timestamp":"2026-10-04T00:10:00Z","uuid":"u7","message":{"content":"shell done"}}`+"\n")
	if got, _ = r.List(project, "s1"); got[0].State != SubagentRunning {
		t.Errorf("want running again: %+v", got[0])
	}
	appendFile(t, path, subHandoff)
	if got, _ = r.List(project, "s1"); got[0].State != SubagentDone || got[0].Running {
		t.Errorf("want done: %+v", got[0])
	}
}
