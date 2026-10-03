package web

import (
	"net/http"

	"github.com/rvanmech/unky-mo/internal/state"
)

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	st, err := s.deps.State.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.fillBranches(st)
	writeJSON(w, st)
}

// fillBranches sets the branch on live session rows that the TUI left
// empty — it only records branches for stray sessions, and the sidebar
// renders the field whenever it's set, so the web fills it in itself for
// the chat nav and the dashboard's Branch column.
func (s *Server) fillBranches(st *state.StateFile) {
	if s.deps.Git == nil {
		return
	}
	for i := range st.Projects {
		p := &st.Projects[i]
		if p.SessionID == "" || p.Branch != "" || p.Path == "" {
			continue
		}
		path := p.Path
		v, _ := s.branchCache.get(path, func() (any, error) { return s.deps.Git.Branch(path), nil })
		p.Branch, _ = v.(string)
	}
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	st, err := s.deps.State.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, st.Usage)
}
