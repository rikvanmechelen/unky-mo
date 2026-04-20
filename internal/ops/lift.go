package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/project"
)

// LiftParams drives LiftSessionToWorktree.
type LiftParams struct {
	ProjectName  string // used only for status-message text
	SourcePath   string // cwd of the session being lifted; git HEAD for the new worktree is read from here
	SessionID    string // claude session ID — its JSONL is moved to the new worktree's encoded-cwd dir
	SourcePID    int    // 0 when the source session is historical (no live process)
	SourceWindow string // tmux window name to kill after SIGTERM; "" skips the kill
	NewBranch    string // branch to create for the new worktree; must not already exist
	StashAndPop  bool   // when true, stash at SourcePath, create worktree, pop in new worktree
}

// LiftResult reports what happened.
type LiftResult struct {
	NewWorktreePath string
	Status          string
	Stashed         bool
	// StashPopErr is populated when a stash+pop actually produced conflicts.
	// The move still happens; callers surface the error to the user.
	StashPopErr string
	// MovedJSONL is true when the session's JSONL was found at source and
	// renamed into the new encoded-cwd dir. False when no JSONL existed on
	// this machine (e.g. historical row synced from elsewhere and not pulled).
	MovedJSONL bool
}

// LiftSessionToWorktree creates a new branch + worktree off the source's
// HEAD, carries optional dirty state via stash+pop, terminates any live
// claude at the source, and *moves* the session's JSONL file to the new
// worktree's encoded-cwd dir so `buildDetailRows` attributes the session to
// the new branch. Does NOT launch a new claude — the user presses `enter` on
// the relocated `br-session` row to resume manually (which reuses the known-
// good `resumeInDir` path).
//
// Ordering matters: SIGTERM before the JSONL move so claude releases its
// file handle; kill-window after SIGTERM for a clean tmux teardown.
func LiftSessionToWorktree(ctx *Context, p LiftParams) (*LiftResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.ProjectName == "" || p.SourcePath == "" || p.SessionID == "" || p.NewBranch == "" {
		return nil, fmt.Errorf("ops.LiftSessionToWorktree: ProjectName, SourcePath, SessionID, NewBranch all required")
	}

	res := &LiftResult{}

	// Step 1: optional stash at source. Silent no-op when clean; real error bubbles.
	if p.StashAndPop {
		dirty, err := project.IsDirty(p.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("check source dirty: %w", err)
		}
		if dirty {
			if err := project.Stash(p.SourcePath, "unky-mo: lifting into "+p.NewBranch); err != nil {
				return nil, fmt.Errorf("stash source: %w", err)
			}
			res.Stashed = true
		}
	}

	// Step 2: create the new worktree off source's HEAD with a fresh branch.
	wtPath, err := project.CreateNewBranchWorktree(p.SourcePath, p.NewBranch)
	if err != nil {
		// Restore the source's working tree before returning.
		if res.Stashed {
			_ = project.StashPop(p.SourcePath)
		}
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	res.NewWorktreePath = wtPath

	// Step 3: pop stash into the new worktree. Conflicts recorded but non-fatal.
	if res.Stashed {
		if err := project.StashPop(wtPath); err != nil {
			res.StashPopErr = err.Error()
		}
	}

	// Step 4: SIGTERM the live source + wait for exit so the JSONL file
	// handle is released before we rename it.
	if p.SourcePID > 0 {
		if proc, err := os.FindProcess(p.SourcePID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
		isAlive := func(pid int) bool { return false }
		if ctx.Claude != nil {
			isAlive = ctx.Claude.IsAlive
		}
		for i := 0; i < 20; i++ {
			if !isAlive(p.SourcePID) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Step 5: kill the old tmux window so the sidebar dies with it.
	if p.SourceWindow != "" {
		_ = ctx.Tmux.KillWindow(ctx.Tmux.SessionName() + ":" + p.SourceWindow)
	}

	// Step 6: move the JSONL. Missing source file is a silent non-fatal.
	if ctx.Claude != nil {
		oldDir := ctx.Claude.ProjectsDirForPath(p.SourcePath)
		newDir := ctx.Claude.ProjectsDirForPath(wtPath)
		oldPath := filepath.Join(oldDir, p.SessionID+".jsonl")
		newPath := filepath.Join(newDir, p.SessionID+".jsonl")
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.MkdirAll(newDir, 0755); err != nil {
				return res, fmt.Errorf("mkdir new session dir: %w", err)
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				return res, fmt.Errorf("move session jsonl: %w", err)
			}
			res.MovedJSONL = true
		}
	}

	res.Status = fmt.Sprintf("Session moved to %s@%s — press enter to resume", p.ProjectName, p.NewBranch)
	if res.StashPopErr != "" {
		res.Status += " (stash pop: " + res.StashPopErr + ")"
	}
	return res, nil
}
