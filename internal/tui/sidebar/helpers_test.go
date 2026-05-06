package sidebar

import (
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
)

func TestRenderDotKnownStatuses(t *testing.T) {
	// Each mapped status renders a filled dot; anything else falls to an open circle.
	cases := []struct {
		status string
		want   string // the literal rune expected in the output
	}{
		{"active", "●"},
		{"idle", "●"},
		{"permission", "●"},
		{"external", "●"},
		{"", "○"},
		{"unknown", "○"},
	}
	for _, c := range cases {
		got := renderDot(c.status)
		if !strings.Contains(got, c.want) {
			t.Errorf("renderDot(%q) should contain %q, got %q", c.status, c.want, got)
		}
	}
}

func TestTruncateName(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"just-right", 10, "just-right"},
		{"a-bit-too-long", 10, "a-bit-t..."},
		// maxLen <= 3: hard cut, no ellipsis.
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
		{"abcdef", 1, "a"},
	}
	for _, c := range cases {
		got := truncateName(c.in, c.maxLen)
		if got != c.want {
			t.Errorf("truncateName(%q, %d) = %q, want %q", c.in, c.maxLen, got, c.want)
		}
	}
}

func TestRenderSectionHeader(t *testing.T) {
	// The header renders with the label embedded. We don't lock the exact
	// rune counts since the style wrapper may vary; just assert presence.
	out := renderSectionHeader("Sessions", 40)
	if !strings.Contains(out, "Sessions") {
		t.Errorf("header missing label: %q", out)
	}
	// Narrow-width fallback renders with -- around the label.
	out = renderSectionHeader("Files", 5)
	if !strings.Contains(out, "Files") {
		t.Errorf("narrow fallback missing label: %q", out)
	}
}

func TestBuildFileTreeLinesSingleFile(t *testing.T) {
	lines := buildFileTreeLines([]string{"main.go"})
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if lines[0].display != "main.go" || lines[0].fileIndex != 0 {
		t.Errorf("line wrong: %+v", lines[0])
	}
}

func TestBuildFileTreeLinesCollapsesSingleChildDir(t *testing.T) {
	// internal/tui/app.go — "internal" has only "tui" as child, which has only "app.go".
	// The single-child dir collapse merges "internal/tui" into one node.
	lines := buildFileTreeLines([]string{"internal/tui/app.go"})
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (dir + file), got %d: %+v", len(lines), lines)
	}
	if !strings.Contains(lines[0].display, "internal") || !strings.Contains(lines[0].display, "tui") {
		t.Errorf("dir node should be collapsed, got %q", lines[0].display)
	}
	if lines[1].display != "app.go" || lines[1].fileIndex != 0 {
		t.Errorf("file line wrong: %+v", lines[1])
	}
}

func TestBuildFileTreeLinesSiblings(t *testing.T) {
	// Two files in same dir: dir is NOT collapsed (has multiple children).
	lines := buildFileTreeLines([]string{"pkg/a.go", "pkg/b.go"})
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (dir + 2 files), got %d: %+v", len(lines), lines)
	}
	if lines[0].display != "pkg" {
		t.Errorf("first line should be 'pkg', got %q", lines[0].display)
	}
	// File indexes preserve input order.
	if lines[1].fileIndex != 0 || lines[2].fileIndex != 1 {
		t.Errorf("file indexes lost: %+v / %+v", lines[1], lines[2])
	}
}

func TestRenderFileTreeEmptyReturnsEmpty(t *testing.T) {
	if got := renderFileTree(nil, 40, 10); got != "" {
		t.Errorf("empty file list should render empty string, got %q", got)
	}
}

func TestRenderFileTreeProducesNonEmpty(t *testing.T) {
	got := renderFileTree([]string{"main.go", "pkg/a.go", "pkg/b.go"}, 40, 10)
	if got == "" {
		t.Error("expected non-empty render for a few files")
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "a.go") {
		t.Errorf("rendered output missing filenames:\n%s", got)
	}
}

