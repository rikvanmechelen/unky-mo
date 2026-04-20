package sidebar

import "testing"

// Phase A3: Characterization tests for termSession() — lock in the current
// naming derivation so Phase B changes are visible in the test diff.

func TestTermSession_UsesInstanceID(t *testing.T) {
	m := Model{instanceID: "a1b2c3d4e5f6", windowID: "@5"}
	got := m.termSession()
	if got != "mo-terms-a1b2c3d4e5f6" {
		t.Errorf("termSession() = %q, want mo-terms-a1b2c3d4e5f6", got)
	}
}

func TestTermSession_UsesWindowID(t *testing.T) {
	m := Model{windowID: "@5"}
	got := m.termSession()
	if got != "mo-terms-5" {
		t.Errorf("termSession() = %q, want mo-terms-5", got)
	}
}

func TestTermSession_WindowIDStripsAtSign(t *testing.T) {
	m := Model{windowID: "@17"}
	got := m.termSession()
	if got != "mo-terms-17" {
		t.Errorf("termSession() = %q, want mo-terms-17", got)
	}
}

func TestTermSession_FallsBackToSanitizedName(t *testing.T) {
	m := Model{windowName: "my:proj.foo"}
	got := m.termSession()
	if got != "mo-terms-my-proj-foo" {
		t.Errorf("termSession() = %q, want mo-terms-my-proj-foo", got)
	}
}

func TestTermSession_BareFallback(t *testing.T) {
	m := Model{}
	got := m.termSession()
	if got != "mo-terms" {
		t.Errorf("termSession() = %q, want mo-terms", got)
	}
}

func TestTermSession_WindowIDTakesPriorityOverName(t *testing.T) {
	m := Model{windowID: "@3", windowName: "alpha"}
	got := m.termSession()
	if got != "mo-terms-3" {
		t.Errorf("termSession() = %q, want mo-terms-3 (windowID should take priority)", got)
	}
}

func TestTermSession_InstanceIDTakesPriorityOverAll(t *testing.T) {
	m := Model{instanceID: "deadbeef0123", windowID: "@3", windowName: "alpha"}
	got := m.termSession()
	if got != "mo-terms-deadbeef0123" {
		t.Errorf("termSession() = %q, want mo-terms-deadbeef0123 (instanceID should take priority)", got)
	}
}

func TestSanitizeTermSessionSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"foo:bar", "foo-bar"},
		{"my.proj", "my-proj"},
		{"has space", "has-space"},
		{"a:b.c d", "a-b-c-d"},
		{"foo@feat", "foo@feat"}, // @ is NOT sanitized
	}
	for _, c := range cases {
		got := sanitizeTermSessionSuffix(c.in)
		if got != c.want {
			t.Errorf("sanitizeTermSessionSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
