package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
)

// seedJSONL writes a dummy JSONL file for sessionID under encodedDir and
// returns the full path. Helper used by the lift tests.
func seedJSONL(t *testing.T, encodedDir, sessionID string) string {
	t.Helper()
	if err := os.MkdirAll(encodedDir, 0755); err != nil {
		t.Fatalf("mkdir encoded dir: %v", err)
	}
	p := filepath.Join(encodedDir, sessionID+".jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"system","content":"seed"}`+"\n"), 0644); err != nil {
		t.Fatalf("write seed jsonl: %v", err)
	}
	return p
}

// liftDirs returns a pair of per-test encoded-cwd directories (source, dest)
// rooted at t.TempDir(). Mocking ProjectsDirForPath to return these keeps the
// suite hermetic (no touches to ~/.claude).
func liftDirs(t *testing.T) (srcDir, dstDir string) {
	t.Helper()
	base := t.TempDir()
	srcDir = filepath.Join(base, "src-encoded")
	dstDir = filepath.Join(base, "dst-encoded")
	return
}

// Live + dirty source + StashAndPop=true: carry.txt moves to new worktree,
// JSONL moves from source encoded dir → new encoded dir, old tmux window dies.
func TestLiftSessionToWorktreeLiveDirtyStash(t *testing.T) {
	repo := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "carry.txt"), []byte("lift me"), 0644); err != nil {
		t.Fatal(err)
	}
	srcEnc, dstEnc := liftDirs(t)
	seedJSONL(t, srcEnc, "sess-abc")

	ctx, tmux, cl := newTestContext(t)
	cl.EXPECT().IsAlive(4242).Return(false).AnyTimes()
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().KillWindow("mo:alpha").Return(nil)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	// CreateNewBranchWorktree derives the path at runtime; we match against
	// "anything that ends with .worktrees/feat-x" via gomock.Any() and verify
	// the moved file on disk below.
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName:  "alpha",
		SourcePath:   repo,
		SessionID:    "sess-abc",
		SourcePID:    4242,
		SourceWindow: "alpha",
		NewBranch:    "feat-x",
		StashAndPop:  true,
	})
	if err != nil {
		t.Fatalf("LiftSessionToWorktree: %v", err)
	}
	if !res.Stashed {
		t.Error("Stashed should be true on dirty source with StashAndPop")
	}
	if !res.MovedJSONL {
		t.Error("MovedJSONL should be true when a seeded JSONL was present")
	}
	if _, err := os.Stat(filepath.Join(srcEnc, "sess-abc.jsonl")); !os.IsNotExist(err) {
		t.Errorf("source JSONL should be gone, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dstEnc, "sess-abc.jsonl")); err != nil {
		t.Errorf("dest JSONL should exist, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(res.NewWorktreePath, "carry.txt")); err != nil {
		t.Errorf("carry.txt should have popped into new worktree, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "carry.txt")); err == nil {
		t.Error("carry.txt should NOT remain in source after stash+pop")
	}
}

// Live + clean source: JSONL moves, old window dies, no stash.
func TestLiftSessionToWorktreeLiveClean(t *testing.T) {
	repo := newGitRepo(t)
	srcEnc, dstEnc := liftDirs(t)
	seedJSONL(t, srcEnc, "sess-xyz")

	ctx, tmux, cl := newTestContext(t)
	cl.EXPECT().IsAlive(1234).Return(false).AnyTimes()
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().KillWindow("mo:alpha").Return(nil)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName:  "alpha",
		SourcePath:   repo,
		SessionID:    "sess-xyz",
		SourcePID:    1234,
		SourceWindow: "alpha",
		NewBranch:    "feat-y",
		StashAndPop:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stashed {
		t.Error("Stashed should be false on a clean lift")
	}
	if !res.MovedJSONL {
		t.Error("MovedJSONL should be true")
	}
	if _, err := os.Stat(filepath.Join(dstEnc, "sess-xyz.jsonl")); err != nil {
		t.Errorf("dest JSONL should exist, err=%v", err)
	}
}

// Dirty source + StashAndPop=false: dirty files stay in source, JSONL still moves.
func TestLiftSessionToWorktreeLiveDirtyLeave(t *testing.T) {
	repo := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("stay"), 0644); err != nil {
		t.Fatal(err)
	}
	srcEnc, dstEnc := liftDirs(t)
	seedJSONL(t, srcEnc, "ss")

	ctx, tmux, cl := newTestContext(t)
	cl.EXPECT().IsAlive(999).Return(false).AnyTimes()
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().KillWindow(gomock.Any()).Return(nil)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName:  "alpha",
		SourcePath:   repo,
		SessionID:    "ss",
		SourcePID:    999,
		SourceWindow: "alpha",
		NewBranch:    "feat-z",
		StashAndPop:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stashed {
		t.Error("Stashed must be false when StashAndPop=false")
	}
	if _, err := os.Stat(filepath.Join(repo, "keep.txt")); err != nil {
		t.Errorf("keep.txt should remain in source, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.NewWorktreePath, "keep.txt")); err == nil {
		t.Error("keep.txt should NOT appear in new worktree")
	}
	if _, err := os.Stat(filepath.Join(dstEnc, "ss.jsonl")); err != nil {
		t.Errorf("dest JSONL should exist, err=%v", err)
	}
}

// Historical session (PID=0, no SourceWindow): no SIGTERM, no kill-window,
// just the JSONL move. Tmux mock gets no calls at all.
func TestLiftSessionToWorktreeHistorical(t *testing.T) {
	repo := newGitRepo(t)
	srcEnc, dstEnc := liftDirs(t)
	seedJSONL(t, srcEnc, "old-id")

	ctx, _, cl := newTestContext(t)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)
	// No IsAlive, no KillWindow, no SessionName — historical path.

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName: "alpha",
		SourcePath:  repo,
		SessionID:   "old-id",
		SourcePID:   0,
		NewBranch:   "hist",
		StashAndPop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.MovedJSONL {
		t.Error("MovedJSONL should be true for historical lift with seeded JSONL")
	}
	if _, err := os.Stat(filepath.Join(dstEnc, "old-id.jsonl")); err != nil {
		t.Errorf("dest JSONL should exist, err=%v", err)
	}
}

// Missing JSONL at source: lift succeeds but MovedJSONL=false. Covers the
// edge case where a row exists without an on-disk JSONL (e.g. synced from
// another machine and not yet pulled).
func TestLiftSessionToWorktreeMissingJSONLIsLoggedNotFatal(t *testing.T) {
	repo := newGitRepo(t)
	srcEnc, dstEnc := liftDirs(t)
	// Intentionally do NOT seed a JSONL at srcEnc.

	ctx, _, cl := newTestContext(t)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName: "alpha",
		SourcePath:  repo,
		SessionID:   "ghost",
		SourcePID:   0,
		NewBranch:   "no-jsonl",
		StashAndPop: false,
	})
	if err != nil {
		t.Fatalf("missing JSONL should be non-fatal, got err=%v", err)
	}
	if res.MovedJSONL {
		t.Error("MovedJSONL should be false when no JSONL existed at source")
	}
}

