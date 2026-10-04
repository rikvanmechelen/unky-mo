package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// subagentsFixture: window @5 runs session s5 in /ws/foo; window @6 has no
// session. List is set up per test.
func subagentsFixture(t *testing.T) (*Server, *mock_web.MockSubagents) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{
		{Name: "foo", WindowID: "@5", SessionID: "s5", Path: "/ws/foo", Status: "idle"},
		{Name: "bar", WindowID: "@6", Path: "/ws/bar", Status: "none"},
	}}, nil).AnyTimes()
	sa := mock_web.NewMockSubagents(ctrl)
	return NewServer(Deps{State: st, Subagents: sa}, 0, "test"), sa
}

func TestSubagentsList(t *testing.T) {
	srv, sa := subagentsFixture(t)
	started := time.Date(2026, 10, 4, 0, 7, 30, 0, time.UTC)
	// Two polls inside the cache TTL share one listing.
	sa.EXPECT().List("/ws/foo", "s5").Return([]claude.Subagent{{
		ID: "abc", AgentType: "Explore", Description: "Map chat view", ToolUseID: "toolu_1",
		Background: true, State: "running", Running: true, StartedAt: started, LastActivity: started.Add(time.Minute),
		ToolUses: 4, LastTool: "Read", LastToolDetail: "/ws/foo/internal/web/chat.js",
		TranscriptPath: "/secret/agent-abc.jsonl",
	}}, nil).Times(1)

	for range 2 {
		rec := get(t, srv, "/api/sessions/@5/subagents")
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		var got []subagentView
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := subagentView{
			ID: "abc", Type: "Explore", Description: "Map chat view", ToolUseID: "toolu_1",
			Background: true, State: "running", Running: true, Started: "2026-10-04T00:07:30Z", LastActivity: "2026-10-04T00:08:30Z",
			ToolUses: 4, LastTool: "Read", LastDetail: "internal/web/chat.js",
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("got %+v", got)
		}
		if strings.Contains(rec.Body.String(), "/secret/") {
			t.Errorf("transcript path leaked: %s", rec.Body)
		}
	}
}

func TestSubagentsNoSession(t *testing.T) {
	srv, _ := subagentsFixture(t) // no List expectation: gomock fails on a call
	for _, path := range []string{"/api/sessions/@6/subagents", "/api/sessions/@6/subagents/abc/transcript", "/api/sessions/@9/subagents"} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

func TestSubagentTranscriptUnknownAgent(t *testing.T) {
	srv, sa := subagentsFixture(t)
	sa.EXPECT().List("/ws/foo", "s5").Return([]claude.Subagent{{ID: "abc", TranscriptPath: "/nope/agent-abc.jsonl"}}, nil)
	if rec := get(t, srv, "/api/sessions/@5/subagents/..%2Fs5/transcript"); rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}

func TestSubagentTranscriptStreamsListedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "agent-abc.jsonl", `{"type":"user","uuid":"u1","message":{"content":"research"}}`+"\n")
	srv, sa := subagentsFixture(t)
	sa.EXPECT().List("/ws/foo", "s5").Return([]claude.Subagent{{ID: "abc", TranscriptPath: path}}, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/@5/subagents/abc/transcript")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			if !strings.Contains(line, `"uuid":"u1"`) {
				t.Errorf("unexpected event %q", line)
			}
			return
		}
	}
	t.Fatal("no event received")
}

// ?once=1 answers with the listed agent's lines as one JSON array.
func TestSubagentTranscriptOnce(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "agent-abc.jsonl", `{"type":"user","uuid":"u1","message":{"content":"research"}}`+"\n"+`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":[]}}`+"\n")
	srv, sa := subagentsFixture(t)
	sa.EXPECT().List("/ws/foo", "s5").Return([]claude.Subagent{{ID: "abc", TranscriptPath: path}}, nil).AnyTimes()

	rec := get(t, srv, "/api/sessions/@5/subagents/abc/transcript?once=1")
	var lines []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil || rec.Code != http.StatusOK || len(lines) != 2 || lines[1]["uuid"] != "a1" {
		t.Fatalf("got %d %s (%v)", rec.Code, rec.Body, err)
	}
	// Still only for an agent the listing returned.
	if rec := get(t, srv, "/api/sessions/@5/subagents/zzz/transcript?once=1"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent: want 404, got %d", rec.Code)
	}
}
