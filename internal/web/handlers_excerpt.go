package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// excerptQuery is what an excerpt request asks for: lines line..end of
// path, on side (gitfiles.SideNew or SideOld), with ctx rows around them.
type excerptQuery struct {
	path       string
	line, end  int
	side       string
	contextLen int
}

// Bounds on an excerpt request's numbers.
const (
	defaultExcerptContext = 3
	maxExcerptContext     = 20
)

// parseExcerpt reads ?path=&line=[&end=][&side=new|old][&ctx=].
func parseExcerpt(r *http.Request) (excerptQuery, error) {
	v := r.URL.Query()
	q := excerptQuery{path: v.Get("path"), side: v.Get("side"), contextLen: defaultExcerptContext}
	if q.path == "" {
		return q, fmt.Errorf("missing path")
	}
	num := func(name string, def, lo, hi int) (int, error) {
		s := v.Get(name)
		if s == "" {
			return def, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("bad %s %q", name, s)
		}
		return n, nil
	}
	var err error
	if v.Get("line") == "" {
		return q, fmt.Errorf("missing line")
	}
	if q.line, err = num("line", 0, 1, 1<<30); err != nil {
		return q, err
	}
	if q.end, err = num("end", q.line, q.line, q.line+gitfiles.MaxExcerptRows); err != nil {
		return q, err
	}
	if q.contextLen, err = num("ctx", defaultExcerptContext, 0, maxExcerptContext); err != nil {
		return q, err
	}
	switch q.side {
	case "":
		q.side = gitfiles.SideNew
	case gitfiles.SideNew, gitfiles.SideOld:
	default:
		return q, fmt.Errorf("bad side %q", q.side)
	}
	return q, nil
}

// handleExcerpt serves a few lines of one file of a live session's change,
// as the change shows them (+/− rows), for the Overview's inspector: a
// call's line, a Peek, a function's first change. It takes the same
// ?base= modes as /overview, and the path must be one the change lists or
// one in the checkout's tree (a caller in an unchanged file).
func (s *Server) handleExcerpt(w http.ResponseWriter, r *http.Request) {
	eq, err := parseExcerpt(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dir, q, ok := s.sessionChange(w, r)
	if !ok {
		return
	}
	o, err := s.change(dir, q)
	if err != nil {
		writeError(w, changeStatus(err), err)
		return
	}
	s.serveExcerpt(w, r, o, eq, func() (int, error) {
		_, status, err := s.listedPath(dir, eq.path)
		return status, err
	})
}

// handleBranchExcerpt is handleExcerpt for the reviewer view: a checkout
// target lists its tree like a session, a ref target its head's tree.
func (s *Server) handleBranchExcerpt(w http.ResponseWriter, r *http.Request) {
	eq, err := parseExcerpt(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
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
	if err != nil {
		writeError(w, changeStatus(err), err)
		return
	}
	s.serveExcerpt(w, r, o, eq, func() (int, error) {
		if t.dir != "" {
			_, status, err := s.listedPath(t.dir, eq.path)
			return status, err
		}
		tree, err := s.refTree(t)
		if err != nil {
			return http.StatusBadGateway, err
		}
		if _, found := slices.BinarySearch(tree, eq.path); !found {
			return http.StatusNotFound, fmt.Errorf("%s is not a file in branch %s", eq.path, t.branch)
		}
		return 0, nil
	})
}

// serveExcerpt answers with eq's cut of path in o. A path o doesn't list
// (by its path or a rename's old path) must pass inTree. The annotated file
// is cached by o's revisions; a working-tree version also by its stamp.
func (s *Server) serveExcerpt(w http.ResponseWriter, r *http.Request, o *gitfiles.Overview, eq excerptQuery, inTree func() (int, error)) {
	listed := slices.ContainsFunc(o.Files, func(f gitfiles.OverviewFile) bool { return f.Path == eq.path || f.OldPath == eq.path })
	if !listed {
		if status, err := inTree(); err != nil {
			writeError(w, status, err)
			return
		}
	}
	key := o.Root + "\x00" + o.Rev + "\x00" + o.Head + "\x00" + eq.path
	if o.Head == "" {
		key += "\x00" + gitfiles.Stamp(o.Root, eq.path)
	}
	v, err := s.excerptCache.get(key, func() (any, error) { return s.deps.Git.Annotate(o, eq.path) })
	switch {
	case errors.Is(err, gitfiles.ErrOutsideRoot):
		writeError(w, http.StatusForbidden, err)
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
		return
	}
	body, err := json.Marshal(v.(*gitfiles.Annotated).Cut(eq.line, eq.end, eq.side, eq.contextLen))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeHashed(w, r, body)
}
