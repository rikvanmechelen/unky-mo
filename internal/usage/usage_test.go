package usage

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRenderBar(t *testing.T) {
	// util is in percent (0..100), matching the /api/oauth/usage response.
	tests := []struct {
		name  string
		util  float64
		width int
		want  string
	}{
		{"zero", 0, 10, strings.Repeat("░", 10)},
		{"full", 100, 10, strings.Repeat("▓", 10)},
		{"half rounds to 5", 50, 10, strings.Repeat("▓", 5) + strings.Repeat("░", 5)},
		{"52 rounds to 5", 52, 10, strings.Repeat("▓", 5) + strings.Repeat("░", 5)},
		{"55 rounds to 6", 55, 10, strings.Repeat("▓", 6) + strings.Repeat("░", 4)},
		{"over-saturates", 150, 6, strings.Repeat("▓", 6)},
		{"negative clamps", -10, 6, strings.Repeat("░", 6)},
		{"zero width", 50, 0, ""},
		{"neg width", 50, -3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderBar(tt.util, tt.width)
			if got != tt.want {
				t.Errorf("RenderBar(%v, %d) = %q, want %q", tt.util, tt.width, got, tt.want)
			}
		})
	}
}

func TestFormatResetIn(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, "—"},
		{"past", now.Add(-time.Minute), "now"},
		{"exactly now", now, "now"},
		{"45s", now.Add(45 * time.Second), "45s"},
		{"2m30s renders minutes only", now.Add(2*time.Minute + 30*time.Second), "2m"},
		{"59m", now.Add(59 * time.Minute), "59m"},
		{"1h05m", now.Add(time.Hour + 5*time.Minute), "1h05m"},
		{"2h15m", now.Add(2*time.Hour + 15*time.Minute), "2h15m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatResetIn(now, tt.t)
			if got != tt.want {
				t.Errorf("FormatResetIn = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTokensShort(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1_000, "1.0k"},
		{12_345, "12.3k"},
		{999_999, "1000.0k"}, // just under a million — boundary is at >=1_000_000
		{1_000_000, "1.0M"},
		{1_234_567, "1.2M"},
		{104_300_000, "104.3M"},
		{999_999_999, "1000.0M"},
		{1_000_000_000, "1.0B"},
		{2_500_000_000, "2.5B"},
		{-10, "0"},
	}
	for _, c := range cases {
		if got := FormatTokensShort(c.n); got != c.want {
			t.Errorf("FormatTokensShort(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestSessionTokensCachingAndSum(t *testing.T) {
	// Parser should return the last assistant turn's input-side tokens
	// (input + cache_read + cache_creation) — the current context footprint.
	dir := t.TempDir()
	path := dir + "/session.jsonl"
	content := `{"type":"user","message":{"role":"user"}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":5}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":50,"cache_creation_input_tokens":3}}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// First call parses and caches; second call must hit cache (same size).
	want := 1 + 50 + 3 // last assistant turn: input + cache_read + cache_creation
	if got := SessionTokens(path); got != want {
		t.Errorf("first call: got %d, want %d", got, want)
	}

	// Poison the cache entry's tokens to prove the second call came from cache.
	sessionCacheMu.Lock()
	e := sessionCache[path]
	e.tokens = -999
	sessionCache[path] = e
	sessionCacheMu.Unlock()

	if got := SessionTokens(path); got != -999 {
		t.Errorf("second call should return cached value: got %d", got)
	}

	// Append a new assistant turn. Size changes → cache invalidates → reparse,
	// and the newer turn replaces the previous one as the footprint.
	more := `{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":7,"output_tokens":8,"cache_read_input_tokens":40,"cache_creation_input_tokens":2}}}
`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(more); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	wantAfter := 7 + 40 + 2
	if got := SessionTokens(path); got != wantAfter {
		t.Errorf("after append: got %d, want %d", got, wantAfter)
	}
}

func TestSessionTokensCompactBoundary(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/session.jsonl"
	// A fat pre-compact turn, then a compact_boundary. With no assistant turn
	// after the boundary, the footprint should collapse to postTokens.
	content := `{"type":"assistant","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":60000,"cache_creation_input_tokens":200}}}
{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":60205,"postTokens":4900}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if got := SessionTokens(path); got != 4900 {
		t.Errorf("post-boundary, no new turn: got %d, want 4900", got)
	}

	// First post-compact assistant turn replaces the boundary's postTokens.
	more := `{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_read_input_tokens":4900,"cache_creation_input_tokens":1200}}}
`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(more); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	want := 1 + 4900 + 1200
	if got := SessionTokens(path); got != want {
		t.Errorf("post-boundary with new turn: got %d, want %d", got, want)
	}
}

func TestSessionTokensMissingFile(t *testing.T) {
	if got := SessionTokens("/nonexistent/path.jsonl"); got != 0 {
		t.Errorf("missing file should return 0, got %d", got)
	}
}

func TestPctFromUtil(t *testing.T) {
	// util is already in percent (0..100); we just round + clamp.
	cases := []struct {
		util float64
		want int
	}{
		{0, 0},
		{50, 50},
		{52.4, 52},
		{52.5, 53}, // math.Round is half-away-from-zero
		{100, 100},
		{120, 100},
		{-50, 0},
	}
	for _, c := range cases {
		if got := PctFromUtil(c.util); got != c.want {
			t.Errorf("PctFromUtil(%v) = %d, want %d", c.util, got, c.want)
		}
	}
}

// --- fetchWithDeps tests (cache decision tree) ---

type fakeCache struct {
	data   cached
	has    bool
	writes int
}

func (f *fakeCache) load() (cached, bool, error) { return f.data, f.has, nil }
func (f *fakeCache) save(c cached) error {
	f.data = c
	f.has = true
	f.writes++
	return nil
}

type fakeClient struct {
	next  httpStatus
	calls int
}

func (f *fakeClient) call(ctx context.Context, token string) httpStatus {
	f.calls++
	return f.next
}

func fakeToken() (string, error) { return "fake-token", nil }

func makeBody(fiveHourUtil float64, resetsAt time.Time) apiResponse {
	return apiResponse{
		FiveHour: apiWindow{Utilization: fiveHourUtil, ResetsAt: resetsAt},
		SevenDay: apiWindow{Utilization: 0.2, ResetsAt: resetsAt.Add(7 * 24 * time.Hour)},
	}
}

func TestFetchCooldownServesStale(t *testing.T) {
	// Cache is in cooldown → must not call API.
	now := time.Now()
	resetsAt := now.Add(time.Hour)
	cache := &fakeCache{
		has: true,
		data: cached{
			Body:          makeBody(0.42, resetsAt),
			FetchedAt:     now.Add(-2 * time.Minute),
			CooldownUntil: now.Add(30 * time.Second),
		},
	}
	client := &fakeClient{next: httpStatus{code: 200, body: makeBody(0.99, resetsAt)}}

	snap, err := fetchWithDeps(context.Background(), cache, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 0 {
		t.Errorf("API called during cooldown: %d", client.calls)
	}
	if snap.Source != "stale" {
		t.Errorf("source = %q, want stale", snap.Source)
	}
	if snap.FiveHour.Utilization != 0.42 {
		t.Errorf("got fresh-looking data during cooldown: %v", snap.FiveHour.Utilization)
	}
}

func TestFetchFreshCacheInsideTTL(t *testing.T) {
	now := time.Now()
	resetsAt := now.Add(time.Hour)
	cache := &fakeCache{
		has: true,
		data: cached{
			Body:      makeBody(0.42, resetsAt),
			FetchedAt: now.Add(-10 * time.Second),
		},
	}
	client := &fakeClient{}

	snap, err := fetchWithDeps(context.Background(), cache, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 0 {
		t.Errorf("API called with fresh cache: %d", client.calls)
	}
	if snap.Source != "cache" {
		t.Errorf("source = %q, want cache", snap.Source)
	}
}

func TestFetchRefetchesAfterWindowReset(t *testing.T) {
	// Cache is technically inside TTL, but a reset fell between FetchedAt and now.
	now := time.Now()
	fetchedAt := now.Add(-10 * time.Second)
	resetsAt := now.Add(-1 * time.Second) // just passed
	cache := &fakeCache{
		has: true,
		data: cached{
			Body:      makeBody(0.95, resetsAt),
			FetchedAt: fetchedAt,
		},
	}
	client := &fakeClient{next: httpStatus{code: 200, body: makeBody(0.01, now.Add(5*time.Hour))}}

	snap, err := fetchWithDeps(context.Background(), cache, client, fakeToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 1 {
		t.Errorf("expected 1 API call after window reset, got %d", client.calls)
	}
	if snap.Source != "api" {
		t.Errorf("source = %q, want api", snap.Source)
	}
}

func TestFetch429ServesStaleAndSetsCooldown(t *testing.T) {
	now := time.Now()
	resetsAt := now.Add(time.Hour)
	cache := &fakeCache{
		has: true,
		data: cached{
			Body:      makeBody(0.42, resetsAt),
			FetchedAt: now.Add(-cacheTTL - time.Second), // stale enough to refetch
		},
	}
	client := &fakeClient{next: httpStatus{code: http.StatusTooManyRequests}}

	snap, err := fetchWithDeps(context.Background(), cache, client, fakeToken)
	if err != nil {
		t.Fatalf("unexpected error on 429: %v", err)
	}
	if snap.Source != "stale" {
		t.Errorf("source = %q, want stale", snap.Source)
	}
	if client.calls != 1 {
		t.Errorf("expected 1 API call, got %d", client.calls)
	}
	if cache.data.CooldownUntil.IsZero() {
		t.Errorf("cooldown was not set after 429")
	}
}

func TestFetch401ReturnsAuthError(t *testing.T) {
	cache := &fakeCache{}
	client := &fakeClient{next: httpStatus{code: http.StatusUnauthorized, err: ErrAuthExpired}}

	_, err := fetchWithDeps(context.Background(), cache, client, fakeToken)
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("err = %v, want ErrAuthExpired", err)
	}
	if cache.writes != 0 {
		t.Errorf("cache was written on 401: %d writes", cache.writes)
	}
}
