package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/review"
)

// The reviewer view (/branch?project=&branch= or /branch?project=&pr=)
// shows the Overview for any branch or pull request of a project, without a
// live session. Its API lives under /api/projects/{name}/branches/{branch}
// or /api/projects/{name}/pulls/{pr}. The server resolves either to a
// target:
//
//   - a checkout (the main checkout or a worktree has the branch checked
//     out): the overview covers uncommitted changes too, and files can be
//     edited, as in the chat view;
//   - a ref (nobody has it checked out): a commit, read-only. For a branch,
//     its local head (or origin's, fetched, if there's no local branch);
//     for a PR, GitHub's refs/pull/<n>/head, fetched, compared with the
//     PR's own base branch.

// branchTarget is a resolved reviewer-view branch.
type branchTarget struct {
	project, branch string
	projectPath     string
	dir             string // the checkout; "" for a ref target
	head            string // a ref target's commit
	base            string // what a ref target is compared with ("" = default branch)
	pr              int
	prTitle, prURL  string
}

// targetView tells the page what it's looking at.
type targetView struct {
	Kind    string `json:"kind"` // "checkout" or "ref"
	Branch  string `json:"branch"`
	Path    string `json:"path,omitempty"`
	Head    string `json:"head,omitempty"`
	PR      int    `json:"pr,omitempty"`
	PRTitle string `json:"prTitle,omitempty"`
	PRURL   string `json:"prURL,omitempty"`
	// SessionWindow is a live session in the checkout, if any: the page
	// links to its chat, and review comments can be sent to it.
	SessionWindow string `json:"sessionWindow,omitempty"`
}

// refKey caches a ref target's overview and analysis: the same commit can
// be two branches, and be compared with two bases.
func (t *branchTarget) refKey() string {
	return t.projectPath + "\x00at\x00" + t.branch + "\x00" + t.head + "\x00" + t.base
}

// lockKey identifies the branch for one-at-a-time scope checks.
func (t *branchTarget) lockKey() string { return "branch\x00" + t.project + "\x00" + t.branch }

// resolveBranchTarget resolves the request's {name} and {branch} or {pr},
// or returns the status and error to answer with.
func (s *Server) resolveBranchTarget(r *http.Request) (*branchTarget, int, error) {
	t := &branchTarget{project: r.PathValue("name"), branch: r.PathValue("branch")}
	pp, ok := s.findProjectPath(t.project)
	if !ok {
		return nil, http.StatusNotFound, fmt.Errorf("project %q not found", t.project)
	}
	t.projectPath = pp
	if pr := r.PathValue("pr"); pr != "" {
		n, err := strconv.Atoi(pr)
		if err != nil || n < 1 {
			return nil, http.StatusBadRequest, fmt.Errorf("bad pr %q", pr)
		}
		v, err := s.prCache.get(fmt.Sprintf("detail:%s:%d", pp, n), func() (any, error) { return s.deps.PRs.GetPRDetail(pp, n) })
		if err != nil {
			return nil, http.StatusBadGateway, err
		}
		d := v.(*github.PRDetail)
		t.pr, t.branch, t.prTitle, t.prURL = n, d.Branch, d.Title, d.URL
		if d.BaseBranch != "" {
			t.base = "origin/" + d.BaseBranch
		}
	}
	// The page polls; the branch list only needs to be a few seconds fresh.
	v, err := s.localRefCache.get("branches\x00"+pp, func() (any, error) { return s.deps.Worktrees.ListBranches(pp) })
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	var b *project.Branch
	for _, br := range v.([]project.Branch) {
		if br.Name == t.branch {
			b = &br
			break
		}
	}
	if b != nil {
		if dir := checkoutPath(pp, *b); dir != "" {
			t.dir = dir
			return t, 0, nil
		}
	}
	// Not checked out: a local branch's head, or a PR's or remote branch's
	// fetched from origin (at most a few minutes old).
	remote := t.pr > 0 || b == nil
	cache, key := s.localRefCache, pp+"\x00"+t.branch
	resolve := func() (any, error) { return s.deps.Git.ResolveBranch(pp, t.branch, remote) }
	if remote {
		cache = s.fetchCache
		// The page asks for several endpoints at once: one fetch, the rest
		// wait for its cached answer (two fetches would fight over the ref).
		s.fetchMu.Lock()
		defer s.fetchMu.Unlock()
	}
	if t.pr > 0 {
		key = fmt.Sprintf("%s\x00pr\x00%d", pp, t.pr)
		base := strings.TrimPrefix(t.base, "origin/")
		resolve = func() (any, error) { return s.deps.Git.ResolvePR(pp, t.pr, base) }
	}
	v, err = cache.get(key, resolve)
	switch {
	case errors.Is(err, gitfiles.ErrBadBranch):
		return nil, http.StatusBadRequest, err
	case errors.Is(err, gitfiles.ErrNoBranch):
		return nil, http.StatusNotFound, fmt.Errorf("branch %q not found: %w", t.branch, err)
	case err != nil:
		return nil, http.StatusBadGateway, err
	}
	t.head = v.(string)
	return t, 0, nil
}

// view describes the target for the page.
func (s *Server) view(t *branchTarget) *targetView {
	v := &targetView{Kind: "ref", Branch: t.branch, Head: t.head, PR: t.pr, PRTitle: t.prTitle, PRURL: t.prURL}
	if t.dir == "" {
		return v
	}
	v.Kind, v.Path, v.Head = "checkout", t.dir, ""
	if st, err := s.deps.State.Read(); err == nil {
		for _, p := range st.Projects {
			if p.WindowID != "" && p.SessionID != "" && p.Status != "none" && underDir(p.Path, t.dir) {
				v.SessionWindow = p.WindowID
				break
			}
		}
	}
	return v
}

