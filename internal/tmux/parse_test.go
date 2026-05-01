package tmux

import "testing"

func TestParseWindowList(t *testing.T) {
	// 5-field format (pre-agent-key): backward compat
	in := []byte(`@0:0:mo:a1b2c3d4e5f6:/home/user
@1:1:my-app::/home/user/workspace/my-app
@2:2:foo@feat:deadbeef0123:/home/user/workspace/foo.worktrees/feat
`)
	got := parseWindowList(in)
	want := []Window{
		{ID: "@0", Index: "0", Name: "mo", InstanceID: "a1b2c3d4e5f6", CWD: "/home/user"},
		{ID: "@1", Index: "1", Name: "my-app", InstanceID: "", CWD: "/home/user/workspace/my-app"},
		{ID: "@2", Index: "2", Name: "foo@feat", InstanceID: "deadbeef0123", CWD: "/home/user/workspace/foo.worktrees/feat"},
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
	// SplitN(5) must preserve any ':' inside the pane_current_path.
	in := []byte(`@3:3:weird:abc123def456:/tmp/dir:with:colons/inside`)
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
	if got[0].InstanceID != "abc123def456" {
		t.Errorf("instance ID: %q", got[0].InstanceID)
	}
}

func TestParseWindowListEmptyAndMalformed(t *testing.T) {
	// Empty input.
	if got := parseWindowList(nil); len(got) != 0 {
		t.Errorf("empty input: got %d rows", len(got))
	}
	// Blank lines and too-few-fields lines are silently dropped.
	// Old 4-field format (no instance ID) is accepted via backwards compat.
	in := []byte(`
@0:0:mo:abc123:/home
bogus
@1:1:partial
@2:2:ok::/p
`)
	got := parseWindowList(in)
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2 (%v)", len(got), got)
	}
	if got[0].Name != "mo" || got[1].Name != "ok" {
		t.Errorf("expected mo + ok, got %+v", got)
	}
}

func TestParseWindowListBackwardsCompat4Fields(t *testing.T) {
	// Old 4-field format (pre-instance-ID) is still accepted.
	in := []byte(`@0:0:mo:/home/user`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].InstanceID != "" {
		t.Errorf("instance ID should be empty for old format, got %q", got[0].InstanceID)
	}
	if got[0].CWD != "/home/user" {
		t.Errorf("cwd: %q", got[0].CWD)
	}
}

func TestParseWindowListBackwardsCompatColonsInPath(t *testing.T) {
	// Old 4-field format where the CWD contains colons — the parser must
	// not misclassify the first path segment as an instance ID.
	in := []byte(`@3:3:weird:/tmp/dir:with:colons/inside`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].InstanceID != "" {
		t.Errorf("instance ID should be empty, got %q", got[0].InstanceID)
	}
	if got[0].CWD != "/tmp/dir:with:colons/inside" {
		t.Errorf("cwd should preserve embedded colons, got %q", got[0].CWD)
	}
}

func TestParseWindowListNewFormatEmptyInstanceID(t *testing.T) {
	// New 5-field format where instance ID is empty (window option not set).
	in := []byte(`@1:1:proj::/home/user/proj`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].InstanceID != "" {
		t.Errorf("instance ID should be empty, got %q", got[0].InstanceID)
	}
	if got[0].CWD != "/home/user/proj" {
		t.Errorf("cwd: %q", got[0].CWD)
	}
}

func TestParseWindowListNewFormatWithAgentKey(t *testing.T) {
	// New 6-field format: id:index:name:instanceID:agentKey:cwd
	in := []byte(`@0:0:mo:a1b2c3d4e5f6:c:/home/user
@1:1:my-app:deadbeef0123:g:/home/user/workspace/my-app
`)
	got := parseWindowList(in)
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2", len(got))
	}
	if got[0].InstanceID != "a1b2c3d4e5f6" || got[0].AgentKey != "c" || got[0].CWD != "/home/user" {
		t.Errorf("row 0: %+v", got[0])
	}
	if got[1].InstanceID != "deadbeef0123" || got[1].AgentKey != "g" || got[1].CWD != "/home/user/workspace/my-app" {
		t.Errorf("row 1: %+v", got[1])
	}
}

func TestParseWindowListNewFormatEmptyAgentKey(t *testing.T) {
	// New 6-field format where agent key is empty (window option not set).
	in := []byte(`@0:0:mo:a1b2c3d4e5f6::/home/user`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].InstanceID != "a1b2c3d4e5f6" {
		t.Errorf("instanceID: %q", got[0].InstanceID)
	}
	if got[0].AgentKey != "" {
		t.Errorf("agentKey should be empty, got %q", got[0].AgentKey)
	}
	if got[0].CWD != "/home/user" {
		t.Errorf("cwd: %q", got[0].CWD)
	}
}

func TestParseWindowListNewFormatColonsInPath(t *testing.T) {
	// SplitN(6) must preserve any ':' inside the pane_current_path.
	in := []byte(`@3:3:weird:abc123def456:g:/tmp/dir:with:colons/inside`)
	got := parseWindowList(in)
	if len(got) != 1 {
		t.Fatalf("count: %d", len(got))
	}
	if got[0].AgentKey != "g" {
		t.Errorf("agentKey: %q", got[0].AgentKey)
	}
	if got[0].CWD != "/tmp/dir:with:colons/inside" {
		t.Errorf("cwd should preserve embedded colons, got %q", got[0].CWD)
	}
}

func TestParsePaneInfoList(t *testing.T) {
	in := []byte("%0:12345\n%1:23456\n%2:34567\n")
	got := parsePaneInfoList(in)
	if len(got) != 3 {
		t.Fatalf("count: got %d, want 3", len(got))
	}
	want := []PaneInfo{
		{ID: "%0", PID: 12345},
		{ID: "%1", PID: 23456},
		{ID: "%2", PID: 34567},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], w)
		}
	}
}

func TestParsePaneInfoListEmpty(t *testing.T) {
	if got := parsePaneInfoList(nil); got != nil {
		t.Errorf("empty input: got %v", got)
	}
	if got := parsePaneInfoList([]byte("")); got != nil {
		t.Errorf("blank input: got %v", got)
	}
}

func TestParsePaneInfoListMalformed(t *testing.T) {
	in := []byte("%0:12345\nbogus\n%1:notanumber\n%2:99\n")
	got := parsePaneInfoList(in)
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2 (skip malformed)", len(got))
	}
	if got[0].ID != "%0" || got[0].PID != 12345 {
		t.Errorf("row 0: %+v", got[0])
	}
	if got[1].ID != "%2" || got[1].PID != 99 {
		t.Errorf("row 1: %+v", got[1])
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