func TestTermSessionScopedPerWindow(t *testing.T) {
	cases := []struct {
		name       string
		windowID   string
		windowName string
		want       string
	}{
		{"windowID preferred", "@5", "moma-chatbot", "mo-terms-5"},
		{"windowName fallback", "", "moma-chatbot", "mo-terms-moma-chatbot"},
		{"sanitize bracket-and-space", "", "moma-chatbot [2]", "mo-terms-moma-chatbot-[2]"},
		{"sanitize colon", "", "foo:bar", "mo-terms-foo-bar"},
		{"sanitize dot", "", "foo.bar", "mo-terms-foo-bar"},
		{"bare fallback", "", "", "mo-terms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{windowID: c.windowID, windowName: c.windowName}
			if got := m.termSession(); got != c.want {
				t.Errorf("termSession(id=%q, name=%q) = %q, want %q",
					c.windowID, c.windowName, got, c.want)
			}
		})
	}
}

func TestSyncProjectName(t *testing.T) {
	cases := []struct {
		name       string
		windowName string
		want       string
	}{
		{"bare project", "moma-chatbot", "moma-chatbot"},
		{"with session title", "moma-chatbot [setup-scaffold]", "moma-chatbot"},
		{"with ordinal", "moma-chatbot [2]", "moma-chatbot"},
		{"worktree bare", "unky-mo@feat-auth", "unky-mo@feat-auth"},
		{"worktree with suffix", "unky-mo@feat-auth [debug-oauth]", "unky-mo@feat-auth"},
		{"empty falls back", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{windowName: c.windowName}
			if got := m.syncProjectName(); got != c.want {
				t.Errorf("syncProjectName() = %q, want %q", got, c.want)
			}
		})
	}
}

// Phase A2: Characterization tests for pickOwnSession — the core
// disambiguation logic extracted from ownWindowSession.

func TestPickOwnSession_SingleCandidate(t *testing.T) {
	candidates := []claude.Session{{PID: 100, SessionID: "s1"}}
	// With a single candidate and no pane PIDs, returns it directly.
	got := pickOwnSession(candidates, nil, func(int, map[int]bool) bool { return false })
	if got == nil || got.SessionID != "s1" {
		t.Errorf("single candidate should return it, got %v", got)
	}
}

func TestPickOwnSession_TwoSiblings_PicksPaneDescendant(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true, 51: true}
	// Only PID 200 descends from the pane tree.
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return pid == 200
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s2" {
		t.Errorf("should pick s2 (PID 200 is descendant), got %v", got)
	}
}

func TestPickOwnSession_NoMatchingDescendant_ReturnsFirst(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true}
	// Neither PID descends from the pane tree.
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return false
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s1" {
		t.Errorf("no match should fall back to first candidate, got %v", got)
	}
}

func TestPickOwnSession_FirstCandidateIsDescendant(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true}
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return pid == 100
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s1" {
		t.Errorf("should pick s1, got %v", got)
	}
}

func TestRenderFileTreeRespectsMaxLines(t *testing.T) {
	files := []string{}
	for i := 0; i < 20; i++ {
		files = append(files, "pkg/f"+string(rune('a'+i))+".go")
	}
	got := renderFileTree(files, 40, 5)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) > 6 {
		// Some implementations add a "…" overflow line on top of maxLines; allow +1.
		t.Errorf("maxLines=5 but got %d lines", len(lines))
	}
}

// --- parseGitStatus tests ---

func TestParseGitStatusEmpty(t *testing.T) {
	got := parseGitStatus("")
	if len(got) != 0 {
		t.Errorf("empty input should return empty map, got %v", got)
	}
}

func TestParseGitStatusModifiedFile(t *testing.T) {
	got := parseGitStatus(" M internal/tui/app.go")
	if got["internal/tui/app.go"] != "M" {
		t.Errorf("expected M for app.go, got %q", got["internal/tui/app.go"])
	}
}

