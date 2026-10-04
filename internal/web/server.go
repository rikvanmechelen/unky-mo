package web

import (
	"encoding/json"
	"net/http"
	"sync"
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

	// Files panel + nav branch caches, keyed by checkout path. Short TTLs:
	// they only dedupe several open tabs polling the same checkout.
	filesCache  *ttlCache
	treeCache   *ttlCache
	branchCache *ttlCache
	shellsCache *ttlCache
	agentsCache *ttlCache

	// launchMu serializes session-mutating requests (launch, replace, stop).
	launchMu sync.Mutex

	// modeMu serializes permission-mode changes; modeSettle is the wait
	// between footer re-reads after each shift+tab (zero in tests).
	modeMu     sync.Mutex
	modeSettle time.Duration
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
		modeSettle:  25 * time.Millisecond,
		mux:         http.NewServeMux(),
		tmuxSession: tmuxSession,
		prCache:     newTTLCache(90 * time.Second),
		ticketCache: newTTLCache(ticketRefresh),
		transcripts: newTranscriptHub(),
		filesCache:  newTTLCache(2 * time.Second),
		treeCache:   newTTLCache(10 * time.Second),
		branchCache: newTTLCache(15 * time.Second),
		shellsCache: newTTLCache(2 * time.Second),
		agentsCache: newTTLCache(time.Second),
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
	s.mux.HandleFunc("GET /api/sessions/{windowID}/files", s.handleSessionFiles)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/tree", s.handleSessionTree)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/diff", s.handleSessionDiff)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/file", s.handleSessionFile)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/spinner", s.handleSpinner)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/mode", s.handleMode)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/mode", s.handleSetMode)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/shells", s.handleShells)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/shells/{pid}/output", s.handleShellOutput)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/subagents", s.handleSubagents)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/subagents/{agentID}/transcript", s.handleSubagentTranscript)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/terminals", s.handleTerminals)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals", s.handleNewTerminal)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/terminals/{pane}/output", s.handleTerminalOutput)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/input", s.handleTerminalInput)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/interrupt", s.handleTerminalInterrupt)
	s.mux.HandleFunc("DELETE /api/sessions/{windowID}/terminals/{pane}", s.handleCloseTerminal)
	s.mux.HandleFunc("GET /api/tickets", s.handleTickets)
	s.mux.HandleFunc("GET /api/tickets/{id}", s.handleTicketDetail)
	s.mux.HandleFunc("GET /api/transcript/{windowID}", s.handleTranscript)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/prompt", s.handlePrompt)
	s.mux.HandleFunc("DELETE /api/sessions/{windowID}", s.handleStopSession)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/projects/{name}/sessions", s.handleProjectSessions)
	s.mux.HandleFunc("POST /api/projects/{name}/sessions", s.handleLaunch)
	s.mux.HandleFunc("POST /api/projects/{name}/cleanup", s.handleCleanup)
	s.mux.HandleFunc("POST /api/projects/{name}/lift", s.handleLift)

	if h := embeddedStatic(); h != nil {
		s.mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
			h.serve(w, r, "chat.html")
		})
		s.mux.Handle("GET /", h)
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

// sessionPath returns the cwd of the live session at windowID, from the
// shared state file. ok is false if no live session exists there.
func (s *Server) sessionPath(windowID string) (string, bool) {
	st, err := s.deps.State.Read()
	if err != nil {
		return "", false
	}
	for _, p := range st.Projects {
		if p.WindowID == windowID && p.SessionID != "" && p.Path != "" {
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
