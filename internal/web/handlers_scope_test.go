package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	"github.com/rvanmech/unky-mo/internal/tickets"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func scopeFixture(t *testing.T) (*Server, *mock_web.MockGitFiles, *mock_web.MockScopeChecker, *mock_web.MockTicketSource) {
	t.Helper()
	srv, git := filesFixture(t)
	ctrl := gomock.NewController(t)
	sc, tk := mock_web.NewMockScopeChecker(ctrl), mock_web.NewMockTicketSource(ctrl)
	srv.deps.Scope, srv.deps.Tickets = sc, tk
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(branchOverview(), nil).AnyTimes()
	return srv, git, sc, tk
}

func post(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

// Only files the overview lists reach the checker, each once; prompts are
// cut short; the ticket is fetched server-side.
func TestScopeSendsOnlyListedFiles(t *testing.T) {
	srv, _, sc, tk := scopeFixture(t)
	tk.EXPECT().Detail(gomock.Any(), "jira", "OP-212").Return(&tickets.TicketDetail{Ticket: tickets.Ticket{ID: "OP-212", Title: "Cleanup"}, DescriptionText: "Remove worktrees."}, nil)
	sc.EXPECT().Check(gomock.Any()).DoAndReturn(func(req review.ScopeRequest) (*review.ScopeResult, error) {
		if req.Ticket == nil || req.Ticket.Description != "Remove worktrees." {
			t.Errorf("ticket %+v", req.Ticket)
		}
		// Turn 2's only file was already sent, but its prompt stays as context.
		if len(req.Turns) != 3 || strings.Join(req.Turns[0].Files, ",") != "a.go" || len(req.Turns[1].Files) != 0 || strings.Join(req.Turns[2].Files, ",") != "gone.go" {
			t.Errorf("turns %+v", req.Turns)
		}
		if n := len([]rune(req.Turns[0].Prompt)); n != maxScopePrompt+1 {
			t.Errorf("prompt not cut: %d runes", n)
		}
		return &review.ScopeResult{Summary: "ok", Files: []review.ScopeFile{{Path: "a.go", Verdict: review.VerdictInScope}}}, nil
	})
	body := `{"ticket":{"provider":"jira","id":"OP-212"},"turns":[
		{"n":1,"prompt":"` + strings.Repeat("x", 3000) + `","files":["a.go","/etc/passwd",".env"]},
		{"n":2,"prompt":"again","files":["a.go"]},
		{"n":0,"files":["gone.go"]}]}`
	rec := post(t, srv, "/api/sessions/@5/scope", body)
	var got scopeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Summary != "ok" || got.Ticket.Title != "Cleanup" || got.Ticket.Description != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// A ticket id that isn't a plain key is never fetched; a failed fetch still
// runs the check, and says why the ticket is missing.
func TestScopeTicketHandling(t *testing.T) {
	srv, _, sc, tk := scopeFixture(t)
	sc.EXPECT().Check(gomock.Any()).DoAndReturn(func(req review.ScopeRequest) (*review.ScopeResult, error) {
		if req.Ticket != nil {
			t.Errorf("ticket sent: %+v", req.Ticket)
		}
		return &review.ScopeResult{Files: []review.ScopeFile{}}, nil
	}).Times(2)
	if rec := post(t, srv, "/api/sessions/@5/scope", `{"ticket":{"provider":"jira","id":"../../x"},"turns":[{"n":1,"prompt":"p","files":["a.go"]}]}`); rec.Code != http.StatusOK {
		t.Errorf("bad id: %d", rec.Code)
	}
	tk.EXPECT().Detail(gomock.Any(), "jira", "OP-1").Return(nil, errors.New("401 Unauthorized"))
	rec := post(t, srv, "/api/sessions/@5/scope", `{"ticket":{"provider":"jira","id":"OP-1"},"turns":[{"n":1,"prompt":"p","files":["a.go"]}]}`)
	var got scopeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.TicketError != "401 Unauthorized" {
		t.Errorf("fetch failure: %d %s", rec.Code, rec.Body)
	}
}

func TestScopeOneAtATime(t *testing.T) {
	srv, _, sc, _ := scopeFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	sc.EXPECT().Check(gomock.Any()).DoAndReturn(func(review.ScopeRequest) (*review.ScopeResult, error) {
		close(started)
		<-release
		return &review.ScopeResult{}, nil
	})
	body := `{"turns":[{"n":1,"prompt":"p","files":["a.go"]}]}`
	done := make(chan int)
	go func() { done <- post(t, srv, "/api/sessions/@5/scope", body).Code }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first check didn't start")
	}
	if rec := post(t, srv, "/api/sessions/@5/scope", body); rec.Code != http.StatusConflict {
		t.Errorf("second check: want 409, got %d", rec.Code)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first check: %d", code)
	}
}

func TestScopeRejects(t *testing.T) {
	srv, _, _, _ := scopeFixture(t)
	// No checker calls for any of these.
	// Only POST is routed (a GET falls through to the static files).
	if rec := get(t, srv, "/api/sessions/@5/scope"); rec.Code == http.StatusOK {
		t.Errorf("GET: got 200")
	}
	if rec := post(t, srv, "/api/sessions/@5/scope", `nope`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body: want 400, got %d", rec.Code)
	}
	if rec := post(t, srv, "/api/sessions/@5/scope?base=x", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base: want 400, got %d", rec.Code)
	}
	if rec := post(t, srv, "/api/sessions/@6/scope", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
}

// With more prompts than fit, the most recent ones are kept, plus the files
// changed outside the conversation.
func TestScopeKeepsRecentTurns(t *testing.T) {
	srv, _, sc, _ := scopeFixture(t)
	sc.EXPECT().Check(gomock.Any()).DoAndReturn(func(req review.ScopeRequest) (*review.ScopeResult, error) {
		if len(req.Turns) != maxScopeTurns || req.Turns[0].N != 22 || req.Turns[len(req.Turns)-1].N != 0 {
			t.Errorf("got %d turns, first %d, last %d", len(req.Turns), req.Turns[0].N, req.Turns[len(req.Turns)-1].N)
		}
		return &review.ScopeResult{}, nil
	})
	var turns []string
	for n := 1; n <= 100; n++ {
		turns = append(turns, `{"n":`+strconv.Itoa(n)+`,"prompt":"p","files":[]}`)
	}
	turns = append(turns, `{"n":0,"files":["a.go"]}`)
	if rec := post(t, srv, "/api/sessions/@5/scope", `{"turns":[`+strings.Join(turns, ",")+`]}`); rec.Code != http.StatusOK {
		t.Errorf("got %d", rec.Code)
	}
}
