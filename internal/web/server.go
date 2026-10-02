package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"
)

// Server is the read-only web dashboard's HTTP handler. It holds no mutable
// state of its own beyond small TTL caches for rate-limited upstream calls —
// every request re-reads the shared state file / git / gh / ticket
// providers through Deps.
type Server struct {
	deps Deps
	mux  *http.ServeMux

	prCache     *ttlCache
	ticketCache *ttlCache
}

// NewServer builds a Server. ticketRefresh is the ticket cache TTL
// (typically cfg.Tickets.RefreshSeconds).
func NewServer(deps Deps, ticketRefresh time.Duration) *Server {
	if ticketRefresh <= 0 {
		ticketRefresh = 5 * time.Minute
	}

	s := &Server{
		deps:        deps,
		mux:         http.NewServeMux(),
		prCache:     newTTLCache(90 * time.Second),
		ticketCache: newTTLCache(ticketRefresh),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/usage", s.handleUsage)
	s.mux.HandleFunc("GET /api/projects", s.handleProjects)
	s.mux.HandleFunc("GET /api/projects/{name}/worktrees", s.handleWorktrees)
	s.mux.HandleFunc("GET /api/projects/{name}/prs", s.handlePRs)
	s.mux.HandleFunc("GET /api/projects/{name}/prs/{number}", s.handlePRDetail)
	s.mux.HandleFunc("GET /api/tickets", s.handleTickets)
	s.mux.HandleFunc("GET /api/tickets/{id}", s.handleTicketDetail)

	static, err := fs.Sub(staticFiles, "static")
	if err == nil {
		s.mux.Handle("/", http.FileServerFS(static))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// findProjectPath resolves a project name (as used in URL paths) to its
// configured filesystem path.
func (s *Server) findProjectPath(name string) (string, bool) {
	projects, err := s.deps.Projects.LoadProjects()
	if err != nil {
		return "", false
	}
	for _, p := range projects {
		if p.Name == name {
			return p.Path, true
		}
	}
	return "", false
}
