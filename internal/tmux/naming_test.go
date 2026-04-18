package tmux

import "testing"

func TestComposeWindowName(t *testing.T) {
	cases := []struct {
		project, branch, suffix string
		want                    string
	}{
		{"foo", "", "", "foo"},
		{"foo", "feat", "", "foo@feat"},
		{"foo", "", "2", "foo [2]"},
		{"foo", "feat", "2", "foo@feat [2]"},
		{"foo", "feat", "debug-oauth", "foo@feat [debug-oauth]"},
	}
	for _, tc := range cases {
		got := ComposeWindowName(tc.project, tc.branch, tc.suffix)
		if got != tc.want {
			t.Errorf("ComposeWindowName(%q, %q, %q) = %q, want %q",
				tc.project, tc.branch, tc.suffix, got, tc.want)
		}
	}
}

func TestParseWindowName(t *testing.T) {
	cases := []struct {
		name                    string
		project, branch, suffix string
		ok                      bool
	}{
		{"foo", "foo", "", "", true},
		{"foo@feat", "foo", "feat", "", true},
		{"foo [2]", "foo", "", "2", true},
		{"foo@feat [2]", "foo", "feat", "2", true},
		{"foo@feat [debug-oauth]", "foo", "feat", "debug-oauth", true},
		{"", "", "", "", false},
		// Branches may contain brackets — as long as the suffix " [...]"
		// is the last occurrence, we strip it. Embedded brackets earlier
		// in the name stay with the branch.
		{"foo@feat[x] [2]", "foo", "feat[x]", "2", true},
	}
	for _, tc := range cases {
		project, branch, suffix, ok := ParseWindowName(tc.name)
		if ok != tc.ok || project != tc.project || branch != tc.branch || suffix != tc.suffix {
			t.Errorf("ParseWindowName(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
				tc.name, project, branch, suffix, ok,
				tc.project, tc.branch, tc.suffix, tc.ok)
		}
	}
}

func TestComposeParseRoundTrip(t *testing.T) {
	cases := []struct{ project, branch, suffix string }{
		{"foo", "", ""},
		{"foo", "feat", ""},
		{"foo", "", "2"},
		{"foo", "feat", "2"},
		{"foo", "feat/nested", "3"},
		{"foo", "feat", "debug-oauth"},
	}
	for _, tc := range cases {
		name := ComposeWindowName(tc.project, tc.branch, tc.suffix)
		p, b, s, ok := ParseWindowName(name)
		if !ok || p != tc.project || b != tc.branch || s != tc.suffix {
			t.Errorf("round-trip %q: got (%q, %q, %q, %v), want (%q, %q, %q, true)",
				name, p, b, s, ok, tc.project, tc.branch, tc.suffix)
		}
	}
}

func TestNextAvailableOrdinal(t *testing.T) {
	cases := []struct {
		existing        []string
		project, branch string
		want            string
	}{
		{nil, "foo", "", "2"},
		{[]string{"foo"}, "foo", "", "2"},
		{[]string{"foo", "foo [2]"}, "foo", "", "3"},
		{[]string{"foo", "foo [2]", "foo [3]"}, "foo", "", "4"},
		// Gaps are filled lowest-first.
		{[]string{"foo", "foo [3]"}, "foo", "", "2"},
		// Non-numeric siblings don't block numeric ordinals.
		{[]string{"foo", "foo [debug]"}, "foo", "", "2"},
		// Worktree siblings are scoped by branch.
		{[]string{"foo@feat", "foo [2]"}, "foo", "feat", "2"},
		{[]string{"foo@feat", "foo@feat [2]"}, "foo", "feat", "3"},
	}
	for _, tc := range cases {
		got := NextAvailableOrdinal(tc.existing, tc.project, tc.branch)
		if got != tc.want {
			t.Errorf("NextAvailableOrdinal(%v, %q, %q) = %q, want %q",
				tc.existing, tc.project, tc.branch, got, tc.want)
		}
	}
}
