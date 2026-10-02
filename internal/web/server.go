package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// Server is the web dashboard's HTTP handler. It holds no mutable state of
// its own beyond small TTL caches for rate-limited upstream calls and the
// transcript hub's live-tail state — every request re-reads the shared
// state file / git / gh / ticket providers through Deps.
type Server struct {
	deps        Deps
	mux         *http.ServeMux
	tmuxSession string

	prCache     *ttlCache
	ticketCache *ttlCache
	transcripts *transcriptHub
}

// NewServer builds a Server. ticketRefresh is the ticket cache TTL
// (typically cfg.Tickets.RefreshSeconds). tmuxSession is the tmux session
// name sessions live in (typically cfg.TmuxSession), used to address a
// session's Claude pane for prompt injection.
func NewServer(deps Deps, ticketRefresh time.Duration, tmuxSession string) *Server {
	if ticketRefresh <= 0 {
		ticketRefresh = 5 * time.Minute
	}

	s := &Server{
		deps:        deps,
		mux:         http.NewServeMux(),
		tmuxSession: tmuxSession,
		prCache:     newTTLCache(90 * time.Second),
		ticketCache: newTTLCache(ticketRefresh),
		transcripts: newTranscriptHub(),
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
	s.mux.HandleFunc("GET /api/transcript/{windowID}", s.handleTranscript)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/prompt", s.handlePrompt)

	static, err := fs.Sub(staticFiles, "static")
	if err == nil {
		s.mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, static, "chat.html")
		})
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

// resolveSession looks up the live session at windowID (its tmux window ID,
// e.g. "@5") via the shared state file, returning its JSONL transcript path,
// Claude session ID, and current status ("active"/"idle"/"permission"). ok
// is false if no live session exists at that window.
func (s *Server) resolveSession(windowID string) (jsonlPath, sessionID, status string, ok bool) {
	st, err := s.deps.State.Read()
	if err != nil {
		return "", "", "", false
	}
	for _, p := range st.Projects {
		if p.WindowID == windowID && p.SessionID != "" {
			path := claude.ProjectsDirForPath(p.Path) + "/" + p.SessionID + ".jsonl"
			return path, p.SessionID, p.Status, true
		}
	}
	return "", "", "", false
}
