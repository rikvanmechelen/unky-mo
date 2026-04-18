package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://moma.atlassian.net", "https://moma.atlassian.net"},
		{"https://moma.atlassian.net/", "https://moma.atlassian.net"},
		{"http://local.atlassian.net/", "http://local.atlassian.net"},
		// Missing scheme defaults to https.
		{"moma.atlassian.net", "https://moma.atlassian.net"},
		{"moma.atlassian.net/", "https://moma.atlassian.net"},
		// Whitespace trimmed.
		{"  https://x.atlassian.net  ", "https://x.atlassian.net"},
		// Empty stays empty.
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeBaseURL(c.in); got != c.want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractJiraMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"errorMessages only", `{"errorMessages":["bad token"]}`, "bad token"},
		{"errors map", `{"errors":{"email":"required"}}`, "email: required"},
		{"message field", `{"message":"gateway timeout"}`, "gateway timeout"},
		{"plain text fallback", `plain error body`, "plain error body"},
		{"empty body", ``, ""},
	}
	for _, c := range cases {
		got := extractJiraMessage([]byte(c.body))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractJiraMessageTruncatesLongBody(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := extractJiraMessage([]byte(long))
	if !strings.HasSuffix(got, "...") {
		t.Errorf("want truncated with ellipsis, got %q", got)
	}
	if len(got) > 205 {
		t.Errorf("truncated output too long: %d", len(got))
	}
}

// isolateConfig redirects HOME + XDG so ensureJiraConfigBlock writes into a temp dir.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir
}

func TestEnsureJiraConfigBlockCreatesNewFile(t *testing.T) {
	home := isolateConfig(t)
	added, err := ensureJiraConfigBlock("https://moma.atlassian.net", "rik@moma.org")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !added {
		t.Error("should report added=true on first write")
	}
	cfgPath := filepath.Join(home, ".config", "unky-mo", "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "[[tickets.jira]]") {
		t.Errorf("block header missing: %s", body)
	}
	if !strings.Contains(body, `base_url = "https://moma.atlassian.net"`) {
		t.Errorf("base_url missing: %s", body)
	}
	if !strings.Contains(body, `email = "rik@moma.org"`) {
		t.Errorf("email missing: %s", body)
	}
}

func TestEnsureJiraConfigBlockIdempotent(t *testing.T) {
	isolateConfig(t)
	_, err := ensureJiraConfigBlock("https://moma.atlassian.net", "rik@moma.org")
	if err != nil {
		t.Fatal(err)
	}
	added, err := ensureJiraConfigBlock("https://moma.atlassian.net/", "rik@moma.org")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("should report added=false when block already exists (trailing slash normalized)")
	}
}

func TestEnsureJiraConfigBlockAppendsSecondInstance(t *testing.T) {
	home := isolateConfig(t)
	if _, err := ensureJiraConfigBlock("https://moma.atlassian.net", "rik@moma.org"); err != nil {
		t.Fatal(err)
	}
	added, err := ensureJiraConfigBlock("https://other.atlassian.net", "rik@other.com")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("different base_url should be appended")
	}
	data, _ := os.ReadFile(filepath.Join(home, ".config", "unky-mo", "config.toml"))
	if strings.Count(string(data), "[[tickets.jira]]") != 2 {
		t.Errorf("expected 2 [[tickets.jira]] headers, got:\n%s", data)
	}
}

func TestVerifyJiraCredsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("missing basic auth: %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"displayName":"Rik V.","emailAddress":"rik@moma.org"}`))
	}))
	defer srv.Close()

	who, err := verifyJiraCreds(context.Background(), srv.URL, "rik@moma.org", "token")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if who != "Rik V." {
		t.Errorf("who: got %q", who)
	}
}

func TestVerifyJiraCredsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := verifyJiraCreds(context.Background(), srv.URL, "e", "t")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("want rejected error, got %v", err)
	}
}

func TestVerifyJiraCredsWrongURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := verifyJiraCreds(context.Background(), srv.URL, "e", "t")
	if err == nil || !strings.Contains(err.Error(), "right URL") {
		t.Errorf("want wrong-URL error, got %v", err)
	}
}

func TestVerifyJiraCredsFallsBackToEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"displayName":"","emailAddress":"rik@example.com"}`))
	}))
	defer srv.Close()
	who, err := verifyJiraCreds(context.Background(), srv.URL, "e", "t")
	if err != nil {
		t.Fatal(err)
	}
	if who != "rik@example.com" {
		t.Errorf("want email fallback, got %q", who)
	}
}