func underDir(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// targetOverview is the target's overview: a checkout's in mode, a ref's
// from its merge base to its head (mode doesn't apply).
func (s *Server) targetOverview(t *branchTarget, mode string) (*gitfiles.Overview, error) {
	if t.dir != "" {
		return s.overview(t.dir, mode)
	}
	v, err := s.overviewCache.get(t.refKey(), func() (any, error) {
		return s.deps.Git.OverviewAt(t.projectPath, t.branch, t.head, t.base)
	})
	if err != nil {
		return nil, err
	}
	return v.(*gitfiles.Overview), nil
}

// branchMode reads ?base= like the session endpoints.
func branchMode(w http.ResponseWriter, r *http.Request) (string, bool) {
	mode := r.URL.Query().Get("base")
	if mode == "" {
		mode = gitfiles.ModeBranch
	}
	if mode != gitfiles.ModeBranch && mode != gitfiles.ModeHead {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown base %q", mode))
		return "", false
	}
	return mode, true
}

func (s *Server) handleBranchOverview(w http.ResponseWriter, r *http.Request) {
	mode, ok := branchMode(w, r)
	if !ok {
		return
	}
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	o, err := s.targetOverview(t, mode)
	writeOverview(w, r, o, err, s.view(t))
}

func (s *Server) handleBranchArchitecture(w http.ResponseWriter, r *http.Request) {
	mode, ok := branchMode(w, r)
	if !ok {
		return
	}
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	key := t.dir + "\x00" + mode
	if t.dir == "" {
		key = t.refKey()
	}
	s.serveArchitecture(w, r, key, func() (*gitfiles.Overview, error) { return s.targetOverview(t, mode) })
}

// handleBranchFile serves one file for the reviewer view's editor tabs. A
// checkout target follows the session rules (handleSessionFile). A ref
// target serves files of its head commit (read-only) or, with rev=base, of
// its merge base, and only paths in the head's tree or the change.
func (s *Server) handleBranchFile(w http.ResponseWriter, r *http.Request) {
	path, rev := r.URL.Query().Get("path"), r.URL.Query().Get("rev")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing path"))
		return
	}
	if rev != "" && rev != "HEAD" && rev != "base" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported rev %q", rev))
		return
	}
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if t.dir != "" {
		s.serveFile(w, r, t.dir, path, rev)
		return
	}

	o, err := s.targetOverview(t, gitfiles.ModeBranch)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	var changed *gitfiles.OverviewFile
	for i := range o.Files {
		if o.Files[i].Path == path {
			changed = &o.Files[i]
		}
	}
	if changed == nil {
		tree, err := s.refTree(t)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		if _, found := slices.BinarySearch(tree, path); !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("%s is not a file in branch %s", path, t.branch))
			return
		}
	}
	var c *gitfiles.Content
	switch {
	case rev == "base" && o.Rev != "":
		old := path
		if changed != nil && changed.OldPath != "" {
			old = changed.OldPath
		}
		c, err = s.deps.Git.ReadAt(t.projectPath, o.Rev, old)
		if c != nil {
			c.Path = path
		}
	case rev == "base":
		c = &gitfiles.Content{Path: path} // no merge base: nothing to compare with
	default:
		c, err = s.deps.Git.ReadAt(t.projectPath, t.head, path)
	}
	if errors.Is(err, gitfiles.ErrOutsideRoot) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeContent(w, r, c, true)
}

// refTree is a ref target's file list (cached by commit).
func (s *Server) refTree(t *branchTarget) ([]string, error) {
	v, err := s.treeCache.get(t.projectPath+"\x00at\x00"+t.head, func() (any, error) {
		return s.deps.Git.TreeAt(t.projectPath, t.head)
	})
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}

func (s *Server) handleBranchSaveFile(w http.ResponseWriter, r *http.Request) {
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if t.dir == "" {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("branch %s isn't checked out: its files are read-only here", t.branch))
		return
	}
	s.saveFile(w, r, t.dir)
}

func (s *Server) handleBranchTree(w http.ResponseWriter, r *http.Request) {
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if t.dir != "" {
		s.serveTree(w, t.dir)
		return
	}
	paths, err := s.refTree(t)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, treeResponse{Repo: true, Root: t.projectPath, Paths: paths})
}

// handleBranchScope runs the scope check for a branch. There's no
// transcript, so the browser sends every file as changed outside a
// conversation; for a PR, its title and description (fetched here) are
// part of the ask.
func (s *Server) handleBranchScope(w http.ResponseWriter, r *http.Request) {
	mode, ok := branchMode(w, r)
	if !ok {
		return
	}
	var body scopeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScopeBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	t, status, err := s.resolveBranchTarget(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	o, err := s.targetOverview(t, mode)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	var extra scopeContext
	if t.pr > 0 {
		key := fmt.Sprintf("detail:%s:%d", t.projectPath, t.pr)
		if v, err := s.prCache.get(key, func() (any, error) { return s.deps.PRs.GetPRDetail(t.projectPath, t.pr) }); err == nil {
			d := v.(*github.PRDetail)
			extra.pr = &review.ScopeTicket{ID: strconv.Itoa(t.pr), Title: d.Title, Description: truncateRunes(d.Body, maxTicketText)}
		}
	}
	s.runScope(w, r, t.lockKey(), o, body, extra)
}
