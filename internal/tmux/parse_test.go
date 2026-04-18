package tmux

import "testing"

func TestParseWindowList(t *testing.T) {
	in := []byte(`@0:0:mo:/home/user
@1:1:my-app:/home/user/workspace/my-app
@2:2:foo@feat:/home/user/workspace/foo.worktrees/feat
`)
	got := parseWindowList(in)
	want := []Window{
		{ID: "@0", Index: "0", Name: "mo", CWD: "/home/user"},
		{ID: "@1", Index: "1", Name: "my-app", CWD: "/home/user/workspace/my-app"},
		{ID: "@2", Index: "2", Name: "foo@feat", CWD: "/home/user/workspace/foo.worktrees/feat"},
	}
	if len(got) != len(want) {
		t.Fatalf("count: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n  got  %+v\n  want %+v", i, got[i], want[i])
		}
	}
}

func TestParseWindowListPreservesColonsInPath(t *testing.T) {
	// SplitN(4) must preserve any ':' inside the pane_current_path.
	in := []byte(`@3:3:weird:/tmp/dir:with:colons/inside`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].CWD != "/tmp/dir:with:colons/inside" {
		t.Errorf("cwd should preserve embedded colons, got %q", got[0].CWD)
	}
	if got[0].Name != "weird" {
		t.Errorf("name: %q", got[0].Name)
	}
}

func TestParseWindowListEmptyAndMalformed(t *testing.T) {
	// Empty input.
	if got := parseWindowList(nil); len(got) != 0 {
		t.Errorf("empty input: got %d rows", len(got))
	}
	// Blank lines and too-few-fields lines are silently dropped.
	in := []byte(`
@0:0:mo:/home
bogus
@1:1:partial
@2:2:ok:/p
`)
	got := parseWindowList(in)
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2 (%v)", len(got), got)
	}
	if got[0].Name != "mo" || got[1].Name != "ok" {
		t.Errorf("expected mo + ok, got %+v", got)
	}
}

func TestParsePIDSet(t *testing.T) {
	in := []byte(`12345
23456
34567
`)
	got := parsePIDSet(in)
	for _, p := range []int{12345, 23456, 34567} {
		if !got[p] {
			t.Errorf("missing pid %d", p)
		}
	}
	if len(got) != 3 {
		t.Errorf("count: got %d, want 3", len(got))
	}
}

func TestParsePIDSetMalformed(t *testing.T) {
	// Non-numeric lines are dropped.
	in := []byte(`42
notapid
88
`)
	got := parsePIDSet(in)
	if len(got) != 2 || !got[42] || !got[88] {
		t.Errorf("want {42,88}, got %v", got)
	}
}

func TestParsePIDSetEmpty(t *testing.T) {
	if got := parsePIDSet(nil); len(got) != 0 {
		t.Errorf("empty input: got %v", got)
	}
}
