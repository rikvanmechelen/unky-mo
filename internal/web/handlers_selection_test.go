package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// Selected commits, the commit before them and their newest one.
var (
	selA    = strings.Repeat("a", 40)
	selB    = strings.Repeat("b", 40)
	selBase = strings.Repeat("c", 40)
)

func selectionOverview() *gitfiles.Overview {
	return &gitfiles.Overview{
		Root: "/ws/foo", Mode: gitfiles.ModeCommits, Rev: selBase, MergeBase: selBase, Head: selB,
		Files: []gitfiles.OverviewFile{
			{Path: "a.go", Status: "M", Added: 1, Kind: gitfiles.KindLogic, Area: "."},
			{Path: "sub/new.go", OldPath: "sub/old.go", Status: "R", Kind: gitfiles.KindRenamed, Area: "sub"},
		},
	}
}

func selectionFixture(t *testing.T) (*Server, *mock_web.MockGitFiles, *mock_web.MockChangeAnalyzer, *mock_web.MockCallGrapher) {
	t.Helper()
	srv, git := filesFixture(t)
	ctrl := gomock.NewController(t)
	an, cg := mock_web.NewMockChangeAnalyzer(ctrl), mock_web.NewMockCallGrapher(ctrl)
	srv.deps.Review, srv.deps.Calls = an, cg
	return srv, git, an, cg
}

// One resolution and one overview serve /overview, /architecture and
// /calls, whatever order the commits are named in.
func TestSelectionSharesResolutionAcrossEndpoints(t *testing.T) {
	srv, git, an, cg := selectionFixture(t)
	o := selectionOverview()
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA, selB}).Return(&gitfiles.Selection{Root: "/ws/foo", Base: selBase, Head: selB}, nil).Times(1)
	git.EXPECT().OverviewRange("/ws/foo", selBase, selB).Return(o, nil).Times(1)
	an.EXPECT().Analyze(o).Return(&review.Analysis{}, nil).Times(1)
	cg.EXPECT().Calls(o).Return(&review.CallGraph{}, nil).Times(1)

	rec := get(t, srv, "/api/sessions/@5/overview?base=commits&commits="+selB+","+selA+","+selA)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Mode != gitfiles.ModeCommits || got.Head != selB || len(got.Files) != 2 {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
	for _, path := range []string{"architecture", "architecture", "calls", "calls"} {
		if rec := get(t, srv, "/api/sessions/@5/"+path+"?base=commits&commits="+selA+","+selB); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// Malformed selections are refused before git runs.
func TestSelectionRejectsMalformed(t *testing.T) {
	srv, _, _, _ := selectionFixture(t)
	tooMany := make([]string, gitfiles.MaxSelection+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%040x", i)
	}
	for name, query := range map[string]string{
		"no commits":         "base=commits",
		"empty commit":       "base=commits&commits=" + selA + ",",
		"commits for branch": "base=branch&commits=" + selA,
		"short id":           "base=commits&commits=abc1234",
		"ref":                "base=commits&commits=HEAD",
		"too many":           "base=commits&commits=" + strings.Join(tooMany, ","),
	} {
		for _, path := range []string{"overview", "architecture", "calls"} {
			if rec := get(t, srv, "/api/sessions/@5/"+path+"?"+query); rec.Code != http.StatusBadRequest {
				t.Errorf("%s %s: want 400, got %d", name, path, rec.Code)
			}
		}
	}
	if rec := get(t, srv, "/api/sessions/@6/overview?base=commits&commits="+selA); rec.Code != http.StatusNotFound {
		t.Errorf("ended window: want 404, got %d", rec.Code)
	}
}

// A selection that isn't consecutive is a 422 carrying the reason; commits
// that don't exist are a 404.
func TestSelectionErrors(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	gap := &gitfiles.SelectionError{Reason: "gap", Commit: selBase}
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA, selB}).Return(nil, gap).Times(1)
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA}).Return(nil, gitfiles.ErrUnknownCommit).Times(1)

	for i := 0; i < 2; i++ {
		rec := get(t, srv, "/api/sessions/@5/overview?base=commits&commits="+selA+","+selB)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusUnprocessableEntity || err != nil || body["error"] != gap.Error() {
			t.Errorf("gap: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := get(t, srv, "/api/sessions/@5/calls?base=commits&commits="+selA); rec.Code != http.StatusNotFound {
		t.Errorf("unknown commit: want 404, got %d", rec.Code)
	}
}

// Either side of a selected file comes from the resolved commits (the base
// side under a rename's old name), read-only, and only for files the
// selection changed.
func TestSelectionFile(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA, selB}).Return(&gitfiles.Selection{Root: "/ws/foo", Base: selBase, Head: selB}, nil)
	git.EXPECT().OverviewRange("/ws/foo", selBase, selB).Return(selectionOverview(), nil)
	git.EXPECT().ReadAt("/ws/foo", selB, "sub/new.go").Return(&gitfiles.Content{Path: "sub/new.go", Exists: true, Text: "new", Hash: "h1"}, nil)
	git.EXPECT().ReadAt("/ws/foo", selBase, "sub/old.go").Return(&gitfiles.Content{Path: "sub/old.go", Exists: true, Text: "old", Hash: "h2"}, nil)

	q := "base=commits&commits=" + selA + "," + selB
	for rev, want := range map[string]string{"sel-head": "new", "sel-base": "old"} {
		rec := get(t, srv, "/api/sessions/@5/file?path=sub/new.go&rev="+rev+"&"+q)
		var got fileResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Text != want || got.Path != "sub/new.go" || !got.ReadOnly {
			t.Errorf("%s: %d %s", rev, rec.Code, rec.Body)
		}
	}
	// Not in the selection: no read.
	if rec := get(t, srv, "/api/sessions/@5/file?path=.env&rev=sel-head&"+q); rec.Code != http.StatusNotFound {
		t.Errorf(".env: want 404, got %d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/@5/file?path=a.go&rev=sel-head&base=branch"); rec.Code != http.StatusBadRequest {
		t.Errorf("sel-head without commits: want 400, got %d", rec.Code)
	}
}