// Existing branch name → error, no tmux calls, no JSONL touched.
func TestLiftSessionToWorktreeBranchAlreadyExists(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "branch", "already-here")

	ctx, _, _ := newTestContext(t)
	// No ProjectsDirForPath expectations — branch-exists check fires first.

	_, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName: "alpha",
		SourcePath:  repo,
		SessionID:   "sess",
		SourcePID:   0,
		NewBranch:   "already-here",
		StashAndPop: false,
	})
	if err == nil {
		t.Fatal("expected error when branch already exists")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "already") &&
		!strings.Contains(strings.ToLower(err.Error()), "exists") {
		t.Errorf("error should mention existing branch, got %q", err)
	}
}

func TestLiftSessionToWorktreeValidatesInputs(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	cases := []LiftParams{
		{ProjectName: "", SourcePath: "/tmp", SessionID: "s", NewBranch: "b"},
		{ProjectName: "a", SourcePath: "", SessionID: "s", NewBranch: "b"},
		{ProjectName: "a", SourcePath: "/tmp", SessionID: "", NewBranch: "b"},
		{ProjectName: "a", SourcePath: "/tmp", SessionID: "s", NewBranch: ""},
	}
	for i, p := range cases {
		if _, err := LiftSessionToWorktree(ctx, p); err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, p)
		}
	}
}

func TestLiftSessionToWorktreeNilContext(t *testing.T) {
	_, err := LiftSessionToWorktree(nil, LiftParams{
		ProjectName: "a", SourcePath: "/tmp", SessionID: "s", NewBranch: "b",
	})
	if err == nil {
		t.Error("nil ctx should error")
	}
}

// Happy-path clean lift: StashPopErr must default to empty (regression guard
// against accidentally conflating "no stash requested" with "pop failed").
func TestLiftSessionToWorktreeStashPopErrDefaultsEmpty(t *testing.T) {
	repo := newGitRepo(t)
	srcEnc, dstEnc := liftDirs(t)
	seedJSONL(t, srcEnc, "s")

	ctx, _, cl := newTestContext(t)
	cl.EXPECT().ProjectsDirForPath(repo).Return(srcEnc)
	cl.EXPECT().ProjectsDirForPath(gomock.Any()).Return(dstEnc)

	res, err := LiftSessionToWorktree(ctx, LiftParams{
		ProjectName: "alpha",
		SourcePath:  repo,
		SessionID:   "s",
		SourcePID:   0,
		NewBranch:   "clean-lift",
		StashAndPop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StashPopErr != "" {
		t.Errorf("StashPopErr should default to empty, got %q", res.StashPopErr)
	}
}
