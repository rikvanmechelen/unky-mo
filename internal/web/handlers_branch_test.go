package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/review"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

const branchHead = "fedcba9876543210fedcba9876543210fedcba98"

type branchMocks struct {
	git   *mock_web.MockGitFiles
	prs   *mock_web.MockPRClient
	scope *mock_web.MockScopeChecker
}

// branchFixture: project "app" at /ws/app, whose main checkout has "main",
// a worktree has "feat" (with a live session in a subdirectory), and
// "idle" exists but isn't checked out.
func branchFixture(t *testing.T) (*Server, branchMocks) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{
		{Name: "app@feat", WindowID: "@9", SessionID: "s9", Path: "/ws/app.worktrees/feat/sub", Status: "idle"},
	}}, nil).AnyTimes()
	pl := mock_web.NewMockProjectLister(ctrl)
	pl.EXPECT().LoadProjects().Return([]project.Project{{Name: "app", Path: "/ws/app"}}, nil).AnyTimes()
	wt := mock_web.NewMockWorktreeReader(ctrl)
	wt.EXPECT().ListBranches("/ws/app").Return([]project.Branch{
		{Name: "main", IsMain: true},
		{Name: "feat", WorktreePath: "/ws/app.worktrees/feat"},
		{Name: "idle"},
	}, nil).AnyTimes()
	m := branchMocks{git: mock_web.NewMockGitFiles(ctrl), prs: mock_web.NewMockPRClient(ctrl), scope: mock_web.NewMockScopeChecker(ctrl)}
	srv := NewServer(Deps{State: st, Projects: pl, Worktrees: wt, Git: m.git, PRs: m.prs, Scope: m.scope}, 0, "test")
	return srv, m
}

func refOverview() *gitfiles.Overview {
	return &gitfiles.Overview{
		Root: "/ws/app", Branch: "idle", Mode: gitfiles.ModeBranch, Base: "main", MergeBase: mergeBase, Rev: mergeBase, Head: branchHead,
		Files: []gitfiles.OverviewFile{
			{Path: "a.go", Status: "M", Kind: gitfiles.KindLogic},
			{Path: "new.go", OldPath: "old.go", Status: "R", Kind: gitfiles.KindRenamed},
			{Path: "gone.go", Status: "D", Kind: gitfiles.KindLogic},
		},
	}
}

// A checked-out branch uses its checkout, like the chat view, and names
// the live session in it.
func TestBranchOverviewCheckout(t *testing.T) {
	srv, m := branchFixture(t)
	m.git.EXPECT().Overview("/ws/app.worktrees/feat", gitfiles.ModeHead).Return(&gitfiles.Overview{Root: "/ws/app.worktrees/feat"}, nil)
	rec := get(t, srv, "/api/projects/app/branches/feat/overview?base=head")
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if got.Target == nil || got.Target.Kind != "checkout" || got.Target.Path != "/ws/app.worktrees/feat" || got.Target.SessionWindow != "@9" {
		t.Errorf("target %+v", got.Target)
	}
	// The main checkout, with no session in it.
	m.git.EXPECT().Overview("/ws/app", gitfiles.ModeBranch).Return(&gitfiles.Overview{Root: "/ws/app"}, nil)
	rec = get(t, srv, "/api/projects/app/branches/main/overview")
	var main overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &main); err != nil || main.Target.Path != "/ws/app" || main.Target.SessionWindow != "" {
		t.Errorf("main: %d %s", rec.Code, rec.Body)
	}
}

