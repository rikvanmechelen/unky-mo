package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

// Detail satisfies tickets.Provider by delegating to GetIssueDetail.
func (p *Provider) Detail(ctx context.Context, id string) (*tickets.TicketDetail, error) {
	return p.GetIssueDetail(ctx, id)
}

// GetIssueDetail fetches one issue with its description pre-rendered to HTML
// (via ?expand=renderedFields) and the extra metadata we want in the popup
// (reporter + assignee displayNames). Description is converted to plain text
// with a naive tag-strip + entity-decode — good enough for reading flow in
// a TUI; rich ADF rendering is a follow-up if ever needed.
func (p *Provider) GetIssueDetail(ctx context.Context, key string) (*tickets.TicketDetail, error) {
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/rest/api/3/issue/" + key +
		"?expand=renderedFields&fields=summary,status,priority,updated,created,project,reporter,assignee," +
		p.cfg.SprintFieldID

	body, err := p.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	// Parse twice: typed (for known fields) and loose (for dynamic sprint
	// field and renderedFields.description since its field name is dynamic
	// when customfield IDs differ).
	var typed struct {
		Key    string `json:"key"`
		Fields struct {
			Summary  string        `json:"summary"`
			Status   jiraStatus    `json:"status"`
			Priority *jiraPriority `json:"priority"`
			Updated  jiraTime      `json:"updated"`
			Created  jiraTime      `json:"created"`
			Project  jiraProject   `json:"project"`
			Reporter *jiraUser     `json:"reporter"`
			Assignee *jiraUser     `json:"assignee"`
		} `json:"fields"`
		RenderedFields struct {
			Description string `json:"description"`
		} `json:"renderedFields"`
	}
	if err := json.Unmarshal(body, &typed); err != nil {
		return nil, fmt.Errorf("jira: parse issue response: %w", err)
	}

	// Loose parse to pull sprint info out of the dynamic custom field.
	var loose struct {
		Fields map[string]interface{} `json:"fields"`
	}
	_ = json.Unmarshal(body, &loose)

	var sprintName string
	var inSprint bool
	if arr, ok := loose.Fields[p.cfg.SprintFieldID].([]interface{}); ok {
		for _, entry := range arr {
			if sp, ok := entry.(map[string]interface{}); ok {
				state, _ := sp["state"].(string)
				name, _ := sp["name"].(string)
				if state == "active" {
					inSprint = true
					if sprintName == "" {
						sprintName = name
					}
				}
			}
		}
	}

	rawStatus := typed.Fields.Status.Name
	bucket := p.cfg.StatusMap.Resolve(rawStatus)

	prio := tickets.PriorityUnknown
	if typed.Fields.Priority != nil {
		prio = priorityFromName(typed.Fields.Priority.Name)
	}

	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	return &tickets.TicketDetail{
		Ticket: tickets.Ticket{
			Provider:   p.Name(),
			ID:         typed.Key,
			Title:      typed.Fields.Summary,
			URL:        fmt.Sprintf("%s/browse/%s", baseURL, typed.Key),
			Bucket:     bucket,
			RawStatus:  rawStatus,
			Priority:   prio,
			InSprint:   inSprint,
			SprintName: sprintName,
			UpdatedAt:  typed.Fields.Updated.Time,
			CreatedAt:  typed.Fields.Created.Time,
			ProjectKey: typed.Fields.Project.Key,
		},
		DescriptionText: StripHTML(typed.RenderedFields.Description),
		Reporter:        userDisplay(typed.Fields.Reporter),
		AssigneeDisplay: userDisplay(typed.Fields.Assignee),
	}, nil
}

type jiraUser struct {
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

func userDisplay(u *jiraUser) string {
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.EmailAddress
}

// --- HTML → plain text ---

var (
	// tagRE matches any HTML tag.
	tagRE = regexp.MustCompile(`(?is)<[^>]+>`)
	// paragraphBreakRE matches block-level boundaries where a blank line is
	// the natural separator.
	paragraphBreakRE = regexp.MustCompile(`(?is)</(?:p|div|h[1-6]|tr|blockquote|pre)>|<hr\s*/?>`)
	// softBreakRE matches inline line breaks — <br> and </li> — which want
	// a single newline, not a paragraph gap.
	softBreakRE = regexp.MustCompile(`(?is)<br\s*/?>|</li>`)
	// listItemRE turns <li> openings into a leading bullet.
	listItemRE = regexp.MustCompile(`(?is)<li[^>]*>`)
	// multiNewlineRE collapses 3+ consecutive newlines.
	multiNewlineRE = regexp.MustCompile(`\n{3,}`)
)

// StripHTML converts the rendered-HTML description/comment bodies Jira
// returns into reasonable plain text: block tags become newlines, <li>
// becomes "- ", remaining tags are stripped, entities decoded. Exported so
// `mo jira issue` can reuse it for the diagnostic command.
func StripHTML(s string) string {
	if s == "" {
		return ""
	}
	// List items: bullet first, then drop the opening <li>.
	s = listItemRE.ReplaceAllString(s, "\n- ")
	// Inline breaks → single newline.
	s = softBreakRE.ReplaceAllString(s, "\n")
	// Paragraph boundaries → blank line (collapsed below).
	s = paragraphBreakRE.ReplaceAllString(s, "\n\n")
	// Everything else disappears.
	s = tagRE.ReplaceAllString(s, "")
	// Decode entities after tag-strip so things like &lt;x&gt; in code
	// samples render as "<x>".
	s = html.UnescapeString(s)
	// Tidy whitespace.
	s = multiNewlineRE.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	return s
}
