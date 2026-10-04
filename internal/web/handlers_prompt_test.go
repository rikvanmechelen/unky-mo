package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// Escape sequences can't ride along into Claude's terminal (e.g. one that
// ends a bracketed paste early): control characters other than newline and
// tab are dropped before sending.
func TestHandlePromptStripsControlCharacters(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@5", SessionID: "s1", Status: "idle"}},
	}, nil)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendPastedText("test:@5.0", "> line [201~ rest\n\tindented").Return(nil)

	srv := NewServer(Deps{State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"> line \u001b[201~ rest\n\tindented\u0007"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Attachments are ids; the handler resolves them to the stored files and
// hands those paths, in order, to SendPrompt.
func TestHandlePromptWithAttachmentsSendsResolvedPaths(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a, _ := store.Save("@5", strings.NewReader(pngHeader))
	b, _ := store.Save("@5", strings.NewReader("GIF89a"))
	pa, _ := store.Path("@5", a)
	pb, _ := store.Path("@5", b)

	ctrl := gomock.NewController(t)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendPrompt("test:@5.0", "what is\nthis", []string{pb, pa}).Return(nil)

	srv := NewServer(Deps{State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"what is\nthis","attachments":["`+b+`","`+a+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePromptImageOnly(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a, _ := store.Save("@5", strings.NewReader(pngHeader))
	pa, _ := store.Path("@5", a)

	mockPrompts := mock_web.NewMockPromptSender(gomock.NewController(t))
	mockPrompts.EXPECT().SendPrompt("test:@5.0", "", []string{pa}).Return(nil)

	srv := NewServer(Deps{State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"  ","attachments":["`+a+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An id that isn't in this window's store (another window's, expired, or a
// path) is refused before anything reaches the pane.
func TestHandlePrompt400OnUnknownAttachment(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	other, _ := store.Save("@6", strings.NewReader(pngHeader))

	for _, id := range []string{other, "../../etc/passwd", "/etc/passwd"} {
		// No PromptSender expectations: any send fails the test.
		mockPrompts := mock_web.NewMockPromptSender(gomock.NewController(t))
		srv := NewServer(Deps{State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
		rec := postPrompt(t, srv, "@5", `{"text":"hi","attachments":["`+id+`"]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("attachment %q: want 400, got %d", id, rec.Code)
		}
	}
}

func TestHandlePrompt409WithAttachmentsWhenBusy(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a, _ := store.Save("@5", strings.NewReader(pngHeader))

	srv := NewServer(Deps{State: liveState(t, "@5", "active"), Attachments: store}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"hi","attachments":["`+a+`"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

func TestHandlePrompt400OnTooManyAttachments(t *testing.T) {
	ids := strings.Repeat(`"x",`, maxPromptAttachments) + `"x"`
	srv := NewServer(Deps{}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"hi","attachments":[`+ids+`]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}
