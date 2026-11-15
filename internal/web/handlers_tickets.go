package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

// ticketsResponse flattens tickets.FetchResult's error field (which doesn't
// marshal meaningfully) into a plain string for the frontend.
type ticketsResponse struct {
	Tickets []tickets.Ticket   `json:"tickets"`
	Errors  []ticketFetchError `json:"errors,omitempty"`
}

// ticketFetchTimeout bounds one refresh of every provider's tickets.
const ticketFetchTimeout = 30 * time.Second

// ticketKeyRe is a ticket key as providers write them (OP-175).
var ticketKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}-[0-9]{1,9}$`)

type ticketFetchError struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

func (s *Server) handleTickets(w http.ResponseWriter, r *http.Request) {
	v, _ := s.cache.tickets.get("all", func() (any, error) {
		// Not the request's context: the result is shared and cached, and
		// one closed tab would otherwise cache "context canceled" for
		// everyone until the next refresh.
		ctx, cancel := context.WithTimeout(context.Background(), ticketFetchTimeout)
		defer cancel()
		ts, results := s.deps.Tickets.MyTickets(ctx)
		resp := ticketsResponse{Tickets: ts}
		for _, res := range results {
			if res.Err != nil {
				resp.Errors = append(resp.Errors, ticketFetchError{Provider: res.Provider, Error: res.Err.Error()})
			}
		}
		return resp, nil
	})
	writeJSON(w, v)
}

func (s *Server) handleTicketDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !ticketKeyRe.MatchString(id) {
		// The id goes into the provider's URL path: nothing but a key.
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad ticket id %q", id))
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("provider query param required"))
		return
	}

	detail, err := s.deps.Tickets.Detail(r.Context(), provider, id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, detail)
}
