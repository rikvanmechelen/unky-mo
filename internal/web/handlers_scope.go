package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
)

// Bounds on what a scope check sends to the model.
const (
	maxScopeTurns  = 80   // the most recent ones are kept
	maxScopePrompt = 2000 // characters per prompt
	maxScopeFiles  = 300
	maxScopeBody   = 1 << 20
	maxTicketText  = 6000 // characters of ticket description
)

// ticketIDRe is a Jira-style key, the only kind of id a scope check fetches.
var ticketIDRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)

type scopeBody struct {
	Ticket *struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	} `json:"ticket"`
	Turns []review.ScopeTurn `json:"turns"`
}

// scopeContext is what the server adds to a scope check besides the
// browser's turns: a pull request (the reviewer view) is part of the ask.
type scopeContext struct {
	pr *review.ScopeTicket
}

type scopeResponse struct {
	*review.ScopeResult
	Ticket      *review.ScopeTicket `json:"ticket,omitempty"`
	TicketError string              `json:"ticketError,omitempty"`
	PR          *review.ScopeTicket `json:"pr,omitempty"`
}

// handleScope runs the Overview tab's on-demand drift check: POST
// {ticket?, turns: [{n, prompt, files}]} (built by the browser from the
// intent trace), ?base= as for /overview. Only files that mode's overview
// lists reach the model, prompts are cut short, and the ticket is fetched
// here, never taken from the browser. One check runs per window at a time.
func (s *Server) handleScope(w http.ResponseWriter, r *http.Request) {
	windowID, mode := r.PathValue("windowID"), r.URL.Query().Get("base")
	if mode == "" {
		mode = gitfiles.ModeBranch
	}
	if mode != gitfiles.ModeBranch && mode != gitfiles.ModeHead {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown base %q", mode))
		return
	}
	var body scopeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScopeBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	dir, ok := s.sessionPath(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	o, err := s.overview(dir, mode)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.runScope(w, r, windowID, o, body, scopeContext{})
}

// runScope runs one scope check over o's files, one at a time per lockKey.
func (s *Server) runScope(w http.ResponseWriter, r *http.Request, lockKey string, o *gitfiles.Overview, body scopeBody, extra scopeContext) {
	if _, busy := s.scopeRunning.LoadOrStore(lockKey, true); busy {
		writeError(w, http.StatusConflict, fmt.Errorf("a scope check is already running for this branch"))
		return
	}
	defer s.scopeRunning.Delete(lockKey)

	listed := map[string]bool{}
	for _, f := range o.Files {
		listed[f.Path] = true
	}
	req := review.ScopeRequest{Turns: []review.ScopeTurn{}, Root: o.Root, Rev: o.Rev, Head: o.Head, PR: extra.pr}
	seen, files := map[string]bool{}, 0
	// The most recent prompts matter most; the files changed outside the
	// conversation (turn 0) always go in.
	var prompts, outside []review.ScopeTurn
	for _, t := range body.Turns {
		if t.N == 0 {
			outside = append(outside, t)
		} else {
			prompts = append(prompts, t)
		}
	}
	if keep := maxScopeTurns - len(outside); len(prompts) > keep {
		prompts = prompts[len(prompts)-max(0, keep):]
	}
	turns := append(prompts, outside...)
	for _, t := range turns {
		if len(req.Turns) == maxScopeTurns {
			break
		}
		t.Prompt = truncateRunes(t.Prompt, maxScopePrompt)
		var kept []string
		for _, f := range t.Files {
			if listed[f] && !seen[f] && files < maxScopeFiles {
				seen[f] = true
				files++
				kept = append(kept, f)
			}
		}
		// A prompt without edits still says what the task is.
		if len(kept) == 0 && (t.N == 0 || t.Prompt == "") {
			continue
		}
		t.Files = kept
		req.Turns = append(req.Turns, t)
	}

	resp := scopeResponse{}
	if extra.pr != nil {
		resp.PR = &review.ScopeTicket{ID: extra.pr.ID, Title: extra.pr.Title}
	}
	if body.Ticket != nil && ticketIDRe.MatchString(body.Ticket.ID) && s.deps.Tickets != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		d, err := s.deps.Tickets.Detail(ctx, body.Ticket.Provider, body.Ticket.ID)
		cancel()
		if err != nil {
			resp.TicketError = err.Error()
		} else {
			req.Ticket = &review.ScopeTicket{ID: d.ID, Title: d.Title, Description: truncateRunes(d.DescriptionText, maxTicketText)}
			resp.Ticket = &review.ScopeTicket{ID: d.ID, Title: d.Title}
		}
	}

	res, err := s.deps.Scope.Check(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	resp.ScopeResult = res
	writeJSON(w, resp)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
