package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
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

// emptyBoxPane is Claude's pane showing an empty message box: what an idle
// or working session must show before anything is typed into it.
func emptyBoxPane(t *testing.T, ctrl *gomock.Controller) *mock_web.MockClaudePane {
	t.Helper()
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture(gomock.Any()).Return(readQueueFixture(t, "working-empty.txt"), nil).AnyTimes()
	return pane
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

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, ctrl), State: mockState, Prompts: mockPrompts}, 0, "test")
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
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "dead"}},
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
	}, nil).AnyTimes()
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

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, ctrl), State: mockState, Prompts: mockPrompts}, 0, "test")
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

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, ctrl), State: mockState, Prompts: mockPrompts}, 0, "test")
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

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, ctrl), State: mockState, Prompts: mockPrompts}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"> line \u001b[201~ rest\n\tindented\u0007"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Attachments are ids; the handler resolves them to the stored files and
// hands those paths, in order, to SendPrompt.
func TestHandlePromptWithAttachmentsSendsResolvedPaths(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a := saveID(t, store, "@5", strings.NewReader(pngHeader))
	b := saveID(t, store, "@5", strings.NewReader("GIF89a"))
	pa, _ := store.Path("@5", a)
	pb, _ := store.Path("@5", b)

	ctrl := gomock.NewController(t)
	mockPrompts := mock_web.NewMockPromptSender(ctrl)
	mockPrompts.EXPECT().SendPrompt("test:@5.0", "what is\nthis", []string{pb, pa}).Return(nil)

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, gomock.NewController(t)), State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"what is\nthis","attachments":["`+b+`","`+a+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePromptImageOnly(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a := saveID(t, store, "@5", strings.NewReader(pngHeader))
	pa, _ := store.Path("@5", a)

	mockPrompts := mock_web.NewMockPromptSender(gomock.NewController(t))
	mockPrompts.EXPECT().SendPrompt("test:@5.0", "", []string{pa}).Return(nil)

	srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, gomock.NewController(t)), State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
	rec := postPrompt(t, srv, "@5", `{"text":"  ","attachments":["`+a+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A file is named after the text as a path; images still go to SendPrompt.
func TestHandlePromptWithFiles(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	img := saveID(t, store, "@5", strings.NewReader(pngHeader))
	f1 := saveFile(t, store, "@5", "report.pdf", "%PDF-1.7")
	f2 := saveFile(t, store, "@5", "app.log", "boom")
	pimg, _ := store.Path("@5", img)
	p1, _ := store.Path("@5", f1)
	p2, _ := store.Path("@5", f2)

	for _, c := range []struct {
		body   string
		expect func(m *mock_web.MockPromptSender)
	}{
		{`{"text":"","attachments":["` + f1 + `"]}`, func(m *mock_web.MockPromptSender) {
			m.EXPECT().SendLiteralText("test:@5.0", "Attached file: "+p1).Return(nil)
		}},
		{`{"text":"why?","attachments":["` + f1 + `"]}`, func(m *mock_web.MockPromptSender) {
			m.EXPECT().SendPastedText("test:@5.0", "why?\n\nAttached file: "+p1).Return(nil)
		}},
		{`{"text":"look","attachments":["` + f1 + `","` + img + `","` + f2 + `"]}`, func(m *mock_web.MockPromptSender) {
			m.EXPECT().SendPrompt("test:@5.0", "look\n\nAttached files:\n- "+p1+"\n- "+p2, []string{pimg}).Return(nil)
		}},
	} {
		mockPrompts := mock_web.NewMockPromptSender(gomock.NewController(t))
		c.expect(mockPrompts)
		srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, gomock.NewController(t)), State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
		if rec := postPrompt(t, srv, "@5", c.body); rec.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d: %s", c.body, rec.Code, rec.Body.String())
		}
	}
}