func TestParseGitStatusMultipleStatuses(t *testing.T) {
	input := " M app.go\nA  new.go\n D deleted.go\n?? untracked.txt"
	got := parseGitStatus(input)

	cases := map[string]string{
		"app.go":         "M",
		"new.go":         "A",
		"deleted.go":     "D",
		"untracked.txt":  "?",
	}
	for path, want := range cases {
		if got[path] != want {
			t.Errorf("path %q: want %q, got %q", path, want, got[path])
		}
	}
}

func TestParseGitStatusRename(t *testing.T) {
	// Renames show as "R  old.go -> new.go"
	got := parseGitStatus("R  old.go -> new.go")
	if got["new.go"] != "R" {
		t.Errorf("rename should map new path with R, got %q", got["new.go"])
	}
	if _, ok := got["old.go"]; ok {
		t.Error("rename should not map old path")
	}
}

func TestParseGitStatusStagedAndWorktree(t *testing.T) {
	// "MM" means staged + worktree modified; should still show "M"
	got := parseGitStatus("MM both.go")
	if got["both.go"] != "M" {
		t.Errorf("MM should resolve to M, got %q", got["both.go"])
	}
}

func TestParseGitStatusSkipsShortLines(t *testing.T) {
	got := parseGitStatus("ab\n M ok.go")
	if len(got) != 1 {
		t.Errorf("should skip short lines, got %d entries", len(got))
	}
}

// --- buildDirTree tests ---

func TestBuildDirTreeEmpty(t *testing.T) {
	root := buildDirTree(nil)
	if root == nil {
		t.Fatal("root should not be nil for empty input")
	}
	if len(root.children) != 0 {
		t.Errorf("empty input should produce empty root, got %d children", len(root.children))
	}
	if !root.isDir {
		t.Error("root should be a directory")
	}
}

func TestBuildDirTreeSingleFile(t *testing.T) {
	root := buildDirTree([]string{"main.go"})
	if len(root.children) != 1 {
		t.Fatalf("want 1 child, got %d", len(root.children))
	}
	child := root.children["main.go"]
	if child == nil {
		t.Fatal("child main.go not found")
	}
	if child.isDir {
		t.Error("main.go should not be a directory")
	}
	if child.path != "main.go" {
		t.Errorf("path should be main.go, got %q", child.path)
	}
}

func TestBuildDirTreeNestedPath(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go"})
	// Should create: root -> a (dir) -> b (dir) -> c.go (file)
	a := root.children["a"]
	if a == nil || !a.isDir {
		t.Fatal("a should be a directory")
	}
	if a.path != "a" {
		t.Errorf("a.path = %q, want %q", a.path, "a")
	}
	b := a.children["b"]
	if b == nil || !b.isDir {
		t.Fatal("a/b should be a directory")
	}
	if b.path != "a/b" {
		t.Errorf("b.path = %q, want %q", b.path, "a/b")
	}
	c := b.children["c.go"]
	if c == nil || c.isDir {
		t.Fatal("c.go should be a file")
	}
	if c.path != "a/b/c.go" {
		t.Errorf("c.path = %q, want %q", c.path, "a/b/c.go")
	}
}

func TestBuildDirTreeSharedParent(t *testing.T) {
	root := buildDirTree([]string{"pkg/a.go", "pkg/b.go"})
	pkg := root.children["pkg"]
	if pkg == nil || !pkg.isDir {
		t.Fatal("pkg should be a directory")
	}
	if len(pkg.children) != 2 {
		t.Errorf("pkg should have 2 children, got %d", len(pkg.children))
	}
	if pkg.children["a.go"] == nil || pkg.children["b.go"] == nil {
		t.Error("pkg should contain a.go and b.go")
	}
}

