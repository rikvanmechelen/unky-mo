package tickets

import "testing"

func TestSlugify(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Fix auth flow", "fix-auth-flow"},
		{"  Multiple   spaces  ", "multiple-spaces"},
		{"Unicode café support", "unicode-café-support"}, // keeps letters
		{"With/slash & ampersand", "with-slash-ampersand"},
		{"!!!leading punctuation", "leading-punctuation"},
		{"trailing ---", "trailing"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyLengthCap(t *testing.T) {
	in := "this is a very long title that should definitely be trimmed by the slug cap because we do not want absurd branch names"
	got := Slugify(in)
	if len(got) > maxSlugLen {
		t.Errorf("slug exceeded cap: len=%d, got=%q", len(got), got)
	}
	// Verify we trimmed at a word boundary, not mid-word.
	if got[len(got)-1] == '-' {
		t.Errorf("slug shouldn't end in '-': %q", got)
	}
}

func TestBranchNameForTicket(t *testing.T) {
	tests := []struct {
		id, title, want string
	}{
		{"OP-175", "fix auth flow", "OP-175-fix-auth-flow"},
		{"OP-175", "", "OP-175"},
		{"", "orphan title", "orphan-title"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := BranchNameForTicket(tc.id, tc.title); got != tc.want {
			t.Errorf("BranchNameForTicket(%q, %q) = %q, want %q", tc.id, tc.title, got, tc.want)
		}
	}
}
