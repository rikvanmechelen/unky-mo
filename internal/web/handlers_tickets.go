package web

import (
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

// ticketsResponse flattens tickets.FetchResult's error field (which doesn't
// marshal meaningfully) into a plain string for the frontend.
type ticketsResponse struct {
	Tickets []tickets.Ticket   `json:"tickets"`
	Errors  []ticketFetchError `json:"errors,omitempty"`
}

type ticketFetchError struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

func (s *Server) handleTickets(w http.ResponseWriter, r *http.Request) {
	v, _ := s.ticketCache.get("all", func() (any, error) {
		ts, results := s.deps.Tickets.MyTickets(r.Context())
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