// An id that isn't in this window's store (another window's, expired, or a
// path) is refused before anything reaches the pane.
func TestHandlePrompt400OnUnknownAttachment(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	other := saveID(t, store, "@6", strings.NewReader(pngHeader))

	for _, id := range []string{other, "../../etc/passwd", "/etc/passwd"} {
		// No PromptSender expectations: any send fails the test.
		mockPrompts := mock_web.NewMockPromptSender(gomock.NewController(t))
		srv := NewServer(Deps{ClaudePane: emptyBoxPane(t, gomock.NewController(t)), State: liveState(t, "@5", "idle"), Prompts: mockPrompts, Attachments: store}, 0, "test")
		rec := postPrompt(t, srv, "@5", `{"text":"hi","attachments":["`+id+`"]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("attachment %q: want 400, got %d", id, rec.Code)
		}
	}
}

func TestHandlePrompt409WithAttachmentsWhenBusy(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	a := saveID(t, store, "@5", strings.NewReader(pngHeader))

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

func readQueueFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/queue/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func activePromptServer(t *testing.T, pane *mock_web.MockClaudePane, prompts *mock_web.MockPromptSender) *Server {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: "active"}},
	}, nil)
	return NewServer(Deps{State: mockState, Prompts: prompts, ClaudePane: pane}, 0, "test")
}

// A working session queues what is typed, as Claude Code's own terminal does.
func TestHandlePromptQueuesWhileActive(t *testing.T) {
	for _, fixture := range []string{"working-empty.txt", "working-queued.txt"} {
		t.Run(fixture, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			pane := mock_web.NewMockClaudePane(ctrl)
			pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, fixture), nil)
			prompts := mock_web.NewMockPromptSender(ctrl)
			prompts.EXPECT().SendLiteralText("test:@1.0", "hello").Return(nil)
			rec := postPrompt(t, activePromptServer(t, pane, prompts), "@1", `{"text":"hello"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Text already typed in the terminal would mix with the queued message.
func TestHandlePromptActive409WhenTerminalBoxHasText(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-typed.txt"), nil)
	prompts := mock_web.NewMockPromptSender(ctrl) // no expectations: nothing typed
	rec := postPrompt(t, activePromptServer(t, pane, prompts), "@1", `{"text":"hello"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

func TestHandlePromptActive409WhenBoxUnreadable(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("test:@1.0").Return("some dialog, no prompt box", nil)
	prompts := mock_web.NewMockPromptSender(ctrl)
	rec := postPrompt(t, activePromptServer(t, pane, prompts), "@1", `{"text":"hello"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d", rec.Code)
	}
}

// statesServer serves one state read per status, in order.
func statesServer(t *testing.T, pane *mock_web.MockClaudePane, prompts *mock_web.MockPromptSender, statuses ...string) *Server {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockState := mock_web.NewMockStateReader(ctrl)
	var calls []any
	for _, st := range statuses {
		calls = append(calls, mockState.EXPECT().Read().Return(&state.StateFile{
			Projects: []state.ProjectState{{WindowID: "@1", SessionID: "s1", Status: st}},
		}, nil))
	}
	gomock.InOrder(calls...)
	return NewServer(Deps{State: mockState, Prompts: prompts, ClaudePane: pane}, 0, "test")
}

// Send now types the message (it queues), then presses Claude Code's
// ctrl+x ctrl+s so it's taken at once.
func TestHandlePromptSendNowPressesChordAfterText(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	prompts := mock_web.NewMockPromptSender(ctrl)
	gomock.InOrder(
		pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-empty.txt"), nil),
		prompts.EXPECT().SendLiteralText("test:@1.0", "now please").Return(nil),
		// Not queued yet on the first look (Enter was just pressed).
		pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-empty.txt"), nil),
		pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-queued.txt"), nil),
		pane.EXPECT().SendKeys("test:@1.0", []tmux.Key{{Name: "C-x"}, {Name: "C-s"}}).Return(nil),
	)
	srv := statesServer(t, pane, prompts, "active", "active", "active")
	srv.modeSettle = 0
	rec := postPrompt(t, srv, "@1", `{"text":"now please","sendNow":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// If Claude finished meanwhile the message went in as a normal prompt and
// there is nothing to flush; the chord would land in an idle box.
func TestHandlePromptSendNowSkipsChordWhenNoLongerActive(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	prompts := mock_web.NewMockPromptSender(ctrl)
	pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-empty.txt"), nil)
	prompts.EXPECT().SendLiteralText("test:@1.0", "now please").Return(nil)
	srv := statesServer(t, pane, prompts, "active", "idle") // no SendKeys expected
	srv.modeSettle = 0
	rec := postPrompt(t, srv, "@1", `{"text":"now please","sendNow":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// sendNow means nothing for an idle session: no chord, no pane read.
func TestHandlePromptSendNowIgnoredWhenIdle(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := emptyBoxPane(t, ctrl)
	prompts := mock_web.NewMockPromptSender(ctrl)
	prompts.EXPECT().SendLiteralText("test:@1.0", "hi").Return(nil)
	srv := statesServer(t, pane, prompts, "idle")
	rec := postPrompt(t, srv, "@1", `{"text":"hi","sendNow":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A queued message that never shows up means the chord would do nothing (or
// Claude Code changed its hint): say so instead of failing silently.
func TestHandlePromptSendNowErrorsWhenNeverQueued(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	prompts := mock_web.NewMockPromptSender(ctrl)
	pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-empty.txt"), nil).Times(1 + sendNowTries)
	prompts.EXPECT().SendLiteralText("test:@1.0", "now please").Return(nil)
	var statuses []string
	for range 1 + sendNowTries {
		statuses = append(statuses, "active")
	}
	srv := statesServer(t, pane, prompts, statuses...)
	srv.modeSettle = 0
	rec := postPrompt(t, srv, "@1", `{"text":"now please","sendNow":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An idle session whose screen shows a dialog instead of the message box
// (/config here, where Enter toggles a setting) gets nothing typed into it.
func TestHandlePromptIdleRefusedWithoutMessageBox(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	b, err := os.ReadFile("testdata/menus/config.txt")
	if err != nil {
		t.Fatal(err)
	}
	pane.EXPECT().Capture("test:@1.0").Return(string(b), nil)
	// No PromptSender expectations: any send fails the test.
	srv := NewServer(Deps{State: liveState(t, "@1", "idle"), Prompts: mock_web.NewMockPromptSender(ctrl), ClaudePane: pane}, 0, "test")
	if rec := postPrompt(t, srv, "@1", `{"text":"y"}`); rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body)
	}
}

// Text typed in the terminal's box isn't appended to.
func TestHandlePromptIdleRefusedWhenBoxHasText(t *testing.T) {
	ctrl := gomock.NewController(t)
	pane := mock_web.NewMockClaudePane(ctrl)
	pane.EXPECT().Capture("test:@1.0").Return(readQueueFixture(t, "working-typed.txt"), nil)
	srv := NewServer(Deps{State: liveState(t, "@1", "idle"), Prompts: mock_web.NewMockPromptSender(ctrl), ClaudePane: pane}, 0, "test")
	if rec := postPrompt(t, srv, "@1", `{"text":"hi"}`); rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body)
	}
}

// A captured AskUserQuestion takes text from the message box only on its
// "Type something" row: elsewhere a digit would pick a row.
func TestHandlePromptQuestionNeedsTypeSomething(t *testing.T) {
	qs := []questionSpec{spec("Which pet?", false, "Cat", "Dog")}
	for _, c := range []struct {
		cursor int
		code   int
	}{{1, http.StatusConflict}, {3, http.StatusOK}} {
		d := newFakeDialog(qs)
		d.cursor = c.cursor
		srv := answerServer(t, questionRow(t, qs), d)
		prompts := mock_web.NewMockPromptSender(gomock.NewController(t))
		if c.code == http.StatusOK {
			prompts.EXPECT().SendLiteralText("mo:@4.0", "a parrot").Return(nil)
		}
		srv.deps.Prompts = prompts
		if rec := postPrompt(t, srv, "@4", `{"text":"a parrot"}`); rec.Code != c.code {
			t.Errorf("cursor on row %d: want %d, got %d: %s", c.cursor, c.code, rec.Code, rec.Body)
		}
	}
}

// A Tab can't be typed (it'd be a key); text with one goes in as a paste.
func TestHandlePromptTabIsPasted(t *testing.T) {
	ctrl := gomock.NewController(t)
	prompts := mock_web.NewMockPromptSender(ctrl)
	prompts.EXPECT().SendPastedText("test:@1.0", "a\tb").Return(nil)
	srv := NewServer(Deps{State: liveState(t, "@1", "idle"), Prompts: prompts, ClaudePane: emptyBoxPane(t, ctrl)}, 0, "test")
	if rec := postPrompt(t, srv, "@1", `{"text":"a\tb"}`); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
}

// The send-now hint counts only as its own line just above the message
// box, not quoted in the transcript.
func TestQueuedHintShown(t *testing.T) {
	if !queuedHintShown(readQueueFixture(t, "working-queued.txt")) || !queuedHintShown(readQueueFixture(t, "working-queued-two.txt")) {
		t.Error("the queued captures show the hint")
	}
	if queuedHintShown(readQueueFixture(t, "working-empty.txt")) {
		t.Error("nothing is queued in working-empty")
	}
	quoted := "● Read(CLAUDE.md)\n  ⎿  … then presses ctrl+x ctrl+s to send now in Claude's pane …\n" +
		strings.Repeat("\n", 20) + readQueueFixture(t, "working-empty.txt")
	if queuedHintShown(quoted) {
		t.Error("a quoted phrase in the transcript counted")
	}
}
