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

	cache       caches
	transcripts *transcriptHub

	// launchMu serializes session-mutating requests (launch, replace, stop).
	launchMu sync.Mutex

	// scopeRunning holds the windows with a scope check in flight: one at
	// a time per window, since each runs a claude process.
	scopeRunning sync.Map

	// fetchMu serializes the reviewer view's git fetches of PR branches.
	fetchMu sync.Mutex

	// paneLocks holds one mutex per window (see paneLock); modeSettle is
	// the wait between screen re-reads after a key (zero in tests).
	paneLocks  sync.Map
	modeSettle time.Duration
	// menuMu guards menuEscOn: per window, the sig of a menu an Esc was
	// pressed on that Claude hasn't redrawn since (see handleMenuKeys).
	menuMu    sync.Mutex
	menuEscOn map[string]string

	// bootID identifies this process, so a browser that asked for a
	// restart can tell when the new server is up (GET /api/boot).
	bootID string

	// pairs rate-limits failed device pairings.
	pairs pairLimiter

	// hosts, when set, is the Host allowlist for a plain-HTTP server (see
	// SetPlainHTTPHosts).
	hosts *hostAllowlist
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
		cache:       newCaches(ticketRefresh),
		transcripts: newTranscriptHub(),
		bootID:      newBootID(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.hosts != nil && !s.hosts.allows(r.Host) {
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		return
	}
	w.Header().Set("Content-Security-Policy", scriptPolicy+"; "+s.frameAncestors())
	if crossSiteRefused(r) {
		writeError(w, http.StatusForbidden, errCrossSite)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && hubCORSPaths[r.URL.Path] && !sameOrigin(r) {
		hdr := w.Header()
		hdr.Set("Access-Control-Allow-Origin", origin)
		hdr.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			hdr.Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
			hdr.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			hdr.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/usage", s.handleUsage)
	s.mux.HandleFunc("GET /api/projects", s.handleProjects)
	s.mux.HandleFunc("GET /api/projects/{name}/worktrees", s.handleWorktrees)
	s.mux.HandleFunc("GET /api/projects/{name}/prs", s.handlePRs)
	s.mux.HandleFunc("GET /api/projects/{name}/prs/{number}", s.handlePRDetail)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/files", s.handleSessionFiles)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/tree", s.handleSessionTree)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/file", s.handleSessionFile)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/log", s.handleSessionLog)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/overview", s.handleOverview)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/architecture", s.handleArchitecture)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/calls", s.handleCalls)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/excerpt", s.handleExcerpt)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/scope", s.handleScope)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/fetch-base", s.handleFetchBase)
	// The reviewer view: a branch, or a pull request (resolved by number).
	for _, prefix := range []string{"/api/projects/{name}/branches/{branch}", "/api/projects/{name}/pulls/{pr}"} {
		s.mux.HandleFunc("GET "+prefix+"/overview", s.handleBranchOverview)
		s.mux.HandleFunc("GET "+prefix+"/architecture", s.handleBranchArchitecture)
		s.mux.HandleFunc("GET "+prefix+"/calls", s.handleBranchCalls)
		s.mux.HandleFunc("GET "+prefix+"/excerpt", s.handleBranchExcerpt)
		s.mux.HandleFunc("GET "+prefix+"/file", s.handleBranchFile)
		s.mux.HandleFunc("PUT "+prefix+"/file", s.handleBranchSaveFile)
		s.mux.HandleFunc("GET "+prefix+"/tree", s.handleBranchTree)
		s.mux.HandleFunc("POST "+prefix+"/scope", s.handleBranchScope)
		s.mux.HandleFunc("POST "+prefix+"/fetch-base", s.handleBranchFetchBase)
	}
	s.mux.HandleFunc("GET /api/sessions/{windowID}/commits/{hash}", s.handleCommit)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/commits/{hash}/file", s.handleCommitFile)
	s.mux.HandleFunc("PUT /api/sessions/{windowID}/file", s.handleSaveFile)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/spinner", s.handleSpinner)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/mode", s.handleMode)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/mode", s.handleSetMode)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/answer", s.handleAnswer)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/permission", s.handlePermission)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/permission", s.handleAnswerPermission)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/menu", s.handleMenu)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/menu/keys", s.handleMenuKeys)
	s.mux.HandleFunc("GET /api/windows/{windowID}/startup", s.handleStartup)
	s.mux.HandleFunc("POST /api/windows/{windowID}/startup", s.handleAnswerStartup)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/shells", s.handleShells)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/shells/{pid}/output", s.handleShellOutput)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/subagents", s.handleSubagents)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/subagents/{agentID}/transcript", s.handleSubagentTranscript)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/commands", s.handleCommands)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/prompt-history", s.handlePromptHistory)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/bash-changes", s.handleBashChanges)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/bash-changes/{toolUseID}", s.handleBashDiff)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/terminals", s.handleTerminals)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals", s.handleNewTerminal)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/terminals/{pane}/output", s.handleTerminalOutput)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/input", s.handleTerminalInput)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/keys", s.handleTerminalKeys)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/size", s.handleTerminalSize)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/terminals/{pane}/interrupt", s.handleTerminalInterrupt)
	s.mux.HandleFunc("DELETE /api/sessions/{windowID}/terminals/{pane}", s.handleCloseTerminal)
	s.mux.HandleFunc("GET /api/tickets", s.handleTickets)
	s.mux.HandleFunc("GET /api/tickets/{id}", s.handleTicketDetail)
	s.mux.HandleFunc("GET /api/transcript/{windowID}", s.handleTranscript)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/tool-image/{toolUseID}", s.handleToolImage)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/prompt", s.handlePrompt)
	s.mux.HandleFunc("POST /api/sessions/{windowID}/attachments", s.handleUploadAttachment)
	s.mux.HandleFunc("GET /api/sessions/{windowID}/attachments/{id}", s.handleGetAttachment)
	s.mux.HandleFunc("DELETE /api/sessions/{windowID}/attachments/{id}", s.handleDeleteAttachment)
	s.mux.HandleFunc("DELETE /api/sessions/{windowID}", s.handleStopSession)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/boot", s.handleBoot)
	s.mux.HandleFunc("GET /api/tls", s.handleTLSInfo)
	s.mux.HandleFunc("GET /ca.crt", s.handleCACert)
	s.mux.HandleFunc("GET /api/hub/hello", s.handleHubHello)
	s.mux.HandleFunc("GET /api/hub/addrs", s.handleHubAddrs)
	s.mux.HandleFunc("GET /api/hub/core", s.handleHubCore)
	s.mux.HandleFunc("POST /api/hub/pair", s.handlePair)
	s.mux.HandleFunc("GET /api/hub/whoami", s.handleWhoami)
	s.mux.HandleFunc("GET /api/hub/devices", s.handleDevices)
	s.mux.HandleFunc("DELETE /api/hub/devices/{id}", s.handleRevokeDevice)
	s.mux.HandleFunc("POST /api/hub/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/hub/ticket", s.handleTicket)
	s.mux.HandleFunc("GET /hub", s.handleHubFile)
	s.mux.HandleFunc("GET /hub/{name}", s.handleHubFile)
	s.mux.HandleFunc("GET /api/push", s.handlePushInfo)
	s.mux.HandleFunc("GET /api/push/subscriptions", s.handlePushLookup)
	s.mux.HandleFunc("POST /api/push/subscriptions", s.handlePushSubscribe)
	s.mux.HandleFunc("DELETE /api/push/subscriptions", s.handlePushUnsubscribe)
	s.mux.HandleFunc("POST /api/push/test", s.handlePushTest)
	s.mux.HandleFunc("POST /api/push/presence", s.handlePushPresence)
	s.mux.HandleFunc("POST /api/restart", s.handleRestart)
	s.mux.HandleFunc("GET /api/projects/{name}/sessions", s.handleProjectSessions)
	s.mux.HandleFunc("POST /api/projects/{name}/sessions", s.handleLaunch)
	s.mux.HandleFunc("POST /api/projects/{name}/cleanup", s.handleCleanup)
	s.mux.HandleFunc("POST /api/projects/{name}/lift", s.handleLift)

	if h := embeddedStatic(); h != nil {
		s.mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
			h.serve(w, r, "chat.html")
		})
		s.mux.HandleFunc("GET /branch", func(w http.ResponseWriter, r *http.Request) {
			h.serve(w, r, "branch.html")
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

// paneLock takes the window's lock for pressing keys in Claude's pane and
// returns its unlock. Everything that types there (prompts, question and
// permission answers, mode changes, menus, the trust dialog) takes it, so
// two requests never interleave their keys: a prompt landing in the middle
// of an answer's walk would pick a row or end up in the wrong box.
func (s *Server) paneLock(windowID string) func() {
	m, _ := s.paneLocks.LoadOrStore(windowID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Request body caps for the JSON endpoints: a request is refused once it
// passes the cap, instead of being read whole first.
const (
	maxSmallBody  = 64 << 10                  // answers, launches, cleanups, mode changes
	maxPromptBody = 4*maxPromptBytes + 64<<10 // the text JSON-escaped, plus attachment ids
)