const selHead = "dddddddddddddddddddddddddddddddddddddddd"

func worktreeOverview(base string) *gitfiles.Overview {
	return &gitfiles.Overview{
		Root: "/ws/foo", Mode: gitfiles.ModeCommits, Rev: base, MergeBase: base, Worktree: true,
		Files: []gitfiles.OverviewFile{
			{Path: "a.go", Status: "M", Added: 1, Kind: gitfiles.KindLogic, Area: "."},
			{Path: "gone.go", Status: "D", Removed: 2, Kind: gitfiles.KindLogic, Area: "."},
		},
	}
}

// The uncommitted changes alone: from HEAD to the working tree, with no
// commits to resolve.
func TestSelectionWorktreeOnly(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	git.EXPECT().HeadCommit("/ws/foo/sub").Return("/ws/foo", selHead, nil).Times(1)
	git.EXPECT().OverviewWorktree("/ws/foo", selHead).Return(worktreeOverview(selHead), nil).Times(1)

	for i := 0; i < 2; i++ {
		rec := get(t, srv, "/api/sessions/@5/overview?base=commits&worktree=1")
		var got overviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || !got.Worktree || got.Rev != selHead {
			t.Fatalf("overview: %d %s", rec.Code, rec.Body)
		}
	}
}

// Commits plus the uncommitted changes: the commits must end at HEAD.
func TestSelectionWorktreeWithCommits(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	git.EXPECT().HeadCommit("/ws/foo/sub").Return("/ws/foo", selB, nil)
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA, selB}).Return(&gitfiles.Selection{Root: "/ws/foo", Base: selBase, Head: selB}, nil)
	git.EXPECT().OverviewWorktree("/ws/foo", selBase).Return(worktreeOverview(selBase), nil)
	rec := get(t, srv, "/api/sessions/@5/overview?base=commits&worktree=1&commits="+selB+","+selA)
	var got overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Rev != selBase || !got.Worktree {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}

	// The same commits while HEAD is elsewhere: refused, naming both.
	srv, git, _, _ = selectionFixture(t)
	git.EXPECT().HeadCommit("/ws/foo/sub").Return("/ws/foo", selHead, nil)
	git.EXPECT().ResolveSelection("/ws/foo/sub", []string{selA, selB}).Return(&gitfiles.Selection{Root: "/ws/foo", Base: selBase, Head: selB}, nil)
	rec = get(t, srv, "/api/sessions/@5/architecture?base=commits&worktree=1&commits="+selA+","+selB)
	want := (&gitfiles.SelectionError{Reason: "worktree", Commit: selB, Other: selHead}).Error()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
		t.Errorf("not at HEAD: %d %s", rec.Code, rec.Body)
	}
}

