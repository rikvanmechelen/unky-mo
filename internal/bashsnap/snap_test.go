package bashsnap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

const session = "13e84595-612d-448b-a232-da071195249e"

// gitRepo makes a repo with one commit: a.txt and .gitignore (ignoring .env).
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@example.com")
	run(t, dir, "config", "user.name", "t")
	write(t, dir, "a.txt", "one\n")
	write(t, dir, ".gitignore", ".env\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testStore(t *testing.T) *Store {
	return newStore(t.TempDir(), moexec.DefaultCommander, time.Now)
}

// lsTree lists a snapshot tree's paths, reading objects the way a diff will.
func lsTree(t *testing.T, s *Store, snap *Snapshot, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "ls-tree", "-r", "--name-only", snap.Tree)
	cmd.Env = append(os.Environ(), "GIT_OBJECT_DIRECTORY="+snap.Objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(repo, ".git", "objects"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

func repoState(t *testing.T, repo string) string {
	t.Helper()
	idx, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return string(idx) + run(t, repo, "count-objects", "-v") + run(t, repo, "--no-optional-locks", "status", "--porcelain")
}

func TestTakeLeavesRepoUntouched(t *testing.T) {
	repo := gitRepo(t)
	s := testStore(t)
	write(t, repo, "a.txt", "two\n")
	write(t, repo, "sub/new.go", "package sub\n")
	write(t, repo, ".env", "SECRET=1\n")
	write(t, repo, "big.bin", strings.Repeat("x", MaxUntrackedSize+1))
	before := repoState(t, repo)

	snap, err := s.Take(context.Background(), session, filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if after := repoState(t, repo); after != before {
		t.Errorf("snapshot changed the repo:\nbefore %s\nafter %s", before, after)
	}
	if snap.Root != repo {
		t.Errorf("root = %q, want %q", snap.Root, repo)
	}
	got := strings.Join(lsTree(t, s, snap, repo), " ")
	if got != ".gitignore a.txt sub/new.go" {
		t.Errorf("tree = %q, want tracked + untracked, without .env and big.bin", got)
	}

	// The same tree again; a change gives a new one.
	again, err := s.Take(context.Background(), session, repo)
	if err != nil || again.Tree != snap.Tree {
		t.Errorf("second snapshot = %v, %v; want the same tree", again, err)
	}
	write(t, repo, "a.txt", "three\n")
	third, err := s.Take(context.Background(), session, repo)
	if err != nil || third.Tree == snap.Tree {
		t.Errorf("snapshot after an edit = %v, %v; want a new tree", third, err)
	}
}

func hook(t *testing.T, s *Store, phase, payload string) error {
	t.Helper()
	return s.Hook(context.Background(), phase, strings.NewReader(payload))
}

func bashPayload(cwd, id string, background bool) string {
	bg := "false"
	if background {
		bg = "true"
	}
	return `{"session_id":"` + session + `","cwd":"` + cwd + `","tool_name":"Bash","tool_use_id":"` + id + `","tool_input":{"command":"x","run_in_background":` + bg + `}}`
}

func TestHookRecordsOnlyChanges(t *testing.T) {
	repo := gitRepo(t)
	s := testStore(t)

	// A command that changes nothing leaves no record.
	if err := hook(t, s, "pre", bashPayload(repo, "toolu_quiet", false)); err != nil {
		t.Fatal(err)
	}
	if err := hook(t, s, "post", bashPayload(repo, "toolu_quiet", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Record(session, "toolu_quiet"); !errors.Is(err, ErrNoRecord) {
		t.Errorf("unchanged call kept a record: %v", err)
	}

	if err := hook(t, s, "pre", bashPayload(repo, "toolu_sed", false)); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "a.txt", "ONE\n")
	if err := hook(t, s, "post", bashPayload(repo, "toolu_sed", false)); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Record(session, "toolu_sed")
	if err != nil || !rec.Changed() || rec.Root != repo {
		t.Fatalf("record = %+v, %v", rec, err)
	}

	// A background command is recorded without a diff.
	if err := hook(t, s, "pre", bashPayload(repo, "toolu_bg", true)); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "a.txt", "later\n")
	if err := hook(t, s, "post", bashPayload(repo, "toolu_bg", true)); err != nil {
		t.Fatal(err)
	}
	if rec, err := s.Record(session, "toolu_bg"); err != nil || !rec.Background || rec.Changed() {
		t.Errorf("background record = %+v, %v", rec, err)
	}

	recs, err := s.Records(session)
	if err != nil || len(recs) != 1 || recs["toolu_sed"] == nil {
		t.Errorf("Records = %v, %v; want only toolu_sed", recs, err)
	}
}

func TestHookSkips(t *testing.T) {
	s := testStore(t)
	notRepo := t.TempDir()
	for name, payload := range map[string]string{
		"not json":     "{",
		"not bash":     `{"session_id":"` + session + `","cwd":"/tmp","tool_name":"Edit","tool_use_id":"toolu_1"}`,
		"bad tool id":  `{"session_id":"` + session + `","cwd":"/tmp","tool_name":"Bash","tool_use_id":"../../x"}`,
		"bad session":  `{"session_id":"../x","cwd":"/tmp","tool_name":"Bash","tool_use_id":"toolu_1"}`,
		"relative cwd": `{"session_id":"` + session + `","cwd":"tmp","tool_name":"Bash","tool_use_id":"toolu_1"}`,
		"not a repo":   bashPayload(notRepo, "toolu_1", false),
	} {
		if err := hook(t, s, "pre", payload); !errors.Is(err, ErrSkip) {
			t.Errorf("%s: err = %v, want ErrSkip", name, err)
		}
	}
	// A post without a pre is skipped too.
	if err := hook(t, s, "post", bashPayload(notRepo, "toolu_2", false)); !errors.Is(err, ErrSkip) {
		t.Errorf("post without pre: err = %v, want ErrSkip", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Dir, "records")); len(entries) != 0 {
		t.Errorf("skipped payloads wrote records: %v", entries)
	}
}

func TestRecordRejectsForeignPaths(t *testing.T) {
	s := testStore(t)
	tree := strings.Repeat("a", 40)
	for name, rec := range map[string]*Record{
		"objects outside": {Root: "/r", Pre: tree, PreObjects: "/etc/objects"},
		"bad tree":        {Root: "/r", Pre: "HEAD", PreObjects: filepath.Join(s.Dir, "buckets", "20260101", "objects")},
		"relative root":   {Root: "r", Pre: tree, PreObjects: filepath.Join(s.Dir, "buckets", "20260101", "objects")},
	} {
		if err := s.write(session, "toolu_x", rec); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Record(session, "toolu_x"); !errors.Is(err, ErrNoRecord) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSweep(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := newStore(t.TempDir(), moexec.DefaultCommander, func() time.Time { return now })
	tree := strings.Repeat("a", 40)
	for _, id := range []string{"toolu_old", "toolu_new"} {
		if err := s.write(session, id, &Record{Root: "/r", Pre: tree, PreObjects: filepath.Join(s.Dir, "buckets", "20261005", "objects")}); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-25 * time.Hour)
	os.Chtimes(s.recordPath(session, "toolu_old"), old, old)
	for _, day := range []string{"20261002", "20261003", "20261004", "20261005"} {
		os.MkdirAll(filepath.Join(s.Dir, "buckets", day, "objects"), 0o700)
	}
	s.Sweep()
	if _, err := os.Stat(s.recordPath(session, "toolu_old")); err == nil {
		t.Error("old record kept")
	}
	if _, err := os.Stat(s.recordPath(session, "toolu_new")); err != nil {
		t.Error("new record removed")
	}
	left, _ := os.ReadDir(filepath.Join(s.Dir, "buckets"))
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "20261003 20261004 20261005" {
		t.Errorf("buckets left = %v", names)
	}
}
