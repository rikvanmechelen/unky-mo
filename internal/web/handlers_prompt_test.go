package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Multi-line text (e.g. a review from the editor tabs) goes in as one
// bracketed paste instead of being typed, which would submit at the first
// newline.
func TestHandlePromptMultilineUsesPaste(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@5", SessionID: "s1", Status: "idle"}},
	}, nil)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendPastedText("test:@5.0", "line one\nline two").Return(nil)

	srv := NewServer(Deps{State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"line one\r\nline two\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePrompt413OnHugeText(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"`+strings.Repeat("x", maxPromptBytes+1)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: want 413, got %d", rec.Code)
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

func TestHandlePrompt200WhenStatusIsQuestion(t *testing.T) {
	// A pending AskUserQuestion is exactly the case answering unblocks —
	// must be accepted like idle, not rejected like a genuine permission
	// prompt.
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "question"}},
	}, nil)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendLiteralText("test:@1.0", "1").Return(nil)

	srv := NewServer(Deps{State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePrompt409WhenStatusIsPermission(t *testing.T) {
	// A genuine permission dialog stays rejected — we don't yet capture
	// enough from Claude Code's PermissionRequest hook to safely show/answer
	// it from the web UI (see handlePrompt's doc comment).
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "permission"}},
	}, nil)

	srv := NewServer(Deps{State: mockState}, 0, "test")
	rec := postPrompt(t, srv, "@1", `{"text":"1"}`)
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
