package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectsDirForPathEncoding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		in   string
		want string
	}{
		{"/Users/rvanmech/workspace/mla_wrapper_app", "-Users-rvanmech-workspace-mla-wrapper-app"},
		{"/Users/x/workspace/unky-mo.worktrees/testing_worktrees", "-Users-x-workspace-unky-mo-worktrees-testing-worktrees"},
		{"/simple", "-simple"},
		// Leading slash is stripped before prepending "-", so double-slash inputs encode to double-dash.
		{"/a/b/c", "-a-b-c"},
	}

	for _, tc := range cases {
		got := ProjectsDirForPath(tc.in)
		wantPath := filepath.Join(home, ".claude", "projects", tc.want)
		if got != wantPath {
			t.Errorf("ProjectsDirForPath(%q) = %q, want %q", tc.in, got, wantPath)
		}
	}
}

func TestProjectsDirForPathContainsAllReplacements(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := ProjectsDirForPath("/a/b_c.d_e")
	base := filepath.Base(got)
	for _, ch := range []string{"/", "_", "."} {
		// The "/" check: only the leading slash remains as "-", other "/"s become "-" too.
		if ch == "/" {
			continue
		}
		if strings.Contains(base, ch) {
			t.Errorf("%q contains %q — not fully replaced", base, ch)
		}
	}
	if !strings.HasPrefix(base, "-") {
		t.Errorf("encoded dir should start with '-', got %q", base)
	}
}

func TestReadSessionsMissingDirReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, err := ReadSessions()
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if sess != nil {
		t.Errorf("want nil, got %v", sess)
	}
}

func TestReadSessionsIgnoresNonJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := SessionsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Valid session file.
	good := `{"pid":12345,"sessionId":"abc","cwd":"/tmp","startedAt":1,"kind":"","entrypoint":"","name":"n"}`
	if err := os.WriteFile(filepath.Join(dir, "12345.json"), []byte(good), 0644); err != nil {
		t.Fatal(err)
	}
	// Garbage (should be skipped).
	if err := os.WriteFile(filepath.Join(dir, "garbage.txt"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	sess, err := ReadSessions()
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sess) != 1 {
		t.Fatalf("want 1 session, got %d (%v)", len(sess), sess)
	}
	if sess[0].SessionID != "abc" || sess[0].PID != 12345 {
		t.Errorf("unexpected session parsed: %+v", sess[0])
	}
}
