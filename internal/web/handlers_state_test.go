package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"

	"github.com/rvanmech/unky-mo/internal/state"
)

func TestHandleState(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := &state.StateFile{
		TmuxSession: "mo",
		Projects:    []state.ProjectState{{Name: "myproj", Status: "active"}},
	}

	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(st, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var got state.StateFile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Name != "myproj" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestHandleStateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(nil, errors.New("boom"))

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
}

func TestHandleUsage(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := &state.StateFile{
		Usage: &state.UsageState{FiveHourPct: 42, FetchedAt: time.Now()},
	}
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(st, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))

	var got state.UsageState
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FiveHourPct != 42 {
		t.Fatalf("want FiveHourPct 42, got %d", got.FiveHourPct)
	}
}

func TestHandleStateFillsTokens(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := &state.StateFile{Projects: []state.ProjectState{
		{Name: "claude", Path: "/ws/a", WindowID: "@1", SessionID: "s1", AgentKey: "c"},
		{Name: "default", Path: "/ws/b", WindowID: "@2", SessionID: "s2"},
		{Name: "other-agent", Path: "/ws/c", WindowID: "@3", SessionID: "s3", AgentKey: "x"},
		{Name: "ended", Path: "/ws/d", WindowID: "@4"},
	}}
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(st, nil)
	// Only Claude rows with a live session are looked up; gomock fails the
	// test on a call for the other-agent or ended row.
	history := mock_web.NewMockSessionHistory(ctrl)
	history.EXPECT().ContextTokens("/ws/a", "s1").Return(1234)
	history.EXPECT().ContextTokens("/ws/b", "s2").Return(0)

	srv := NewServer(Deps{State: mockState, History: history}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))

	var got state.StateFile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]int{"@1": 1234, "@2": 0, "@3": 0, "@4": 0}
	for _, p := range got.Projects {
		if p.Tokens != want[p.WindowID] {
			t.Errorf("%s: want tokens %d, got %d", p.WindowID, want[p.WindowID], p.Tokens)
		}
	}
}
