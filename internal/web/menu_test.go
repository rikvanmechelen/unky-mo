package web

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func menuFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "menus", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func menuRowTexts(v *menuView) []string {
	var out []string
	for _, r := range v.Rows {
		out = append(out, r.Text)
	}
	return out
}

func mustParseMenu(t *testing.T, name string) *menuView {
	t.Helper()
	v := parseMenu(menuFixture(t, name))
	if v == nil {
		t.Fatalf("%s: no menu found", name)
	}
	return v
}

func TestParseMenuNotAMenu(t *testing.T) {
	var files []string
	for _, glob := range []string{"permission/*.txt", "startup/*.txt", "queue/*.txt"} {
		m, _ := filepath.Glob(filepath.Join("testdata", glob))
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if v := parseMenu(string(b)); v != nil {
			t.Errorf("%s parsed as a menu: %+v", f, v.Title)
		}
	}
	if parseMenu("") != nil {
		t.Error("empty screen parsed as a menu")
	}
}

func TestParseMenuPickOne(t *testing.T) {
	v := mustParseMenu(t, "model.txt")
	if v.Title != "Select model" {
		t.Errorf("title = %q", v.Title)
	}
	if !strings.HasPrefix(v.Subtitle, "Switch between Claude models") {
		t.Errorf("subtitle = %q", v.Subtitle)
	}
	if len(v.Rows) != 9 {
		t.Fatalf("rows = %d: %q", len(v.Rows), menuRowTexts(v))
	}
	first := v.Rows[0]
	if !first.Cursor || first.Number != 1 || v.Cursor != 0 {
		t.Errorf("first row = %+v, cursor %d", first, v.Cursor)
	}
	wantCols := []string{"Default (recommended) ✔", "Fable 5.1"}
	if !reflect.DeepEqual(first.Cols, wantCols) {
		t.Errorf("cols = %q, want %q", first.Cols, wantCols)
	}
	if got := v.Rows[1]; got.Number != 2 || got.Cursor || got.Cols[0] != "Opus 5.5" {
		t.Errorf("row 2 = %+v", got)
	}
	if last := v.Rows[8]; last.Marker != "▸" || last.Text != "More models" {
		t.Errorf("last row = %+v", last)
	}
	if len(v.Extra) != 1 || !strings.Contains(v.Extra[0], "High effort (default)") {
		t.Errorf("extra = %q", v.Extra)
	}
	wantLegend := []menuKey{
		{Keys: []string{"enter"}, Label: "set as default"},
		{Keys: []string{"s"}, Label: "use this session only"},
		{Keys: []string{"esc"}, Label: "cancel"},
	}
	if !reflect.DeepEqual(v.Legend, wantLegend) {
		t.Errorf("legend = %+v", v.Legend)
	}
	if v.Sig == "" {
		t.Error("no sig")
	}
}

