package jira

import (
	"os"
	"testing"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

func TestPriorityFromName(t *testing.T) {
	tests := []struct {
		name string
		want tickets.Priority
	}{
		{"Highest", tickets.PriorityHighest},
		{"blocker", tickets.PriorityHighest}, // case-insensitive
		{"High", tickets.PriorityHigh},
		{"  Medium  ", tickets.PriorityMedium},
		{"Low", tickets.PriorityLow},
		{"Lowest", tickets.PriorityLowest},
		{"Weird", tickets.PriorityUnknown},
		{"", tickets.PriorityUnknown},
	}
	for _, tc := range tests {
		if got := priorityFromName(tc.name); got != tc.want {
			t.Errorf("priorityFromName(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestNewRejectsMissingFields(t *testing.T) {
	cases := []Config{
		{BaseURL: "", Email: "a@b.c", Token: "tok"},
		{BaseURL: "https://x", Email: "", Token: "tok"},
		{BaseURL: "https://x", Email: "a@b.c", Token: ""},
	}
	for i, c := range cases {
		if _, err := New(c); err != ErrAuthMissing {
			t.Errorf("case %d: want ErrAuthMissing, got %v", i, err)
		}
	}
}

func TestBuildProvidersSkipsIncomplete(t *testing.T) {
	isolateTokenEnv(t, "test-token")
	insts := []Instance{
		{Name: "ok", BaseURL: "https://x", Email: "a@b.c"},
		{Name: "missing-email", BaseURL: "https://x"},
		{Name: "missing-url", Email: "a@b.c"},
	}
	got := BuildProviders(insts)
	if len(got) != 1 {
		t.Fatalf("want 1 provider, got %d", len(got))
	}
	if got[0].Name() != "ok" {
		t.Errorf("want provider Name=ok, got %q", got[0].Name())
	}
}

func TestBuildProvidersReturnsNilWithoutToken(t *testing.T) {
	isolateTokenEnv(t, "")
	insts := []Instance{{BaseURL: "https://x", Email: "a@b.c"}}
	if got := BuildProviders(insts); got != nil {
		t.Errorf("want nil without token, got %v", got)
	}
}

func TestNeedsConfig(t *testing.T) {
	isolateTokenEnv(t, "")
	if !NeedsConfig(nil) {
		t.Error("no token + no instances should need config")
	}

	isolateTokenEnv(t, "tok")
	if !NeedsConfig(nil) {
		t.Error("token without instances should need config")
	}

	insts := []Instance{{BaseURL: "https://x", Email: "a@b.c"}}
	if NeedsConfig(insts) {
		t.Error("token + complete instance should NOT need config")
	}
}

// isolateTokenEnv points HOME at a temp dir (so the default token file path
// resolves to a fresh, empty location) and sets UNKY_MO_JIRA_TOKEN to the
// given value. Use this in any test that touches HasToken/LoadToken to
// prevent the user's real ~/.config/unky-mo/jira.token from affecting results.
func isolateTokenEnv(t *testing.T, envToken string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvToken, envToken)
}

func TestLoadTokenPrefersEnvOverFile(t *testing.T) {
	isolateTokenEnv(t, "from-env")
	if _, err := WriteToken("from-file", false); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "from-env" {
		t.Errorf("want env token to win, got %q", got)
	}
}

func TestLoadTokenFromFile(t *testing.T) {
	isolateTokenEnv(t, "")
	if _, err := WriteToken("secret-tok", false); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "secret-tok" {
		t.Errorf("want file token, got %q", got)
	}
}

func TestLoadTokenMissingReturnsEmpty(t *testing.T) {
	isolateTokenEnv(t, "")
	got, err := LoadToken()
	if err != nil {
		t.Errorf("want nil err on missing token, got %v", err)
	}
	if got != "" {
		t.Errorf("want empty token, got %q", got)
	}
}

func TestLoadTokenRejectsLoosePermissions(t *testing.T) {
	isolateTokenEnv(t, "")
	path, err := WriteToken("tok", false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadToken(); err == nil {
		t.Error("want error on loose permissions, got nil")
	}
}

func TestHasToken(t *testing.T) {
	isolateTokenEnv(t, "")
	if HasToken() {
		t.Error("want false with no env + no file")
	}
	isolateTokenEnv(t, "env-tok")
	if !HasToken() {
		t.Error("want true with env var set")
	}
	isolateTokenEnv(t, "")
	if _, err := WriteToken("file-tok", false); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !HasToken() {
		t.Error("want true with token file present")
	}
}
