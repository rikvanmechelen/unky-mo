package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func postPrompt(t *testing.T, srv *Server, windowID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+windowID+"/prompt", bytes.NewBufferString(body))
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHandlePrompt400OnEmptyText(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}

func TestHandlePrompt400OnMultilineText(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"line one\nline two"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}

func TestHandlePrompt400OnMalformedJSON(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	rec := postPrompt(t, srv, "@1", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}

func TestHandlePrompt404WhenWindowUnknown(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{}, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"hello"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

func TestHandlePrompt409WhenNotIdle(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "active"}},
	}, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"hello"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

func TestHandlePrompt200CallsSendLiteralTextWithResolvedTarget(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@5", SessionID: "s1", Status: "idle"}},
	}, nil)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendLiteralText("test:@5.0", "hello").Return(nil)

	srv := NewServer(Deps{State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePrompt500WhenSendFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "idle"}},
	}, nil)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendLiteralText(gomock.Any(), gomock.Any()).Return(errSendFailed)

	srv := NewServer(Deps{State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"hello"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
}

type sendFailedError string

func (e sendFailedError) Error() string { return string(e) }

const errSendFailed = sendFailedError("tmux send-keys failed")