func TestParseMenuMarkersAndPreview(t *testing.T) {
	v := mustParseMenu(t, "theme.txt")
	if v.Title != "Theme" || len(v.Rows) != 8 {
		t.Fatalf("title %q rows %q", v.Title, menuRowTexts(v))
	}
	cur := v.Rows[v.Cursor]
	if cur.Text != "Dark mode" || cur.Marker != "✔" || !cur.Cursor {
		t.Errorf("cursor row = %+v", cur)
	}
	if v.Rows[0].Text != "Auto (match terminal)" || v.Rows[0].Marker != "" {
		t.Errorf("row 0 = %+v", v.Rows[0])
	}
	joined := strings.Join(v.Extra, "\n")
	if !strings.Contains(joined, `console.log("Hello, Claude!");`) || !strings.Contains(joined, "Syntax theme: Monokai Extended") {
		t.Errorf("preview missing from extra:\n%s", joined)
	}
	if len(v.Legend) != 2 || v.Legend[0].Label != "select" {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuSlider(t *testing.T) {
	v := mustParseMenu(t, "effort.txt")
	if v.Title != "Effort" || len(v.Rows) != 0 || v.Cursor != -1 {
		t.Fatalf("title %q rows %d cursor %d", v.Title, len(v.Rows), v.Cursor)
	}
	joined := strings.Join(v.Extra, "\n")
	for _, want := range []string{"▲", "low", "xhigh", "Ultracode  off", "The default effort for this model"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extra lacks %q:\n%s", want, joined)
		}
	}
	wantLegend := []menuKey{
		{Keys: []string{"left", "right"}, Label: "adjust"},
		{Keys: []string{"enter"}, Label: "confirm"},
		{Keys: []string{"s"}, Label: "for this session only"},
		{Keys: []string{"esc"}, Label: "cancel"},
	}
	if !reflect.DeepEqual(v.Legend, wantLegend) {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuTextInput(t *testing.T) {
	v := mustParseMenu(t, "add-dir.txt")
	if v.Title != "Add directory to workspace" || v.Input == nil {
		t.Fatalf("title %q input %+v", v.Title, v.Input)
	}
	if v.Input.Kind != "text" || v.Input.Placeholder != "Directory path…" || v.Input.Text != "" {
		t.Errorf("input = %+v", v.Input)
	}
	if !strings.Contains(v.Subtitle, "Enter the path to the directory:") {
		t.Errorf("subtitle = %q", v.Subtitle)
	}
	if len(v.Rows) != 0 {
		t.Errorf("rows = %q", menuRowTexts(v))
	}
	if len(v.Legend) != 3 || v.Legend[0].Label != "complete" || v.Legend[0].Keys[0] != "tab" {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuTypedSearchText(t *testing.T) {
	screen := strings.Replace(menuFixture(t, "config.txt"), "Search settings…", "thinking", 1)
	v := parseMenu(screen)
	if v == nil || v.Input == nil {
		t.Fatal("no input")
	}
	if v.Input.Kind != "search" || v.Input.Text != "thinking" || v.Input.Placeholder != "" {
		t.Errorf("input = %+v", v.Input)
	}
}

func TestParseMenuTabsWithStyle(t *testing.T) {
	v := mustParseMenu(t, "config.ansi")
	if v.Title != "Settings" {
		t.Errorf("title = %q", v.Title)
	}
	labels := []string{}
	active := ""
	for _, tab := range v.Tabs {
		labels = append(labels, tab.Label)
		if tab.Active {
			active = tab.Label
		}
	}
	if !reflect.DeepEqual(labels, []string{"Status", "Config", "Usage", "Stats"}) || active != "Config" {
		t.Errorf("tabs = %+v", v.Tabs)
	}
	if v.Input == nil || v.Input.Kind != "search" || v.Input.Placeholder != "Search settings…" {
		t.Errorf("input = %+v", v.Input)
	}
	if v.Below != 9 {
		t.Errorf("below = %d", v.Below)
	}
	if len(v.Rows) < 30 {
		t.Fatalf("rows = %d", len(v.Rows))
	}
	cur := v.Rows[v.Cursor]
	if cur.Cols[0] != "Auto-compact" || len(cur.Cols) != 2 {
		t.Errorf("cursor row = %+v", cur)
	}
	// The search box has the keyboard here: typing, ↓ to leave it.
	wantLegend := []menuKey{
		{Keys: []string{}, Label: "filter", Typing: true},
		{Keys: []string{"enter", "down"}, Label: "select"},
		{Keys: []string{"up"}, Label: "tabs"},
		{Keys: []string{"esc"}, Label: "clear"},
	}
	if !reflect.DeepEqual(v.Legend, wantLegend) {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuActiveTabVariants(t *testing.T) {
	// Reverse video and a background colour both mark the active tab.
	cases := map[string]string{
		"permissions.ansi":           "Allow",
		"permissions-allow-tab.ansi": "Ask",
		"plugin.ansi":                "Discover",
	}
	for name, want := range cases {
		v := mustParseMenu(t, name)
		got := ""
		for _, tab := range v.Tabs {
			if tab.Active {
				got = tab.Label
			}
		}
		if got != want {
			t.Errorf("%s: active tab = %q, want %q (tabs %+v)", name, got, want, v.Tabs)
		}
	}
	// Without styling there is no way to tell: nothing is active.
	v := mustParseMenu(t, "help.txt")
	if v.Title != "Help" || len(v.Tabs) != 3 || v.Tabs[2].Label != "Custom commands" {
		t.Errorf("help tabs = %+v title %q", v.Tabs, v.Title)
	}
	for _, tab := range v.Tabs {
		if tab.Active {
			t.Errorf("plain capture marked %q active", tab.Label)
		}
	}
}

func TestParseMenuGroupsAndStatus(t *testing.T) {
	v := mustParseMenu(t, "mcp.txt")
	if v.Title != "Manage MCP servers" {
		t.Errorf("title = %q", v.Title)
	}
	byText := map[string]menuRow{}
	for _, r := range v.Rows {
		byText[r.Cols[0]] = r
	}
	first := v.Rows[0]
	if first.Marker != "✘" || first.Cols[0] != "claude_design" || !first.Cursor || !strings.HasPrefix(first.Group, "Local MCPs") {
		t.Errorf("first row = %+v", first)
	}
	if r := byText["chrome-devtools"]; r.Marker != "✔" || r.Group != "User MCPs (/home/user/.claude.json)" || r.Cols[1] != "33 tools" {
		t.Errorf("chrome-devtools = %+v", r)
	}
	if r := byText["claude.ai Spotify"]; r.Marker != "⚠" || r.Group != "claude.ai" {
		t.Errorf("spotify = %+v", r)
	}
	for _, r := range v.Rows {
		if r.Text == "User MCPs (/home/user/.claude.json)" {
			t.Errorf("group heading kept as a row")
		}
	}
}

func TestParseMenuScrolledList(t *testing.T) {
	v := mustParseMenu(t, "permissions.txt")
	if v.Title != "Permissions" || len(v.Tabs) != 6 {
		t.Fatalf("title %q tabs %+v", v.Title, v.Tabs)
	}
	if v.Subtitle != "Claude Code won't ask before using allowed tools." {
		t.Errorf("subtitle = %q", v.Subtitle)
	}
	if v.Input == nil || v.Input.Placeholder != "Search…" {
		t.Errorf("input = %+v", v.Input)
	}
	if len(v.Rows) != 10 || v.Rows[0].Number != 1 || v.Rows[0].Text != "Add a new rule…" {
		t.Fatalf("rows = %q", menuRowTexts(v))
	}
	if last := v.Rows[9]; last.Number != 10 || last.Text != "Bash(./mo hooks:*)" || !last.MoreBelow {
		t.Errorf("last row = %+v", last)
	}
	if v.Cursor != -1 {
		// the search box has focus: no row carries the cursor
		t.Errorf("cursor = %d", v.Cursor)
	}
}

func TestParseMenuReadOnlyPanel(t *testing.T) {
	v := mustParseMenu(t, "status.txt")
	if v.Title != "Settings" || len(v.Rows) != 0 {
		t.Fatalf("title %q rows %q", v.Title, menuRowTexts(v))
	}
	joined := strings.Join(v.Extra, "\n")
	if !strings.Contains(joined, "Version:           2.1.325") || !strings.Contains(joined, "Auto mode server:  Enabled") {
		t.Errorf("extra =\n%s", joined)
	}
	if len(v.Legend) != 1 || v.Legend[0].Keys[0] != "esc" {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuLegendInSubtitle(t *testing.T) {
	// /skills puts its key hints in the subtitle and ends on a note.
	v := mustParseMenu(t, "skills.txt")
	if v.Title != "Skills" {
		t.Errorf("title = %q", v.Title)
	}
	got := map[string][]string{}
	for _, k := range v.Legend {
		got[k.Label] = k.Keys
	}
	if !reflect.DeepEqual(got["cycle"], []string{"enter", "space"}) || !reflect.DeepEqual(got["search"], []string{"/"}) ||
		!reflect.DeepEqual(got["sort"], []string{"t"}) || !reflect.DeepEqual(got["close"], []string{"esc"}) {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuTypeToSearchHasNoKey(t *testing.T) {
	v := mustParseMenu(t, "plugin.txt")
	var typing *menuKey
	for i := range v.Legend {
		if v.Legend[i].Label == "search" {
			typing = &v.Legend[i]
		}
	}
	if typing == nil || len(typing.Keys) != 0 || !typing.Typing {
		t.Errorf("legend = %+v", v.Legend)
	}
}

func TestParseMenuRawAndSig(t *testing.T) {
	v := mustParseMenu(t, "model.txt")
	if !strings.HasPrefix(v.Raw, "Select model") || !strings.Contains(v.Raw, "Opus 5.5") || strings.Contains(v.Raw, "▔") {
		t.Errorf("raw = %q", v.Raw[:min(len(v.Raw), 120)])
	}
	// The sig follows what's drawn, cursor included.
	moved := strings.Replace(menuFixture(t, "model.txt"), "❯ 1.  Default", "  1.  Default", 1)
	moved = strings.Replace(moved, "     2.  Opus 5.5", "   ❯ 2.  Opus 5.5", 1)
	w := parseMenu(moved)
	if w == nil || w.Cursor != 1 || w.Sig == v.Sig {
		t.Errorf("moved cursor = %+v", w)
	}
	if again := parseMenu(menuFixture(t, "model.txt")); again.Sig != v.Sig {
		t.Error("sig changed for the same screen")
	}
}

func TestParseMenuSurvivesSidePanel(t *testing.T) {
	// A changes panel to the right must not leak into rows or sig.
	plain := menuFixture(t, "model.txt")
	var out []string
	for _, l := range strings.Split(plain, "\n") {
		pad := l + strings.Repeat(" ", max(0, 100-len([]rune(l))))
		out = append(out, pad+"│ panel line")
	}
	v := parseMenu(strings.Join(out, "\n"))
	if v == nil || len(v.Rows) != 9 || v.Rows[0].Cols[0] != "Default (recommended) ✔" {
		t.Fatalf("panel leaked: %+v", v)
	}
}

func TestParseMenuReadsEveryFixture(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "menus", "*.txt"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		v := parseMenu(string(b))
		if v == nil || v.Title == "" {
			t.Errorf("%s: no menu or no title: %+v", filepath.Base(f), v)
		}
	}
}

func TestParseMenuDetailScreen(t *testing.T) {
	// Enter on a plugin opens a detail page: prose above, actions below.
	v := mustParseMenu(t, "plugin-detail.txt")
	if v.Title != "Plugins" || len(v.Tabs) != 5 {
		t.Fatalf("title %q tabs %+v", v.Title, v.Tabs)
	}
	if !strings.Contains(v.Subtitle, "Plugin details") || !strings.Contains(v.Subtitle, "Version: 7.0.2") {
		t.Errorf("subtitle = %q", v.Subtitle)
	}
	want := []string{
		"Install for you (user scope)",
		"Install for all collaborators on this repository (project scope)",
		"Install for you, in this repo only (local scope)",
		"Open homepage", "View repository", "Back to plugin list",
	}
	if got := menuRowTexts(v); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %q", got)
	}
	if v.Cursor != 0 || v.Legend[0].Label != "select" || v.Legend[1].Label != "go back" {
		t.Errorf("cursor %d legend %+v", v.Cursor, v.Legend)
	}
}

func TestParseMenuNumberedActions(t *testing.T) {
	v := mustParseMenu(t, "mcp-detail.txt")
	if v.Title != "Claude_design MCP Server" {
		t.Errorf("title = %q", v.Title)
	}
	if len(v.Rows) != 2 || v.Rows[0].Number != 1 || v.Rows[0].Text != "Reconnect" || v.Rows[1].Text != "Disable" || !v.Rows[0].Cursor {
		t.Fatalf("rows = %+v", v.Rows)
	}
	if !strings.Contains(v.Subtitle, "Status:") || !strings.Contains(v.Subtitle, "URL:") {
		t.Errorf("subtitle = %q", v.Subtitle)
	}
}

func TestParseMenuFilteredSearch(t *testing.T) {
	v := mustParseMenu(t, "config-search.txt")
	if v.Input == nil || v.Input.Text != "think" || v.Input.Placeholder != "" {
		t.Fatalf("input = %+v", v.Input)
	}
	if len(v.Rows) != 2 || v.Rows[0].Cols[0] != "Thinking mode" || !v.Rows[0].Cursor {
		t.Errorf("rows = %q", menuRowTexts(v))
	}
}