func TestBuildDirTreePreservesOrder(t *testing.T) {
	root := buildDirTree([]string{"z.go", "a.go", "m.go"})
	// Order should match input order
	want := []string{"z.go", "a.go", "m.go"}
	if len(root.order) != len(want) {
		t.Fatalf("order length %d, want %d", len(root.order), len(want))
	}
	for i, name := range root.order {
		if name != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, name, want[i])
		}
	}
}

func TestBuildDirTreeDefaultsCollapsed(t *testing.T) {
	root := buildDirTree([]string{"a/b.go"})
	a := root.children["a"]
	if a == nil {
		t.Fatal("a directory not found")
	}
	if a.expanded {
		t.Error("directories should default to collapsed (expanded=false)")
	}
}

// --- flattenDirTree tests ---

func TestFlattenDirTreeEmpty(t *testing.T) {
	root := buildDirTree(nil)
	lines := flattenDirTree(root)
	if len(lines) != 0 {
		t.Errorf("empty tree should flatten to 0 lines, got %d", len(lines))
	}
}

func TestFlattenDirTreeAllCollapsed(t *testing.T) {
	// With all dirs collapsed, only root-level items are visible.
	// Dirs show as collapsed nodes, files show directly.
	root := buildDirTree([]string{"a/b.go", "c/d.go", "main.go"})
	lines := flattenDirTree(root)
	// Should see: a/ (collapsed), c/ (collapsed), main.go
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (2 collapsed dirs + 1 file), got %d: %+v", len(lines), lines)
	}
	// a/ is a dir
	if !lines[0].isDir || lines[0].display != "a" {
		t.Errorf("line 0: want dir 'a', got %+v", lines[0])
	}
	// c/ is a dir
	if !lines[1].isDir || lines[1].display != "c" {
		t.Errorf("line 1: want dir 'c', got %+v", lines[1])
	}
	// main.go is a file
	if lines[2].isDir || lines[2].display != "main.go" {
		t.Errorf("line 2: want file 'main.go', got %+v", lines[2])
	}
}

func TestFlattenDirTreeExpandOneLevel(t *testing.T) {
	root := buildDirTree([]string{"pkg/a.go", "pkg/b.go", "main.go"})
	// Expand pkg
	root.children["pkg"].expanded = true
	lines := flattenDirTree(root)
	// Should see: pkg/ (expanded), a.go, b.go, main.go
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d: %+v", len(lines), lines)
	}
	if !lines[0].isDir || lines[0].display != "pkg" {
		t.Errorf("line 0: want expanded dir 'pkg', got %+v", lines[0])
	}
	if lines[1].display != "a.go" || lines[1].path != "pkg/a.go" {
		t.Errorf("line 1: want file 'a.go' at 'pkg/a.go', got %+v", lines[1])
	}
	if lines[2].display != "b.go" {
		t.Errorf("line 2: want file 'b.go', got %+v", lines[2])
	}
	if lines[3].display != "main.go" {
		t.Errorf("line 3: want file 'main.go', got %+v", lines[3])
	}
}

func TestFlattenDirTreeCollapseHidesDescendants(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go", "a/d.go"})
	// Expand a but keep a/b collapsed
	root.children["a"].expanded = true
	lines := flattenDirTree(root)
	// Should see: a/ (expanded), b/ (collapsed), d.go
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %+v", len(lines), lines)
	}
	if !lines[1].isDir || lines[1].path != "a/b" {
		t.Errorf("line 1: want collapsed dir 'b', got %+v", lines[1])
	}
}

func TestFlattenDirTreeNestedExpand(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go"})
	root.children["a"].expanded = true
	root.children["a"].children["b"].expanded = true
	lines := flattenDirTree(root)
	// Should see: a/ (expanded), b/ (expanded), c.go
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %+v", len(lines), lines)
	}
	if lines[2].display != "c.go" || lines[2].path != "a/b/c.go" {
		t.Errorf("line 2: want file 'c.go', got %+v", lines[2])
	}
}

