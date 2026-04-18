package tickets

import (
	"testing"
	"time"
)

func TestStatusMapResolve(t *testing.T) {
	sm := StatusMap{
		InProgress: []string{"In Progress", "In Development"},
		Blocked:    []string{"Blocked"},
		Review:     []string{"Code Review", "In Review"},
		Todo:       []string{"To Do", "Backlog"},
	}

	tests := []struct {
		raw  string
		want Bucket
	}{
		{"In Progress", BucketInProgress},
		{"in progress", BucketInProgress}, // case-insensitive
		{"  Code Review  ", BucketReview}, // whitespace-trimmed
		{"Blocked", BucketBlocked},
		{"Backlog", BucketTodo},
		{"Something Weird", BucketUnmapped},
		{"", BucketUnmapped},
	}
	for _, tc := range tests {
		if got := sm.Resolve(tc.raw); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSortByRelevance(t *testing.T) {
	now := time.Now()
	ts := []Ticket{
		{ID: "A", Priority: PriorityMedium, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "B", Priority: PriorityHigh, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "C", Priority: PriorityHigh, InSprint: true, UpdatedAt: now.Add(-3 * time.Hour)},
		{ID: "D", Priority: PriorityLow, InSprint: true, UpdatedAt: now},
		{ID: "E", Priority: PriorityHigh, UpdatedAt: now}, // newer than B but same priority
	}
	SortByRelevance(ts)

	// Expected order:
	//   1) sprint first: C, D
	//      within sprint: C (High) > D (Low)
	//   2) non-sprint: E (High, newer), B (High, older), A (Medium)
	want := []string{"C", "D", "E", "B", "A"}
	for i, t2 := range ts {
		if t2.ID != want[i] {
			t.Errorf("position %d: got %s, want %s (full order: %v)", i, t2.ID, want[i], idsOf(ts))
			return
		}
	}
}

func TestGroupDisplayOrderAndEmptyBuckets(t *testing.T) {
	ts := []Ticket{
		{ID: "A", Bucket: BucketTodo},
		{ID: "B", Bucket: BucketInProgress},
		{ID: "C", Bucket: BucketReview},
		{ID: "D", Bucket: BucketUnmapped},
	}
	groups := Group(ts)
	if len(groups) != 4 {
		t.Fatalf("want 4 groups, got %d", len(groups))
	}
	// Empty BucketBlocked should be omitted; order is InProgress, Review, Todo, Unmapped.
	wantOrder := []Bucket{BucketInProgress, BucketReview, BucketTodo, BucketUnmapped}
	for i, g := range groups {
		if g.Bucket != wantOrder[i] {
			t.Errorf("position %d: got %s, want %s", i, g.Bucket, wantOrder[i])
		}
	}
}

func TestGroupAssignsEmptyBucketToUnmapped(t *testing.T) {
	ts := []Ticket{{ID: "A", Bucket: ""}}
	groups := Group(ts)
	if len(groups) != 1 || groups[0].Bucket != BucketUnmapped {
		t.Fatalf("expected lone Unmapped bucket, got %+v", groups)
	}
}

func TestStatusMapResolveEmptyStatusMapAllUnmapped(t *testing.T) {
	sm := StatusMap{}
	for _, raw := range []string{"In Progress", "Blocked", "Done", "Anything"} {
		if got := sm.Resolve(raw); got != BucketUnmapped {
			t.Errorf("empty map should send everything to Unmapped; %q → %q", raw, got)
		}
	}
}

func TestGroupSortsWithinEachBucket(t *testing.T) {
	now := time.Now()
	ts := []Ticket{
		{ID: "A", Bucket: BucketInProgress, Priority: PriorityMedium, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "B", Bucket: BucketInProgress, Priority: PriorityHigh, InSprint: true, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "C", Bucket: BucketTodo, Priority: PriorityLow, UpdatedAt: now},
	}
	groups := Group(ts)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	// First group is InProgress; B (in sprint + high) should precede A.
	if groups[0].Bucket != BucketInProgress {
		t.Errorf("want InProgress first, got %q", groups[0].Bucket)
	}
	if groups[0].Tickets[0].ID != "B" {
		t.Errorf("sprint+high should sort first within bucket, got %q", groups[0].Tickets[0].ID)
	}
}

func TestSortByRelevanceStableTieBreak(t *testing.T) {
	// Identical keys → input order preserved (stable sort).
	same := time.Now()
	ts := []Ticket{
		{ID: "A", Priority: PriorityMedium, UpdatedAt: same},
		{ID: "B", Priority: PriorityMedium, UpdatedAt: same},
		{ID: "C", Priority: PriorityMedium, UpdatedAt: same},
	}
	SortByRelevance(ts)
	want := []string{"A", "B", "C"}
	for i, v := range ts {
		if v.ID != want[i] {
			t.Errorf("stable-sort broke: position %d has %q, want %q", i, v.ID, want[i])
		}
	}
}

func TestDisplayLabel(t *testing.T) {
	cases := map[Bucket]string{
		BucketInProgress: "In Progress",
		BucketBlocked:    "Blocked",
		BucketReview:     "Review",
		BucketTodo:       "To Do",
		BucketUnmapped:   "Unmapped",
	}
	for b, want := range cases {
		if got := DisplayLabel(b); got != want {
			t.Errorf("DisplayLabel(%q) = %q, want %q", b, got, want)
		}
	}
	// Unknown bucket falls through to the raw string.
	if got := DisplayLabel(Bucket("custom")); got != "custom" {
		t.Errorf("unknown bucket should return raw string, got %q", got)
	}
}

func idsOf(ts []Ticket) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
