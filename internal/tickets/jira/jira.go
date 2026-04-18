// Package jira implements the tickets.Provider interface against Atlassian
// Cloud Jira. Uses basic auth (email + API token) which works uniformly
// across Atlassian Cloud and returns issues assigned to the token owner.
//
// The token is read from UNKY_MO_JIRA_TOKEN. The email and base URL come
// from config ([[tickets.jira]]). No credentials are ever written to disk
// by this package.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

const (
	defaultJQL = `assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC`
	maxResults = 100
)

// baseFields are the issue fields we request on every search. The sprint
// custom field is appended dynamically since its ID varies per installation.
var baseFields = []string{
	"summary",
	"status",
	"priority",
	"updated",
	"created",
	"project",
}

// ErrAuthMissing is returned when required credentials aren't present.
var ErrAuthMissing = errors.New("jira: missing credentials (set UNKY_MO_JIRA_TOKEN and email + base_url in config)")

// ErrAuthFailed is returned on 401 / 403 from the Jira API.
var ErrAuthFailed = errors.New("jira: authentication failed (check UNKY_MO_JIRA_TOKEN and email)")

// Config configures a single Jira provider instance.
type Config struct {
	BaseURL   string // e.g. "https://moma.atlassian.net"
	Email     string // user's atlassian email (basic auth username)
	Token     string // API token (Bearer-ish, used as basic auth password)
	Name      string // friendly name shown if multiple instances configured
	StatusMap tickets.StatusMap
	// SprintFieldID optionally overrides the default sprint custom field
	// (Jira's default is customfield_10020; some installations differ).
	SprintFieldID string
}

// Provider is a configured Jira ticket source.
type Provider struct {
	cfg    Config
	client *http.Client
}

// New returns a Provider ready to call Jira. Returns ErrAuthMissing when
// required fields are blank so callers can render the "not configured"
// state without treating it as a hard error.
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" || cfg.Email == "" || cfg.Token == "" {
		return nil, ErrAuthMissing
	}
	if cfg.SprintFieldID == "" {
		cfg.SprintFieldID = "customfield_10020"
	}
	return &Provider{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Name returns the friendly provider name (or "jira" by default).
func (p *Provider) Name() string {
	if p.cfg.Name != "" {
		return p.cfg.Name
	}
	return "jira"
}

// MyTickets fetches tickets assigned to the authenticated user, converts them
// to the normalized tickets.Ticket form, and maps statuses to buckets using
// the configured StatusMap.
func (p *Provider) MyTickets(ctx context.Context) ([]tickets.Ticket, error) {
	raw, err := p.search(ctx, defaultJQL)
	if err != nil {
		return nil, err
	}
	out := make([]tickets.Ticket, 0, len(raw.Issues))
	for _, iss := range raw.Issues {
		out = append(out, p.toTicket(iss))
	}
	return out, nil
}

// --- HTTP / JSON plumbing ---

type searchResp struct {
	Issues []jiraIssue `json:"issues"`
}

type jiraIssue struct {
	Key    string     `json:"key"`
	Fields jiraFields `json:"fields"`
}

type jiraFields struct {
	Summary  string        `json:"summary"`
	Status   jiraStatus    `json:"status"`
	Priority *jiraPriority `json:"priority"`
	Updated  jiraTime      `json:"updated"`
	Created  jiraTime      `json:"created"`
	Project  jiraProject   `json:"project"`
	// Sprint field is dynamic; captured out-of-band in toTicket via raw map.
	rawSprints []jiraSprint
}

type jiraStatus struct {
	Name string `json:"name"`
}

type jiraPriority struct {
	Name string `json:"name"`
}

type jiraProject struct {
	Key string `json:"key"`
}

type jiraSprint struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"` // "active" | "future" | "closed"
}

// jiraTime handles Jira's ISO-8601-with-milliseconds format.
type jiraTime struct {
	time.Time
}

func (t *jiraTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Jira returns e.g. "2026-04-15T09:32:11.123+0000" — try a few layouts.
	layouts := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("jira: unrecognized time format %q", s)
}

// doRequest wraps the common auth + status-code handling for any Jira HTTP
// call. method is "GET" or "POST"; reqBody is nil for GETs, pre-marshalled
// JSON for POSTs. Shared by search() and detail.go's GetIssueDetail().
func (p *Provider) doRequest(ctx context.Context, method, endpoint string, reqBody []byte) ([]byte, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		bodyReader = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Basic "+basicAuth(p.cfg.Email, p.cfg.Token))

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuthFailed
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("jira: rate-limited (HTTP 429) — try again shortly")
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("jira: HTTP %d — %s", resp.StatusCode, extractJiraError(body))
	}
	return body, nil
}

