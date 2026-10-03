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
	s.fillTokens(st)
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

// fillTokens sets each live Claude row's context token count, which the
// chat view shows next to the 5-hour meter like the sidebar does. Rows
// running another agent are skipped — their transcripts aren't Claude JSONL.
// usage.SessionTokens caches by file size, so per-poll calls are cheap.
func (s *Server) fillTokens(st *state.StateFile) {
	if s.deps.History == nil {
		return
	}
	for i := range st.Projects {
		p := &st.Projects[i]
		if p.SessionID == "" || p.Path == "" || (p.AgentKey != "" && p.AgentKey != "c") {
			continue
		}
		p.Tokens = s.deps.History.ContextTokens(p.Path, p.SessionID)
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
