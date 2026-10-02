package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/tickets"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func TestHandleTickets(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockTickets := mock_web.NewMockTicketSource(ctrl)
	mockTickets.EXPECT().MyTickets(gomock.Any()).Return(
		[]tickets.Ticket{{ID: "OP-1", Title: "fix thing"}},
		[]tickets.FetchResult{{Provider: "jira", Err: errors.New("timeout")}},
	)

	srv := NewServer(Deps{Tickets: mockTickets}, 0)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tickets", nil))

	var got ticketsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tickets) != 1 || got.Tickets[0].ID != "OP-1" {
		t.Fatalf("unexpected tickets: %+v", got.Tickets)
	}
	if len(got.Errors) != 1 || got.Errors[0].Error != "timeout" {
		t.Fatalf("unexpected errors: %+v", got.Errors)
	}
}

func TestHandleTicketDetail(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockTickets := mock_web.NewMockTicketSource(ctrl)
	mockTickets.EXPECT().Detail(gomock.Any(), "jira", "OP-1").Return(&tickets.TicketDetail{
		Ticket: tickets.Ticket{ID: "OP-1"},
	}, nil)

	srv := NewServer(Deps{Tickets: mockTickets}, 0)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tickets/OP-1?provider=jira", nil))

	var got tickets.TicketDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "OP-1" {
		t.Fatalf("unexpected detail: %+v", got)
	}
}

func TestHandleTicketDetailMissingProvider(t *testing.T) {
	srv := NewServer(Deps{}, 0)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tickets/OP-1", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}
