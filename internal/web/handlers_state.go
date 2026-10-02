package web

import "net/http"

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	st, err := s.deps.State.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	st, err := s.deps.State.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, st.Usage)
}
