package web

import (
	"fmt"
	"net/http"
)

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.deps.Projects.LoadProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, projects)
}

func (s *Server) handleWorktrees(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}

	branches, err := s.deps.Worktrees.ListBranches(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, branches)
}