func (p *Provider) search(ctx context.Context, jql string) (*searchResp, error) {
	// New Jira Cloud search endpoint (POST, JSON body) — /rest/api/2/search
	// was removed in 2025; see Atlassian changelog CHANGE-2046.
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/rest/api/3/search/jql"

	reqBody := struct {
		JQL        string   `json:"jql"`
		Fields     []string `json:"fields"`
		MaxResults int      `json:"maxResults"`
	}{
		JQL:        jql,
		Fields:     append(append([]string{}, baseFields...), p.cfg.SprintFieldID),
		MaxResults: maxResults,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	body, err := p.doRequest(ctx, http.MethodPost, endpoint, bodyBytes)
	if err != nil {
		return nil, err
	}

	// Parse once as a loose map so we can extract the dynamic sprint field,
	// then re-decode the typed bits.
	var loose struct {
		Issues []map[string]interface{} `json:"issues"`
	}
	if err := json.Unmarshal(body, &loose); err != nil {
		return nil, fmt.Errorf("jira: parse response: %w", err)
	}

	var typed searchResp
	if err := json.Unmarshal(body, &typed); err != nil {
		return nil, fmt.Errorf("jira: parse response: %w", err)
	}

	for i, iss := range loose.Issues {
		if i >= len(typed.Issues) {
			break
		}
		fieldsMap, _ := iss["fields"].(map[string]interface{})
		if fieldsMap == nil {
			continue
		}
		sprintsRaw, ok := fieldsMap[p.cfg.SprintFieldID]
		if !ok || sprintsRaw == nil {
			continue
		}
		arr, ok := sprintsRaw.([]interface{})
		if !ok {
			continue
		}
		for _, entry := range arr {
			sp, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			s := jiraSprint{}
			if name, ok := sp["name"].(string); ok {
				s.Name = name
			}
			if state, ok := sp["state"].(string); ok {
				s.State = state
			}
			if id, ok := sp["id"].(float64); ok {
				s.ID = int(id)
			}
			typed.Issues[i].Fields.rawSprints = append(typed.Issues[i].Fields.rawSprints, s)
		}
	}

	return &typed, nil
}

func (p *Provider) toTicket(iss jiraIssue) tickets.Ticket {
	rawStatus := iss.Fields.Status.Name
	bucket := p.cfg.StatusMap.Resolve(rawStatus)

	var sprintName string
	var inSprint bool
	for _, s := range iss.Fields.rawSprints {
		if s.State == "active" {
			inSprint = true
			if sprintName == "" {
				sprintName = s.Name
			}
		}
	}

	prio := PriorityUnknown
	if iss.Fields.Priority != nil {
		prio = priorityFromName(iss.Fields.Priority.Name)
	}

	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	return tickets.Ticket{
		Provider:   p.Name(),
		ID:         iss.Key,
		Title:      iss.Fields.Summary,
		URL:        fmt.Sprintf("%s/browse/%s", baseURL, iss.Key),
		Bucket:     bucket,
		RawStatus:  rawStatus,
		Priority:   prio,
		InSprint:   inSprint,
		SprintName: sprintName,
		UpdatedAt:  iss.Fields.Updated.Time,
		CreatedAt:  iss.Fields.Created.Time,
		ProjectKey: iss.Fields.Project.Key,
	}
}

// PriorityUnknown is re-exported for convenience within this package.
const PriorityUnknown = tickets.PriorityUnknown

// priorityFromName maps Jira's named priorities to the normalized scale.
// Unknown names fall through to PriorityUnknown.
func priorityFromName(name string) tickets.Priority {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "highest", "blocker", "critical":
		return tickets.PriorityHighest
	case "high", "major":
		return tickets.PriorityHigh
	case "medium", "normal":
		return tickets.PriorityMedium
	case "low", "minor":
		return tickets.PriorityLow
	case "lowest", "trivial":
		return tickets.PriorityLowest
	}
	return tickets.PriorityUnknown
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// extractJiraError pulls a readable message out of a Jira error response body.
// Jira returns one of two shapes, depending on the failure:
//
//	{"errorMessages":["..."], "errors":{"field":"..."}}   // REST API errors
//	{"message":"...", "status-code":400}                  // gateway errors
//
// Falls back to a truncated version of the raw body if neither parses.
func extractJiraError(body []byte) string {
	var j struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if err := json.Unmarshal(body, &j); err == nil {
		var parts []string
		parts = append(parts, j.ErrorMessages...)
		for field, msg := range j.Errors {
			parts = append(parts, field+": "+msg)
		}
		if j.Message != "" {
			parts = append(parts, j.Message)
		}
		if len(parts) > 0 {
			return truncate(strings.Join(parts, "; "), 200)
		}
	}
	return truncate(strings.TrimSpace(string(body)), 200)
}
