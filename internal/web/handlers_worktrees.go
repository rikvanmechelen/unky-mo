package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
)

type cleanupRequest struct {
	Branch       string `json:"branch"`
	DeleteBranch bool   `json:"delete_branch"`
	// StopSessions confirms stopping sessions live in the worktree — the
	// TUI cleanup's kill stage. Without it, live sessions get a 409.
	StopSessions bool `json:"stop_sessions"`
}

type cleanupConflict struct {
	Error string `json:"error"`
	Live  int    `json:"live"`
}

// handleCleanup removes a branch's worktree and/or deletes the branch — the
// TUI's `x` on a branch row. Refuses the main checkout.
func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}
	var req cleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	s.launchMu.Lock()
	defer s.launchMu.Unlock()

	b, err := s.findBranch(path, req.Branch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	switch {
	case b == nil:
		writeError(w, http.StatusNotFound, fmt.Errorf("no branch %q", req.Branch))
		return
	case b.IsMain:
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is the main checkout; it can't be removed", b.Name))
		return
	case b.WorktreePath == "" && !req.DeleteBranch:
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s has no worktree; only deleting the branch is possible", b.Name))
		return
	}

	var live []claude.Session
	if b.WorktreePath != "" {
		all, err := s.deps.Sessions.LiveSessions()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, sess := range all {
			if sess.CWD == b.WorktreePath {
				live = append(live, sess)
			}
		}
	}
	if len(live) > 0 && !req.StopSessions {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(cleanupConflict{
			Error: fmt.Sprintf("%d session(s) are running in this worktree", len(live)),
			Live:  len(live),
		})
		return
	}

	res, err := s.deps.Sessions.CleanupWorktree(ops.CleanupParams{
		ProjectPath:  path,
		Branch:       b.Name,
		DeleteBranch: req.DeleteBranch,
		Sessions:     live,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"status": res.Status})
}

type liftRequest struct {
	Branch    string `json:"branch"` // checkout the session lives in now
	SessionID string `json:"session_id"`
	NewBranch string `json:"new_branch"`
	// Dirty decides what happens to uncommitted changes at the source:
	// "stash" carries them into the new worktree, "leave" keeps them where
	// they are. Required when the source is dirty (else 409).
	Dirty string `json:"dirty"`
}

// handleLift moves a session (live or past) into a new branch + worktree off
// its checkout's HEAD — the TUI's `w` on a session row. Like the TUI it
// doesn't launch anything; the session shows up under the new worktree,
// ready to resume.
func (s *Server) handleLift(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}
	var req liftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if !sessionIDPattern.MatchString(req.SessionID) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid session id"))
		return
	}
	if err := validateBranchName(req.NewBranch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch req.Dirty {
	case "", "stash", "leave":
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown dirty mode %q", req.Dirty))
		return
	}

	s.launchMu.Lock()
	defer s.launchMu.Unlock()

	b, err := s.findBranch(path, req.Branch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if b == nil || checkoutPath(path, *b) == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("branch %q has no checkout", req.Branch))
		return
	}
	cwd := checkoutPath(path, *b)
	if !s.deps.History.TranscriptExists(cwd, req.SessionID) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no session %s in this checkout", req.SessionID))
		return
	}

	if req.Dirty == "" {
		dirty, err := s.deps.Sessions.IsDirty(cwd)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if dirty {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "the checkout has uncommitted changes",
				"dirty": true,
			})
			return
		}
	}

	// A live session is stopped as part of the move; find its window + PID.
	params := ops.LiftParams{
		ProjectName: name,
		SourcePath:  cwd,
		SessionID:   req.SessionID,
		NewBranch:   req.NewBranch,
		StashAndPop: req.Dirty == "stash",
	}
	for _, row := range liveRowsAt(s.readState(), cwd) {
		if row.SessionID == req.SessionID {
			params.SourceWindow = row.WindowName
			params.SourcePID = s.livePID(row.SessionID)
		}
	}

	res, err := s.deps.Sessions.LiftSessionToWorktree(params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{
		"status":          res.Status,
		"branch":          req.NewBranch,
		"stash_pop_error": res.StashPopErr,
	})
}

// findBranch looks up a local branch of the project by name (nil if absent).
func (s *Server) findBranch(projectPath, name string) (*project.Branch, error) {
	branches, err := s.deps.Worktrees.ListBranches(projectPath)
	if err != nil {
		return nil, err
	}
	for i := range branches {
		if branches[i].Name == name {
			return &branches[i], nil
		}
	}
	return nil, nil
}