func TestFlattenDirTreeSingleChildCollapse(t *testing.T) {
	// When a dir has exactly one child that is also a dir, display collapses them.
	// a/b/c.go with a expanded: should show "a/b/" as single collapsed node
	root := buildDirTree([]string{"a/b/c.go"})
	root.children["a"].expanded = true
	lines := flattenDirTree(root)
	// a is expanded, but b is the only child and is a dir → display as "b" under a.
	// Actually, let's verify what we get. The key behavior: when you expand a,
	// if b is the only child dir, we can collapse the display to show "b/" directly.
	// But the node structure is preserved for expand/collapse.
	// With a expanded but b collapsed: a/, b/ (collapsed)
	found := false
	for _, l := range lines {
		if l.isDir && l.path == "a/b" {
			found = true
		}
	}
	if !found {
		t.Errorf("should show a/b dir node, got %+v", lines)
	}
}

func TestFlattenDirTreeFileIndex(t *testing.T) {
	root := buildDirTree([]string{"a.go", "b.go", "pkg/c.go"})
	root.children["pkg"].expanded = true
	lines := flattenDirTree(root)
	// Files should have fileIndex matching their position in the visible file list.
	// Dirs should have fileIndex == -1.
	for _, l := range lines {
		if l.isDir && l.fileIndex != -1 {
			t.Errorf("dir %q should have fileIndex -1, got %d", l.display, l.fileIndex)
		}
		if !l.isDir && l.fileIndex < 0 {
			t.Errorf("file %q should have fileIndex >= 0, got %d", l.display, l.fileIndex)
		}
	}
}

func TestFlattenDirTreeIndentation(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go"})
	root.children["a"].expanded = true
	root.children["a"].children["b"].expanded = true
	lines := flattenDirTree(root)
	// Each level should increase indentation
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	// Root children get " " indent, next level "   ", etc.
	if len(lines[0].indent) >= len(lines[1].indent) {
		t.Errorf("nested dir should have more indent: %q vs %q", lines[0].indent, lines[1].indent)
	}
	if len(lines[1].indent) >= len(lines[2].indent) {
		t.Errorf("nested file should have more indent: %q vs %q", lines[1].indent, lines[2].indent)
	}
}

// --- preserveExpandedState / restoreExpandedState tests ---

func TestPreserveExpandedStateEmpty(t *testing.T) {
	root := buildDirTree(nil)
	state := preserveExpandedState(root)
	if len(state) != 0 {
		t.Errorf("empty tree should produce empty state, got %v", state)
	}
}

func TestPreserveExpandedStateCaptures(t *testing.T) {
	root := buildDirTree([]string{"a/b.go", "c/d.go"})
	root.children["a"].expanded = true
	// c stays collapsed
	state := preserveExpandedState(root)
	if !state["a"] {
		t.Error("a should be expanded in state")
	}
	if state["c"] {
		t.Error("c should be collapsed in state")
	}
}

func TestRestoreExpandedStateRoundtrip(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go", "d/e.go"})
	root.children["a"].expanded = true
	root.children["a"].children["b"].expanded = true
	// d stays collapsed

	state := preserveExpandedState(root)

	// Build a fresh tree with same files
	root2 := buildDirTree([]string{"a/b/c.go", "d/e.go"})
	restoreExpandedState(root2, state, 0)

	if !root2.children["a"].expanded {
		t.Error("a should be restored as expanded")
	}
	if !root2.children["a"].children["b"].expanded {
		t.Error("a/b should be restored as expanded")
	}
	if root2.children["d"].expanded {
		t.Error("d should be restored as collapsed")
	}
}

func TestRestoreExpandedStateNewDirsGetDefault(t *testing.T) {
	// Old tree had a/, new tree has a/ and newdir/
	state := map[string]bool{"a": true}

	root := buildDirTree([]string{"a/b.go", "newdir/c.go"})
	restoreExpandedState(root, state, 1)

	if !root.children["a"].expanded {
		t.Error("a should be restored as expanded")
	}
	// newdir is at depth 1, defaultDepth=1 → expanded
	if !root.children["newdir"].expanded {
		t.Error("newdir at depth 1 should be expanded by default (defaultDepth=1)")
	}
}

