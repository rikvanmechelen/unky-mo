package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

func TestSearchJQLEndpointAndFields(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)

		// Matches the shape jira.go unmarshals — a single in-progress issue
		// in an active sprint.
		resp := `{
			"issues":[
				{
					"key":"OP-1",
					"fields":{
						"summary":"Fix thing",
						"status":{"name":"In Progress"},
						"priority":{"name":"High"},
						"updated":"2026-04-10T12:00:00.000+0000",
						"created":"2026-04-01T10:00:00.000+0000",
						"project":{"key":"OP"},
						"customfield_10020":[{"id":1,"name":"Sprint 42","state":"active"}]
					}
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	p, err := New(Config{
		BaseURL:   srv.URL,
		Email:     "rik@example.com",
		Token:     "token",
		Name:      "test",
		StatusMap: tickets.DefaultStatusMap(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := p.MyTickets(context.Background())
	if err != nil {
		t.Fatalf("MyTickets: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 ticket, got %d", len(got))
	}
	tk := got[0]

	if gotPath != "/rest/api/3/search/jql" {
		t.Errorf("endpoint: want /rest/api/3/search/jql, got %q", gotPath)
	}
	// Fields must include default sprint field (customfield_10020).
	fieldsRaw, _ := gotBody["fields"].([]interface{})
	foundSprint := false
	for _, f := range fieldsRaw {
		if fs, ok := f.(string); ok && fs == "customfield_10020" {
			foundSprint = true
		}
	}
	if !foundSprint {
		t.Errorf("fields must include sprint field; got %v", fieldsRaw)
	}

	if tk.ID != "OP-1" {
		t.Errorf("ticket ID: %q", tk.ID)
	}
	if tk.Bucket != tickets.BucketInProgress {
		t.Errorf("bucket: %q", tk.Bucket)
	}
	if tk.Priority != tickets.PriorityHigh {
		t.Errorf("priority: %d", tk.Priority)
	}
	if !tk.InSprint {
		t.Error("InSprint should be true (active sprint)")
	}
	if tk.SprintName != "Sprint 42" {
		t.Errorf("sprint name: %q", tk.SprintName)
	}
	if !strings.HasSuffix(tk.URL, "/browse/OP-1") {
		t.Errorf("URL: %q", tk.URL)
	}
	if tk.ProjectKey != "OP" {
		t.Errorf("project key: %q", tk.ProjectKey)
	}
}

func TestSearchSprintStateFutureIsNotInSprint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"issues":[{
			"key":"OP-2",
			"fields":{
				"summary":"Later",
				"status":{"name":"To Do"},
				"updated":"2026-01-01T00:00:00.000+0000",
				"created":"2026-01-01T00:00:00.000+0000",
				"project":{"key":"OP"},
				"customfield_10020":[{"id":2,"name":"Future","state":"future"}]
			}
		}]}`
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	p, _ := New(Config{BaseURL: srv.URL, Email: "e", Token: "t", StatusMap: tickets.DefaultStatusMap()})
	got, _ := p.MyTickets(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 ticket")
	}
	if got[0].InSprint {
		t.Errorf("future sprint should NOT count as InSprint")
	}
}

func TestSearchCustomSprintFieldID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()

	p, err := New(Config{
		BaseURL:       srv.URL,
		Email:         "e",
		Token:         "t",
		SprintFieldID: "customfield_99999",
		StatusMap:     tickets.DefaultStatusMap(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.MyTickets(context.Background()); err != nil {
		t.Fatalf("MyTickets: %v", err)
	}

	fields, _ := gotBody["fields"].([]interface{})
	for _, f := range fields {
		if fs, _ := f.(string); fs == "customfield_99999" {
			return
		}
	}
	t.Errorf("custom sprint field id should appear in request fields; got %v", fields)
}

func TestAuth401ReturnsErrAuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p, _ := New(Config{BaseURL: srv.URL, Email: "e", Token: "t", StatusMap: tickets.DefaultStatusMap()})
	_, err := p.MyTickets(context.Background())
	if err != ErrAuthFailed {
		t.Errorf("want ErrAuthFailed, got %v", err)
	}
}

func TestAuth429Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p, _ := New(Config{BaseURL: srv.URL, Email: "e", Token: "t", StatusMap: tickets.DefaultStatusMap()})
	_, err := p.MyTickets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Errorf("429 should surface rate-limited error, got %v", err)
	}
}

func TestExtractJiraErrorRESTShape(t *testing.T) {
	body := []byte(`{"errorMessages":["Field 'foo' is required"],"errors":{"bar":"missing"}}`)
	got := extractJiraError(body)
	if !strings.Contains(got, "Field 'foo' is required") {
		t.Errorf("REST errorMessages missing from output: %q", got)
	}
	if !strings.Contains(got, "bar: missing") {
		t.Errorf("REST errors map missing from output: %q", got)
	}
}

func TestExtractJiraErrorGatewayShape(t *testing.T) {
	body := []byte(`{"message":"Gateway timeout"}`)
	got := extractJiraError(body)
	if got != "Gateway timeout" {
		t.Errorf("want raw message, got %q", got)
	}
}

func TestExtractJiraErrorFallsBackToBody(t *testing.T) {
	body := []byte("Plain text error")
	got := extractJiraError(body)
	if got != "Plain text error" {
		t.Errorf("plain body fallback: got %q", got)
	}
}

func TestNewMissingFieldsReturnsErrAuthMissing(t *testing.T) {
	for _, cfg := range []Config{
		{Email: "e", Token: "t"},                       // no BaseURL
		{BaseURL: "https://x.atlassian.net", Token: "t"}, // no Email
		{BaseURL: "https://x.atlassian.net", Email: "e"}, // no Token
	} {
		_, err := New(cfg)
		if err != ErrAuthMissing {
			t.Errorf("want ErrAuthMissing for %+v, got %v", cfg, err)
		}
	}
}

func TestNewDefaultsSprintFieldID(t *testing.T) {
	// Verify the default sprint field ID is wired through by inspecting the
	// outgoing request fields.
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()

	p, _ := New(Config{BaseURL: srv.URL, Email: "e", Token: "t", StatusMap: tickets.DefaultStatusMap()})
	_, _ = p.MyTickets(context.Background())
	fields, _ := gotBody["fields"].([]interface{})
	for _, f := range fields {
		if fs, _ := f.(string); fs == "customfield_10020" {
			return
		}
	}
	t.Errorf("default sprint field (customfield_10020) missing from request: %v", fields)
}
