package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func TestHandleTranscript404WhenWindowUnknown(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{}, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/transcript/@1", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestHandleTranscriptEmitsBacklogThenLiveMessage(t *testing.T) {
	// resolveSession resolves the JSONL path via the real
	// claude.ProjectsDirForPath(Path), so the fixture must live where that
	// function actually points — a uniquely-named (t.TempDir()-derived)
	// subdirectory under ~/.claude/projects, cleaned up afterward.
	projectPath := t.TempDir()
	realDir := claude.ProjectsDirForPath(projectPath)
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(realDir) })

	jsonlPath := writeFile(t, realDir, "sessABC.jsonl", `{"type":"user","uuid":"u1","message":{"content":"hi"}}`+"\n")

	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "sessABC", Path: projectPath, Status: "idle"}},
	}, nil).AnyTimes()

	srv := NewServer(Deps{State: mockState}, 0, "test")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/transcript/@1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	deadline := time.Now().Add(2 * time.Second)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, line)
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"uuid":"u1"`) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if len(events) == 0 {
		t.Fatalf("expected at least one transcript event, fixture at %s", jsonlPath)
	}
	if events[0] != "event: transcript" {
		t.Errorf("expected event name %q, got %q", "event: transcript", events[0])
	}

	// Append a new line and confirm it's live-tailed too.
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"hey"}]}}` + "\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	found := false
	deadline = time.Now().Add(2 * time.Second)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"uuid":"a1"`) {
			found = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if !found {
		t.Fatalf("expected the live-appended message to arrive over SSE")
	}
}
