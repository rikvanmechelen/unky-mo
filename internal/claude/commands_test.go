package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listingLine(t *testing.T, content string, names []string, initial bool) string {
	t.Helper()
	att := map[string]any{"type": "skill_listing", "content": content, "isInitial": initial, "skillCount": len(names)}
	if names != nil {
		att["names"] = names
	}
	b, err := json.Marshal(map[string]any{"type": "attachment", "attachment": att})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func commandsByName(cmds []SlashCommand) map[string]SlashCommand {
	m := map[string]SlashCommand{}
	for _, c := range cmds {
		m[c.Name] = c
	}
	return m
}

func TestParseSkillListingMultilineDescriptions(t *testing.T) {
	content := "- simplify: Review the changed code\n- data:extract: Build a data skill.\nBOOTSTRAP MODE - Triggers: foo\n- not-a-skill: continuation that looks like an entry\n- loop: Run a prompt on an interval"
	got := parseSkillListing(skillListing{Content: content, Names: []string{"simplify", "data:extract", "loop"}})
	if len(got) != 3 {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	if got[0].Name != "simplify" || got[0].Source != CommandSkill {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Source != CommandPlugin {
		t.Errorf("namespaced skill should be a plugin: %+v", got[1])
	}
	want := "Build a data skill.\nBOOTSTRAP MODE - Triggers: foo\n- not-a-skill: continuation that looks like an entry"
	if got[1].Description != want {
		t.Errorf("description = %q", got[1].Description)
	}
	// Old transcripts have no names: every "- x: " line starts an entry.
	if got := parseSkillListing(skillListing{Content: content}); len(got) != 4 {
		t.Errorf("without names: got %d entries", len(got))
	}
}

func TestReadFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.md")
	writeFile(t, path, "---\r\ndescription: >\r\n  Folded over\r\n  two lines\r\nargument-hint: \"[a] [b]\"\r\nname: 'x'\r\n---\r\nbody: not frontmatter\r\n")
	fm := readFrontmatter(path)
	if fm["description"] != "Folded over two lines" || fm["argument-hint"] != "[a] [b]" || fm["name"] != "x" {
		t.Errorf("got %#v", fm)
	}
	if _, ok := fm["body"]; ok {
		t.Error("read past the closing ---")
	}
	writeFile(t, path, "no frontmatter")
	if fm := readFrontmatter(path); fm == nil || len(fm) != 0 {
		t.Errorf("got %#v", fm)
	}
	if readFrontmatter(filepath.Join(dir, "missing.md")) != nil {
		t.Error("missing file should be nil")
	}
}

func TestSlashCommandsMergesSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "repo")
	cwd := filepath.Join(repo, "sub")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	// The transcript lists two skills, then adds a third mid-session; a
	// skill also tries to shadow a built-in.
	jsonl := filepath.Join(ProjectsDirForPath(cwd), "s1.jsonl")
	writeFile(t, jsonl, `{"type":"user","message":{"content":"hi"}}`+"\n"+
		listingLine(t, "- deploy: Ship it (listed)\n- clear: Not the built-in", []string{"deploy", "clear"}, true)+
		listingLine(t, "- eng:review: Review a PR", []string{"eng:review"}, false))

	writeFile(t, filepath.Join(home, ".claude", "commands", "standup.md"), "---\ndescription: Daily standup\nargument-hint: \"[date]\"\n---\nbody")
	writeFile(t, filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Ship it (file)\nargument-hint: <env>\n---\n")
	writeFile(t, filepath.Join(home, ".claude", "skills", "internal", "SKILL.md"), "---\nname: internal\nuser-invocable: false\n---\n")
	writeFile(t, filepath.Join(repo, ".claude", "commands", "nested", "fixit.md"), "---\ndescription: Repo-level\n---\n")
	writeFile(t, filepath.Join(cwd, ".claude", "skills", "hidden-from-model", "SKILL.md"), "---\ndescription: Not in the listing\ndisable-model-invocation: true\n---\n")

	got := commandsByName(SlashCommands(cwd, "s1"))

	if c := got["clear"]; c.Source != CommandBuiltin || c.Description == "Not the built-in" {
		t.Errorf("built-in was overridden: %+v", c)
	}
	if c := got["deploy"]; c.Description != "Ship it (listed)" || c.ArgumentHint != "<env>" || c.Source != CommandUser {
		t.Errorf("deploy = %+v", c)
	}
	if c := got["eng:review"]; c.Source != CommandPlugin {
		t.Errorf("eng:review = %+v", c)
	}
	if c := got["standup"]; c.Source != CommandUser || c.ArgumentHint != "[date]" {
		t.Errorf("standup = %+v", c)
	}
	if c := got["fixit"]; c.Source != CommandProject {
		t.Errorf("fixit (from the repo root's .claude) = %+v", c)
	}
	if c := got["hidden-from-model"]; c.Source != CommandProject || c.Description != "Not in the listing" {
		t.Errorf("hidden-from-model = %+v", c)
	}
	if _, ok := got["internal"]; ok {
		t.Error("user-invocable: false skill listed")
	}
	if c := got["model"]; !c.Dialog {
		t.Errorf("model should open a dialog: %+v", c)
	}
	all := SlashCommands(cwd, "s1")
	for i := 1; i < len(all); i++ {
		if all[i-1].Name >= all[i].Name {
			t.Fatalf("not sorted/unique at %q, %q", all[i-1].Name, all[i].Name)
		}
	}
}

func TestSlashCommandsWithoutTranscriptOrRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	writeFile(t, filepath.Join(filepath.Dir(cwd), ".claude", "commands", "outside.md"), "x")
	got := commandsByName(SlashCommands(cwd, "missing"))
	if _, ok := got["compact"]; !ok {
		t.Error("built-ins missing")
	}
	// Without a repository only cwd's own .claude counts, not its parents'.
	if _, ok := got["outside"]; ok {
		t.Error("parent .claude scanned outside a repo")
	}
}