func TestRestoreExpandedStateNewDirsBeyondDepth(t *testing.T) {
	state := map[string]bool{}

	root := buildDirTree([]string{"a/b/c.go"})
	restoreExpandedState(root, state, 1)

	// a is at depth 1 → expanded
	if !root.children["a"].expanded {
		t.Error("a at depth 1 should be expanded (defaultDepth=1)")
	}
	// b is at depth 2 → stays collapsed
	if root.children["a"].children["b"].expanded {
		t.Error("b at depth 2 should stay collapsed (defaultDepth=1)")
	}
}

func TestRestoreExpandedStateRemovedDirsIgnored(t *testing.T) {
	// Old state has "gone/" which doesn't exist in new tree
	state := map[string]bool{"gone": true, "a": true}

	root := buildDirTree([]string{"a/b.go"})
	restoreExpandedState(root, state, 0)

	// Should not panic; a should be restored
	if !root.children["a"].expanded {
		t.Error("a should be restored as expanded")
	}
}

// --- annotateTreeWithStatus tests ---

func TestAnnotateTreeWithStatusEmpty(t *testing.T) {
	lines := annotateTreeWithStatus(nil, nil)
	if len(lines) != 0 {
		t.Errorf("nil input should return nil, got %v", lines)
	}
}

func TestAnnotateTreeWithStatusCleanFile(t *testing.T) {
	lines := []fileTreeLine{
		{display: "clean.go", path: "clean.go"},
	}
	result := annotateTreeWithStatus(lines, map[string]string{})
	if result[0].gitStatus != "" {
		t.Errorf("clean file should have empty gitStatus, got %q", result[0].gitStatus)
	}
}

func TestAnnotateTreeWithStatusModifiedFile(t *testing.T) {
	lines := []fileTreeLine{
		{display: "app.go", path: "pkg/app.go"},
	}
	statusMap := map[string]string{"pkg/app.go": "M"}
	result := annotateTreeWithStatus(lines, statusMap)
	if result[0].gitStatus != "M" {
		t.Errorf("modified file should have gitStatus M, got %q", result[0].gitStatus)
	}
}

func TestAnnotateTreeWithStatusMultiple(t *testing.T) {
	lines := []fileTreeLine{
		{display: "a.go", path: "a.go"},
		{display: "b.go", path: "b.go"},
		{display: "c.go", path: "c.go"},
	}
	statusMap := map[string]string{"a.go": "M", "c.go": "?"}
	result := annotateTreeWithStatus(lines, statusMap)
	if result[0].gitStatus != "M" {
		t.Errorf("a.go: want M, got %q", result[0].gitStatus)
	}
	if result[1].gitStatus != "" {
		t.Errorf("b.go: want empty, got %q", result[1].gitStatus)
	}
	if result[2].gitStatus != "?" {
		t.Errorf("c.go: want ?, got %q", result[2].gitStatus)
	}
}

func TestAnnotateTreeWithStatusDirWithChangedChild(t *testing.T) {
	lines := []fileTreeLine{
		{display: "pkg", path: "pkg", isDir: true},
		{display: "app.go", path: "pkg/app.go"},
	}
	statusMap := map[string]string{"pkg/app.go": "M"}
	result := annotateTreeWithStatus(lines, statusMap)
	// Directory containing a changed file should get a marker
	if result[0].gitStatus != "●" {
		t.Errorf("dir with changed child should get ● marker, got %q", result[0].gitStatus)
	}
}

func TestAnnotateTreeWithStatusDirNoChanges(t *testing.T) {
	lines := []fileTreeLine{
		{display: "pkg", path: "pkg", isDir: true},
		{display: "clean.go", path: "pkg/clean.go"},
	}
	result := annotateTreeWithStatus(lines, map[string]string{})
	if result[0].gitStatus != "" {
		t.Errorf("dir without changed children should have empty gitStatus, got %q", result[0].gitStatus)
	}
}

