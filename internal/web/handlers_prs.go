package web

import (
	"fmt"
	"net/http"
	"strconv"
)

func (s *Server) handlePRs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}

	v, err := s.prCache.get("list:"+path, func() (any, error) {
		return s.deps.PRs.ListPRs(path)
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) handlePRDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.findProjectPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q not found", name))
		return
	}

	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid PR number"))
		return
	}

	key := fmt.Sprintf("detail:%s:%d", path, number)
	v, err := s.prCache.get(key, func() (any, error) {
		return s.deps.PRs.GetPRDetail(path, number)
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}
