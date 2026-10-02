package claude

import (
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