func TestSelectionWorktreeRejects(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	for name, query := range map[string]string{
		"worktree for branch":   "base=branch&worktree=1",
		"worktree for head":     "base=head&worktree=1",
		"worktree=yes":          "base=commits&worktree=yes",
		"neither":               "base=commits",
		"worktree, bad commits": "base=commits&worktree=1&commits=HEAD",
	} {
		if rec := get(t, srv, "/api/sessions/@5/overview?"+query); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
	git.EXPECT().HeadCommit("/ws/foo/sub").Return("", "", gitfiles.ErrNoCommits)
	if rec := get(t, srv, "/api/sessions/@5/calls?base=commits&worktree=1"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no commits: want 422, got %d", rec.Code)
	}
}

// A diff against a worktree selection's base: the base side from the
// commit, the working-tree side as the plain file, which may also be a
// file only the selection lists.
func TestSelectionWorktreeFile(t *testing.T) {
	srv, git, _, _ := selectionFixture(t)
	git.EXPECT().HeadCommit("/ws/foo/sub").Return("/ws/foo", selHead, nil).AnyTimes()
	git.EXPECT().OverviewWorktree("/ws/foo", selHead).Return(worktreeOverview(selHead), nil).AnyTimes()
	git.EXPECT().Tree("/ws/foo/sub").Return("/ws/foo", []string{"a.go"}, nil).AnyTimes()
	git.EXPECT().Changes("/ws/foo/sub").Return(&gitfiles.Changes{Root: "/ws/foo"}, nil).AnyTimes()
	git.EXPECT().Overview("/ws/foo/sub", gitfiles.ModeBranch).Return(&gitfiles.Overview{Root: "/ws/foo"}, nil).AnyTimes()
	q := "base=commits&worktree=1"

	git.EXPECT().ReadAt("/ws/foo", selHead, "gone.go").Return(&gitfiles.Content{Path: "gone.go", Exists: true, Text: "old", Hash: "h"}, nil)
	rec := get(t, srv, "/api/sessions/@5/file?path=gone.go&rev=sel-base&"+q)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"old"`) {
		t.Errorf("sel-base: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, "/api/sessions/@5/file?path=a.go&rev=sel-head&"+q); rec.Code != http.StatusBadRequest {
		t.Errorf("sel-head on a worktree selection: want 400, got %d", rec.Code)
	}

	git.EXPECT().ReadFile("/ws/foo", "a.go").Return(&gitfiles.Content{Path: "a.go", Exists: true, Text: "new", Hash: "h1"}, nil)
	git.EXPECT().ReadFile("/ws/foo", "gone.go").Return(&gitfiles.Content{Path: "gone.go"}, nil)
	for _, path := range []string{"a.go", "gone.go"} {
		rec := get(t, srv, "/api/sessions/@5/file?path="+path+"&"+q)
		var got fileResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.ReadOnly {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := get(t, srv, "/api/sessions/@5/file?path=.env&"+q); rec.Code != http.StatusNotFound {
		t.Errorf(".env: want 404, got %d", rec.Code)
	}
}
