// Package tickets defines the provider-agnostic ticket model used by the
// dashboard tickets panel. A Provider (Jira, Linear, ...) returns a slice of
// normalized Tickets already mapped into one of the display Buckets.
package tickets

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Bucket is the coarse grouping rendered in the dashboard panel. Statuses
// from each provider are mapped into these four well-known buckets, plus
// BucketUnmapped for anything the user's config doesn't cover (kept visible
// on purpose so workflow changes are easy to spot).
type Bucket string

const (
	BucketInProgress Bucket = "in_progress"
	BucketBlocked    Bucket = "blocked"
	BucketReview     Bucket = "review"
	BucketTodo       Bucket = "todo"
	BucketUnmapped   Bucket = "unmapped"
)

// DisplayOrder is the order buckets render in the panel.
var DisplayOrder = []Bucket{
	BucketInProgress,
	BucketBlocked,
	BucketReview,
	BucketTodo,
	BucketUnmapped,
}

// DisplayLabel returns the human label for a bucket.
func DisplayLabel(b Bucket) string {
	switch b {
	case BucketInProgress:
		return "In Progress"
	case BucketBlocked:
		return "Blocked"
	case BucketReview:
		return "Review"
	case BucketTodo:
		return "To Do"
	case BucketUnmapped:
		return "Unmapped"
	}
	return string(b)
}

// Priority is a normalized 1..5 scale so sorting is provider-agnostic.
// Each provider's native priority gets mapped to this before reaching the UI.
type Priority int

const (
	PriorityUnknown Priority = 0
	PriorityLowest  Priority = 1
	PriorityLow     Priority = 2
	PriorityMedium  Priority = 3
	PriorityHigh    Priority = 4
	PriorityHighest Priority = 5
)

// Ticket is the normalized form shown in the dashboard. RawStatus is kept for
// display ("In Code Review" reads better than "review") and for surfacing
// unmapped statuses without losing information.
type Ticket struct {
	Provider   string
	ID         string // e.g. "OP-175"
	Title      string
	URL        string
	Bucket     Bucket
	RawStatus  string
	Priority   Priority
	InSprint   bool
	SprintName string
	UpdatedAt  time.Time
	CreatedAt  time.Time
	ProjectKey string
}

// TicketDetail extends Ticket with fields only fetched on demand (description,
// reporter, assignee). Kept separate from Ticket so the list endpoint stays
// lean — detail is only fetched when the user opens the popup.
type TicketDetail struct {
	Ticket
	DescriptionText  string // rendered from provider HTML to plain text
	Reporter         string
	AssigneeDisplay  string
}

// Provider is implemented by each ticket source.
type Provider interface {
	Name() string
	MyTickets(ctx context.Context) ([]Ticket, error)
	Detail(ctx context.Context, id string) (*TicketDetail, error)
}

// SortByRelevance orders tickets within a bucket:
//  1. in an active sprint first
//  2. higher priority first
//  3. more recently updated first
//
// This is stable so ties preserve input order (useful when providers already
// return a sensible secondary order like "ID asc").
func SortByRelevance(ts []Ticket) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if a.InSprint != b.InSprint {
			return a.InSprint
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
}

// Group buckets a flat slice of tickets. Buckets are returned in DisplayOrder;
// empty buckets are omitted. Each bucket's slice is sorted by relevance.
func Group(ts []Ticket) []BucketGroup {
	byBucket := make(map[Bucket][]Ticket)
	for _, t := range ts {
		b := t.Bucket
		if b == "" {
			b = BucketUnmapped
		}
		byBucket[b] = append(byBucket[b], t)
	}
	groups := make([]BucketGroup, 0, len(DisplayOrder))
	for _, b := range DisplayOrder {
		items := byBucket[b]
		if len(items) == 0 {
			continue
		}
		SortByRelevance(items)
		groups = append(groups, BucketGroup{Bucket: b, Tickets: items})
	}
	return groups
}

// BucketGroup is a bucket plus its sorted tickets, ready to render.
type BucketGroup struct {
	Bucket  Bucket
	Tickets []Ticket
}

// StatusMap maps raw provider statuses to Mo buckets. Matching is
// case-insensitive and whitespace-trimmed so config authors don't have to
// get the casing exactly right.
type StatusMap struct {
	InProgress []string
	Blocked    []string
	Review     []string
	Todo       []string
}

// DefaultStatusMap returns sensible defaults that cover stock Jira workflows.
// Users can still override via config.
func DefaultStatusMap() StatusMap {
	return StatusMap{
		InProgress: []string{"In Progress", "In Development", "Doing"},
		Blocked:    []string{"Blocked", "On Hold", "Waiting"},
		Review:     []string{"In Review", "Code Review", "In PR", "Ready for Review", "Reviewing"},
		Todo:       []string{"To Do", "Open", "Backlog", "Selected for Development", "Ready"},
	}
}

// Resolve returns the bucket for a raw status, or BucketUnmapped if no entry
// matches. Callers that want to keep the raw status visible should put the
// ticket in BucketUnmapped and surface RawStatus in the UI.
func (s StatusMap) Resolve(raw string) Bucket {
	needle := strings.ToLower(strings.TrimSpace(raw))
	if needle == "" {
		return BucketUnmapped
	}
	check := func(candidates []string, b Bucket) (Bucket, bool) {
		for _, c := range candidates {
			if strings.ToLower(strings.TrimSpace(c)) == needle {
				return b, true
			}
		}
		return "", false
	}
	if b, ok := check(s.InProgress, BucketInProgress); ok {
		return b
	}
	if b, ok := check(s.Blocked, BucketBlocked); ok {
		return b
	}
	if b, ok := check(s.Review, BucketReview); ok {
		return b
	}
	if b, ok := check(s.Todo, BucketTodo); ok {
		return b
	}
	return BucketUnmapped
}