// A branch nobody has checked out is read at its head commit; a PR's from
// origin. Both overview and architecture share one resolution and one
// overview read.
func TestBranchOverviewRef(t *testing.T) {
	srv, m := branchFixture(t)
	m.git.EXPECT().ResolveBranch("/ws/app", "idle", false).Return(branchHead, nil).Times(1)
	m.git.EXPECT().OverviewAt("/ws/app", "idle", branchHead, "").Return(refOverview(), nil).Times(1)
	rec := get(t, srv, "/api/projects/app/branches/idle/overview")
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Target.Kind != "ref" || got.Head != branchHead {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	an := mock_web.NewMockChangeAnalyzer(gomock.NewController(t))
	srv.deps.Review = an
	an.EXPECT().Analyze(gomock.Any()).Return(&review.Analysis{}, nil)
	if rec := get(t, srv, "/api/projects/app/branches/idle/architecture"); rec.Code != http.StatusOK {
		t.Errorf("architecture: %d %s", rec.Code, rec.Body)
	}

	// A branch only on origin (slashes and all) is fetched.
	m.git.EXPECT().ResolveBranch("/ws/app", "rik/OP-1-x", true).Return(branchHead, nil)
	m.git.EXPECT().OverviewAt("/ws/app", "rik/OP-1-x", branchHead, "").Return(refOverview(), nil)
	if rec := get(t, srv, "/api/projects/app/branches/rik%2FOP-1-x/overview"); rec.Code != http.StatusOK {
		t.Errorf("remote branch: %d %s", rec.Code, rec.Body)
	}
}

// A pull request is resolved by number: its head from refs/pull/<n>/head,
// compared with its own base branch, and named in the target.
func TestPullOverview(t *testing.T) {
	srv, m := branchFixture(t)
	m.prs.EXPECT().GetPRDetail("/ws/app", 12).Return(&github.PRDetail{Number: 12, Title: "Fix it", URL: "https://gh/12", Branch: "rik/fix", BaseBranch: "develop"}, nil).Times(1)
	m.git.EXPECT().ResolvePR("/ws/app", 12, "develop").Return(branchHead, nil).Times(1)
	m.git.EXPECT().OverviewAt("/ws/app", "rik/fix", branchHead, "origin/develop").Return(refOverview(), nil).Times(1)
	for i := 0; i < 2; i++ {
		rec := get(t, srv, "/api/projects/app/pulls/12/overview")
		var got overviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		if tv := got.Target; tv.Kind != "ref" || tv.PR != 12 || tv.PRTitle != "Fix it" || tv.Branch != "rik/fix" || tv.PRURL != "https://gh/12" {
			t.Errorf("target %+v", tv)
		}
	}
	// A PR whose branch is checked out uses that checkout.
	m.prs.EXPECT().GetPRDetail("/ws/app", 13).Return(&github.PRDetail{Number: 13, Branch: "feat", BaseBranch: "main"}, nil)
	m.git.EXPECT().Overview("/ws/app.worktrees/feat", gitfiles.ModeBranch).Return(&gitfiles.Overview{Root: "/ws/app.worktrees/feat"}, nil)
	if rec := get(t, srv, "/api/projects/app/pulls/13/overview"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"checkout"`) {
		t.Errorf("checked out pr: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, "/api/projects/app/pulls/x/overview"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad pr: %d", rec.Code)
	}
}

func TestBranchResolveErrors(t *testing.T) {
	srv, m := branchFixture(t)
	if rec := get(t, srv, "/api/projects/nope/branches/main/overview"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", rec.Code)
	}
	m.git.EXPECT().ResolveBranch("/ws/app", "--upload-pack=x", true).Return("", gitfiles.ErrBadBranch)
	if rec := get(t, srv, "/api/projects/app/branches/--upload-pack=x/overview"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad name: %d", rec.Code)
	}
	m.git.EXPECT().ResolveBranch("/ws/app", "gone", true).Return("", gitfiles.ErrNoBranch)
	if rec := get(t, srv, "/api/projects/app/branches/gone/overview"); rec.Code != http.StatusNotFound {
		t.Errorf("missing branch: %d", rec.Code)
	}
}

// A ref target's files: the head commit's version (read-only), the merge
// base's under a rename's old name, a deleted file through the change
// list, and nothing outside the tree or the change.
func TestBranchFileRef(t *testing.T) {
	srv, m := branchFixture(t)
	m.git.EXPECT().ResolveBranch("/ws/app", "idle", false).Return(branchHead, nil).AnyTimes()
	m.git.EXPECT().OverviewAt("/ws/app", "idle", branchHead, "").Return(refOverview(), nil).AnyTimes()
	m.git.EXPECT().TreeAt("/ws/app", branchHead).Return([]string{"a.go", "lib/util.go", "new.go"}, nil).Times(1)
	m.git.EXPECT().ReadAt("/ws/app", branchHead, "lib/util.go").Return(&gitfiles.Content{Path: "lib/util.go", Exists: true, Hash: "h1", Text: "x"}, nil)
	m.git.EXPECT().ReadAt("/ws/app", mergeBase, "old.go").Return(&gitfiles.Content{Path: "old.go", Exists: true, Hash: "h2"}, nil)
	m.git.EXPECT().ReadAt("/ws/app", mergeBase, "gone.go").Return(&gitfiles.Content{Path: "gone.go", Exists: true, Hash: "h3"}, nil)

	rec := get(t, srv, "/api/projects/app/branches/idle/file?path=lib/util.go")
	var got fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.ReadOnly || got.Text != "x" {
		t.Errorf("head: %d %s", rec.Code, rec.Body)
	}
	rec = get(t, srv, "/api/projects/app/branches/idle/file?path=new.go&rev=base")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Path != "new.go" || got.Hash != "h2" {
		t.Errorf("rename base: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, "/api/projects/app/branches/idle/file?path=gone.go&rev=base"); rec.Code != http.StatusOK {
		t.Errorf("deleted: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{".env", "../x", "/etc/passwd"} {
		if rec := get(t, srv, "/api/projects/app/branches/idle/file?path="+p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
	if rec := get(t, srv, "/api/projects/app/branches/idle/tree"); rec.Code != http.StatusOK {
		t.Errorf("tree: %d", rec.Code)
	}
	// No writes to a branch that isn't checked out.
	if rec := put(t, srv, "/api/projects/app/branches/idle/file", `{"path":"a.go","text":"x","baseHash":"h"}`); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("save: want 405, got %d", rec.Code)
	}
}

// A checked-out branch's files follow the session rules, writes included.
func TestBranchFileCheckout(t *testing.T) {
	srv, m := branchFixture(t)
	m.git.EXPECT().Tree("/ws/app.worktrees/feat").Return("/ws/app.worktrees/feat", []string{"a.go"}, nil).AnyTimes()
	m.git.EXPECT().ReadFile("/ws/app.worktrees/feat", "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "h"}, nil)
	m.git.EXPECT().WriteFile("/ws/app.worktrees/feat", "a.go", "new", "h").Return(&gitfiles.Content{Path: "a.go", Exists: true, Hash: "h2"}, nil)
	rec := get(t, srv, "/api/projects/app/branches/feat/file?path=a.go")
	var got fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.ReadOnly {
		t.Errorf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := put(t, srv, "/api/projects/app/branches/feat/file", `{"path":"a.go","text":"new","baseHash":"h"}`); rec.Code != http.StatusOK {
		t.Errorf("save: %d %s", rec.Code, rec.Body)
	}
}

// For a PR, its title and description join the scope check's ask.
func TestBranchScopeAddsPR(t *testing.T) {
	srv, m := branchFixture(t)
	m.git.EXPECT().ResolvePR("/ws/app", 7, "main").Return(branchHead, nil)
	m.git.EXPECT().OverviewAt("/ws/app", "idle", branchHead, "origin/main").Return(refOverview(), nil)
	m.prs.EXPECT().GetPRDetail("/ws/app", 7).Return(&github.PRDetail{Title: "Idle cleanup", Body: "Why: …", Branch: "idle", BaseBranch: "main"}, nil)
	m.scope.EXPECT().Check(gomock.Any()).DoAndReturn(func(req review.ScopeRequest) (*review.ScopeResult, error) {
		if req.PR == nil || req.PR.ID != "7" || req.PR.Title != "Idle cleanup" || req.PR.Description != "Why: …" {
			t.Errorf("pr %+v", req.PR)
		}
		if req.Head != branchHead || req.Rev != mergeBase || len(req.Turns) != 1 {
			t.Errorf("req %+v", req)
		}
		return &review.ScopeResult{Files: []review.ScopeFile{}}, nil
	})
	rec := post(t, srv, "/api/projects/app/pulls/7/scope", `{"turns":[{"n":0,"files":["a.go","not-listed.go"]}]}`)
	var got scopeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.PR == nil || got.PR.Title != "Idle cleanup" {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

// A PR that can't be read can't be resolved: 502, and nothing is fetched.
func TestPullUnreadable(t *testing.T) {
	srv, m := branchFixture(t)
	m.prs.EXPECT().GetPRDetail("/ws/app", 7).Return(nil, errors.New("gh: not found"))
	if rec := get(t, srv, "/api/projects/app/pulls/7/overview"); rec.Code != http.StatusBadGateway {
		t.Errorf("got %d", rec.Code)
	}
}