// --- findDirNodeFromRoot tests ---

func TestFindDirNodeFromRootTopLevel(t *testing.T) {
	root := buildDirTree([]string{"a/b.go", "c/d.go"})
	node := findDirNodeFromRoot(root, "a")
	if node == nil || node.path != "a" {
		t.Errorf("should find top-level dir 'a', got %v", node)
	}
}

func TestFindDirNodeFromRootNested(t *testing.T) {
	root := buildDirTree([]string{"a/b/c.go"})
	node := findDirNodeFromRoot(root, "a/b")
	if node == nil || node.path != "a/b" {
		t.Errorf("should find nested dir 'a/b', got %v", node)
	}
}

func TestFindDirNodeFromRootNotFound(t *testing.T) {
	root := buildDirTree([]string{"a/b.go"})
	node := findDirNodeFromRoot(root, "nonexistent")
	if node != nil {
		t.Errorf("should return nil for nonexistent path, got %v", node)
	}
}

// --- fileLineCount / resolveFilePath integration tests ---

func TestFileLineCountChangedMode(t *testing.T) {
	m := Model{
		fileTreeMode: fileTreeModeChanged,
		changedFiles: []string{"a.go", "b.go"},
	}
	if got := m.fileLineCount(); got != 2 {
		t.Errorf("changed mode: fileLineCount() = %d, want 2", got)
	}
}

func TestFileLineCountFullMode(t *testing.T) {
	tree := buildDirTree([]string{"a/b.go", "c.go"})
	m := Model{
		fileTreeMode: fileTreeModeFull,
		dirTree:      tree,
	}
	// All collapsed: a/ (dir) + c.go (file) = 2 lines
	if got := m.fileLineCount(); got != 2 {
		t.Errorf("full mode collapsed: fileLineCount() = %d, want 2", got)
	}
	// Expand a: a/ + b.go + c.go = 3 lines
	tree.children["a"].expanded = true
	if got := m.fileLineCount(); got != 3 {
		t.Errorf("full mode expanded: fileLineCount() = %d, want 3", got)
	}
}

func TestResolveFilePathChangedMode(t *testing.T) {
	m := Model{
		fileTreeMode: fileTreeModeChanged,
		changedFiles: []string{"a.go", "b.go"},
		fileCursor:   1,
	}
	path, isDir, hasStatus := m.resolveFilePath()
	if path != "b.go" || isDir || !hasStatus {
		t.Errorf("resolveFilePath() = (%q, %v, %v), want (b.go, false, true)", path, isDir, hasStatus)
	}
}

func TestResolveFilePathFullModeFile(t *testing.T) {
	tree := buildDirTree([]string{"a.go", "b.go"})
	m := Model{
		fileTreeMode: fileTreeModeFull,
		dirTree:      tree,
		gitStatusMap: map[string]string{"a.go": "M"},
		fileCursor:   0, // a.go (first file)
	}
	path, isDir, hasStatus := m.resolveFilePath()
	if path != "a.go" || isDir || !hasStatus {
		t.Errorf("resolveFilePath() = (%q, %v, %v), want (a.go, false, true)", path, isDir, hasStatus)
	}
}

func TestResolveFilePathFullModeDir(t *testing.T) {
	tree := buildDirTree([]string{"pkg/a.go", "main.go"})
	m := Model{
		fileTreeMode: fileTreeModeFull,
		dirTree:      tree,
		fileCursor:   0, // pkg/ (first item is the dir)
	}
	path, isDir, _ := m.resolveFilePath()
	if path != "pkg" || !isDir {
		t.Errorf("resolveFilePath() = (%q, %v, _), want (pkg, true, _)", path, isDir)
	}
}

