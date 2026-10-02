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

	srv := NewServer(Deps{State: mockState}, 0)
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

	srv := NewServer(Deps{State: mockState}, 0)
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

	srv := NewServer(Deps{State: mockState}, 0)
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