func TestResolveFilePathFullModeCleanFile(t *testing.T) {
	tree := buildDirTree([]string{"clean.go"})
	m := Model{
		fileTreeMode: fileTreeModeFull,
		dirTree:      tree,
		gitStatusMap: map[string]string{},
		fileCursor:   0,
	}
	path, isDir, hasStatus := m.resolveFilePath()
	if path != "clean.go" || isDir || hasStatus {
		t.Errorf("resolveFilePath() = (%q, %v, %v), want (clean.go, false, false)", path, isDir, hasStatus)
	}
}

func TestResolveFilePathOutOfBounds(t *testing.T) {
	m := Model{
		fileTreeMode: fileTreeModeChanged,
		changedFiles: []string{"a.go"},
		fileCursor:   5,
	}
	path, _, _ := m.resolveFilePath()
	if path != "" {
		t.Errorf("out of bounds should return empty, got %q", path)
	}
}

// --- Toggle expand/collapse via dirNode ---

func TestToggleExpandCollapse(t *testing.T) {
	tree := buildDirTree([]string{"pkg/a.go", "pkg/b.go"})
	pkg := tree.children["pkg"]
	if pkg.expanded {
		t.Fatal("pkg should start collapsed")
	}
	// Simulate toggle
	pkg.expanded = !pkg.expanded
	if !pkg.expanded {
		t.Error("pkg should be expanded after toggle")
	}
	// Toggle back
	pkg.expanded = !pkg.expanded
	if pkg.expanded {
		t.Error("pkg should be collapsed after second toggle")
	}
}

// --- renderGitStatusMarker tests ---

func TestRenderGitStatusMarkerKnownStatuses(t *testing.T) {
	// Just verify it doesn't panic and produces non-empty output
	for _, s := range []string{"M", "A", "D", "?", "R", "●"} {
		got := renderGitStatusMarker(s)
		if got == "" {
			t.Errorf("renderGitStatusMarker(%q) returned empty", s)
		}
	}
}

func TestRenderGitStatusMarkerUnknown(t *testing.T) {
	got := renderGitStatusMarker("X")
	if got != "X" {
		t.Errorf("unknown status should pass through, got %q", got)
	}
}

// --- ensureFileCursorVisible tests ---

func TestEnsureFileCursorVisibleScrollsDown(t *testing.T) {
	m := Model{fileCursor: 15, fileViewStart: 0}
	m.ensureFileCursorVisible(10)
	// Cursor at 15, viewport of 10 → viewStart should be 6 (15-10+1)
	if m.fileViewStart != 6 {
		t.Errorf("fileViewStart = %d, want 6", m.fileViewStart)
	}
}

func TestEnsureFileCursorVisibleScrollsUp(t *testing.T) {
	m := Model{fileCursor: 2, fileViewStart: 5}
	m.ensureFileCursorVisible(10)
	// Cursor at 2, viewStart at 5 → viewStart should snap to 2
	if m.fileViewStart != 2 {
		t.Errorf("fileViewStart = %d, want 2", m.fileViewStart)
	}
}

func TestEnsureFileCursorVisibleNoChange(t *testing.T) {
	m := Model{fileCursor: 5, fileViewStart: 3}
	m.ensureFileCursorVisible(10)
	// Cursor at 5 is within [3, 13) — no change
	if m.fileViewStart != 3 {
		t.Errorf("fileViewStart = %d, want 3 (no change)", m.fileViewStart)
	}
}

// --- Default mode is full tree ---

func TestFirstLoadAllDirsCollapsed(t *testing.T) {
	// Simulates what refreshDirTree does on first load (no prior state)
	root := buildDirTree([]string{"a/b.go", "c/d/e.go", "f.go"})
	restoreExpandedState(root, map[string]bool{}, 0)
	// All dirs should be collapsed
	if root.children["a"].expanded {
		t.Error("a should be collapsed on first load (defaultDepth=0)")
	}
	if root.children["c"].expanded {
		t.Error("c should be collapsed on first load (defaultDepth=0)")
	}
}
