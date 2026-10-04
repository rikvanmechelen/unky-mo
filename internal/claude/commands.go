package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SlashCommand is one command a session's prompt box would complete after
// "/": a Claude Code built-in, a skill or plugin command from the session's
// skill listing, or a custom command or skill found on disk.
type SlashCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	Aliases      []string
	// Source is CommandBuiltin, CommandPlugin, CommandSkill, CommandUser
	// or CommandProject.
	Source string
	// Dialog marks a built-in that opens an interactive dialog in the
	// terminal (with an ArgumentHint: when run without arguments).
	Dialog bool
}

const (
	CommandBuiltin = "built-in"
	CommandPlugin  = "plugin"
	CommandSkill   = "skill"
	CommandUser    = "user"
	CommandProject = "project"
)

// builtinCommands are Claude Code's own slash commands. Nothing on disk or
// in the transcript lists them, so they're kept by hand: read from the
// command table in the claude 2.1.289 binary, leaving out hidden, removed,
// disabled-by-default and undescribed ones. Dialog is set for "local-jsx"
// commands, which render an Ink UI in the terminal.
var builtinCommands = []SlashCommand{
	{Name: "add-dir", Description: "Add a new working directory", ArgumentHint: "<path>", Dialog: true},
	{Name: "advisor", Description: "Let Claude consult a stronger model at key moments", Dialog: true},
	{Name: "artifacts", Description: "Browse your published and shared artifacts", Dialog: true},
	{Name: "autocompact", Description: "Set how full the context gets before auto-summarizing", ArgumentHint: "[auto|<tokens>]", Dialog: true},
	{Name: "background", Description: "Send this session to the background and free the terminal", ArgumentHint: "[prompt]", Aliases: []string{"bg"}, Dialog: true},
	{Name: "branch", Description: "Create a branch of the current conversation at this point", ArgumentHint: "[name]", Dialog: true},
	{Name: "btw", Description: "Ask a quick side question without interrupting the main conversation", ArgumentHint: "[question]", Dialog: true},
	{Name: "cd", Description: "Move this session to a new working directory", ArgumentHint: "<path>", Dialog: true},
	{Name: "clear", Description: "Start a new session with empty context; previous session stays on disk (resumable with /resume)", ArgumentHint: "[name]", Aliases: []string{"reset", "new"}},
	{Name: "color", Description: "Set the prompt bar color for this session", Dialog: true},
	{Name: "compact", Description: "Free up context by summarizing the conversation so far", ArgumentHint: "<optional custom summarization instructions>"},
	{Name: "config", Description: "Open settings", ArgumentHint: "[key=value]", Aliases: []string{"settings"}, Dialog: true},
	{Name: "context", Description: "Visualize current context usage as a colored grid", ArgumentHint: "[all]", Dialog: true},
	{Name: "copy", Description: "Copy Claude's last response to clipboard (or /copy N for the Nth-latest)", Dialog: true},
	{Name: "doctor", Description: "Health-check your setup and fix issues", Aliases: []string{"checkup"}},
	{Name: "effort", Description: "Set effort level for model usage", Dialog: true},
	{Name: "exit", Description: "Exit Claude Code", Aliases: []string{"quit"}},
	{Name: "export", Description: "Export the current conversation to a file or clipboard", ArgumentHint: "[filename]", Dialog: true},
	{Name: "feedback", Description: "Send feedback to Anthropic or report a bug", ArgumentHint: "[report]", Aliases: []string{"bug"}, Dialog: true},
	{Name: "focus", Description: "Toggle focus view: just your prompt, summary, and response", ArgumentHint: "[on|off]", Dialog: true},
	{Name: "fork", Description: "Copy this conversation into a new background session and keep working here", ArgumentHint: "[prompt]", Dialog: true},
	{Name: "goal", Description: "Set a goal Claude checks before stopping", ArgumentHint: "[<condition> | clear]"},
	{Name: "help", Description: "Show help and available commands", Dialog: true},
	{Name: "hooks", Description: "View hook configurations for tool events", Dialog: true},
	{Name: "ide", Description: "Manage IDE integrations and show status", ArgumentHint: "[open]", Dialog: true},
	{Name: "init", Description: "Initialize a new CLAUDE.md file with codebase documentation"},
	{Name: "insights", Description: "Generate a report analyzing your Claude Code sessions"},
	{Name: "login", Description: "Sign in to your Anthropic account", Dialog: true},
	{Name: "logout", Description: "Sign out from your Anthropic account", Dialog: true},
	{Name: "mcp", Description: "Manage MCP servers", ArgumentHint: "[reconnect (<server>|all)|enable|disable [<server>|all]]", Dialog: true},
	{Name: "memory", Description: "Edit CLAUDE.md files and memory settings", Dialog: true},
	{Name: "model", Description: "Set the AI model for Claude Code", ArgumentHint: "[model]", Dialog: true},
	{Name: "output-style", Description: "List output styles or switch to one", ArgumentHint: "[style]"},
	{Name: "permissions", Description: "Manage allow and deny tool permission rules", Aliases: []string{"allowed-tools"}, Dialog: true},
	{Name: "plan", Description: "Enable plan mode or view the current session plan", ArgumentHint: "[open|<description>]", Dialog: true},
	{Name: "plugin", Description: "Manage Claude Code plugins", Aliases: []string{"plugins", "marketplace"}, Dialog: true},
	{Name: "privacy-settings", Description: "View and update your privacy settings", Dialog: true},
	{Name: "recap", Description: "Generate a one-line session recap now"},
	{Name: "release-notes", Description: "View release notes", Dialog: true},
	{Name: "reload-plugins", Description: "Activate pending plugin changes in the current session", ArgumentHint: "[--force]"},
	{Name: "reload-skills", Description: "Pick up skills added or changed on disk during this session"},
	{Name: "rename", Description: "Rename the current conversation", ArgumentHint: "[name]", Aliases: []string{"name"}},
	{Name: "resume", Description: "Resume a previous conversation", ArgumentHint: "[conversation id or search term]", Aliases: []string{"continue"}, Dialog: true},
	{Name: "rewind", Description: "Restore the code and/or conversation to a previous point", Aliases: []string{"checkpoint", "undo"}, Dialog: true},
	{Name: "skill-doctor", Description: "Show which loaded skills are unused and costing context", Dialog: true},
	{Name: "skills", Description: "List available skills", Dialog: true},
	{Name: "status", Description: "Show Claude Code status including version, model, account, API connectivity, and tool statuses", Dialog: true},
	{Name: "statusline", Description: "Set up Claude Code's status line UI"},
	{Name: "tasks", Description: "View and manage everything running in the background", Aliases: []string{"bashes"}, Dialog: true},
	{Name: "team-onboarding", Description: "Help teammates ramp on Claude Code with a guide from your usage"},
	{Name: "theme", Description: "Change the theme", Dialog: true},
	{Name: "usage", Description: "Show session cost, plan usage, and activity stats", Aliases: []string{"cost", "stats"}, Dialog: true},
	{Name: "version", Description: "Show this session's version (autoupdate may have a newer one)"},
	{Name: "workflows", Description: "Browse running and completed workflows", Dialog: true},
}

// SlashCommands lists what "/" completes to in the session sessionID
// running in cwd: the built-ins, every skill the session's transcript
// lists (its skill_listing attachments, which include plugin skills and
// commands), and the custom commands and skills under ~/.claude and the
// checkout's .claude directories — the latter also catch commands hidden
// from the model (disable-model-invocation), which the listing leaves out.
// The result is sorted by name.
func SlashCommands(cwd, sessionID string) []SlashCommand {
	home, _ := os.UserHomeDir()
	var listed []SlashCommand
	if sessionID != "" {
		listed = readSkillListings(filepath.Join(ProjectsDirForPath(cwd), sessionID+".jsonl"))
	}
	var onDisk []SlashCommand
	if home != "" {
		onDisk = append(onDisk, scanCommandDirs(filepath.Join(home, ".claude"), CommandUser)...)
	}
	for _, dir := range projectClaudeDirs(cwd, home) {
		onDisk = append(onDisk, scanCommandDirs(dir, CommandProject)...)
	}
	return mergeCommands(builtinCommands, listed, onDisk)
}

// mergeCommands joins the sources by name; the first one to name a command
// wins, and later ones only fill in what it lacks (a listed skill gets its
// argument hint and user/project source from the file it came from).
func mergeCommands(sources ...[]SlashCommand) []SlashCommand {
	byName := map[string]int{}
	var out []SlashCommand
	for _, src := range sources {
		for _, c := range src {
			i, ok := byName[c.Name]
			if !ok {
				byName[c.Name] = len(out)
				out = append(out, c)
				continue
			}
			have := &out[i]
			if have.Source == CommandBuiltin {
				continue
			}
			if have.Description == "" {
				have.Description = c.Description
			}
			if have.ArgumentHint == "" {
				have.ArgumentHint = c.ArgumentHint
			}
			if c.Source == CommandUser || c.Source == CommandProject {
				have.Source = c.Source
			}
		}
	}
	for i := range out {
		if out[i].Source == "" {
			out[i].Source = CommandBuiltin
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// skillListing is the attachment Claude Code writes when it tells the model
// which skills exist: the first one (isInitial) lists them all, later ones
// add skills that appeared mid-session. content is "- name: description"
// entries, where a description may run over several lines.
type skillListing struct {
	Type    string   `json:"type"`
	Content string   `json:"content"`
	Names   []string `json:"names"`
}

var listingEntry = regexp.MustCompile(`^- ([A-Za-z0-9_.:-]+): ?(.*)$`)

// readSkillListings returns every skill named by the transcript's
// skill_listing attachments, in order.
func readSkillListings(path string) []SlashCommand {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []SlashCommand
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	marker := []byte(`"skill_listing"`)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, marker) {
			continue
		}
		var entry struct {
			Type       string       `json:"type"`
			Attachment skillListing `json:"attachment"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "attachment" || entry.Attachment.Type != "skill_listing" {
			continue
		}
		out = append(out, parseSkillListing(entry.Attachment)...)
	}
	return out
}

func parseSkillListing(l skillListing) []SlashCommand {
	// With names to check against, a continuation line that happens to
	// look like "- word: …" isn't mistaken for a new entry.
	var known map[string]bool
	if len(l.Names) > 0 {
		known = make(map[string]bool, len(l.Names))
		for _, n := range l.Names {
			known[n] = true
		}
	}
	var out []SlashCommand
	for _, line := range strings.Split(l.Content, "\n") {
		if m := listingEntry.FindStringSubmatch(line); m != nil && (known == nil || known[m[1]]) {
			src := CommandSkill
			if strings.Contains(m[1], ":") {
				src = CommandPlugin
			}
			out = append(out, SlashCommand{Name: m[1], Description: m[2], Source: src})
			continue
		}
		if len(out) > 0 && strings.TrimSpace(line) != "" {
			d := &out[len(out)-1].Description
			*d = strings.TrimSpace(*d + "\n" + line)
		}
	}
	return out
}

// projectClaudeDirs is the .claude directory of cwd and of each parent up
// to the repository root (the first directory holding .git), the way
// Claude Code finds project commands. Without a repository only cwd's own
// counts; home's is the user directory, scanned separately.
func projectClaudeDirs(cwd, home string) []string {
	if cwd == "" {
		return nil
	}
	var dirs []string
	for dir := filepath.Clean(cwd); ; {
		if dir != home {
			dirs = append(dirs, filepath.Join(dir, ".claude"))
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dirs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs[:min(len(dirs), 1)]
		}
		dir = parent
	}
}

// scanCommandDirs reads custom commands (<dir>/commands/**/*.md, named by
// file) and skills (<dir>/skills/<name>/SKILL.md) under one .claude dir.
func scanCommandDirs(dir, source string) []SlashCommand {
	var out []SlashCommand
	cmdDir := filepath.Join(dir, "commands")
	_ = filepath.WalkDir(cmdDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		fm := readFrontmatter(path)
		out = append(out, SlashCommand{
			Name:         strings.TrimSuffix(d.Name(), ".md"),
			Description:  fm["description"],
			ArgumentHint: fm["argument-hint"],
			Source:       source,
		})
		return nil
	})
	entries, _ := os.ReadDir(filepath.Join(dir, "skills"))
	for _, e := range entries {
		fm := readFrontmatter(filepath.Join(dir, "skills", e.Name(), "SKILL.md"))
		if fm == nil || fm["user-invocable"] == "false" {
			continue
		}
		name := fm["name"]
		if name == "" {
			name = e.Name()
		}
		out = append(out, SlashCommand{Name: name, Description: fm["description"], ArgumentHint: fm["argument-hint"], Source: source})
	}
	return out
}

// readFrontmatter returns the top-level "key: value" pairs of a markdown
// file's YAML frontmatter — the flat subset command files use, plus
// folded/literal (> or |) and indented continuation values. nil if the
// file is unreadable; empty if it has no frontmatter.
func readFrontmatter(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fm := map[string]string{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return fm
	}
	body, _, ok := strings.Cut(text[4:], "\n---")
	if !ok {
		return fm
	}
	key := ""
	for _, line := range strings.Split(body, "\n") {
		if key != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			fm[key] = strings.TrimSpace(fm[key] + " " + strings.TrimSpace(line))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != k || k == "" {
			key = ""
			continue
		}
		key = k
		v = strings.TrimSpace(v)
		if v == ">" || v == "|" || v == ">-" || v == "|-" {
			v = ""
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		fm[key] = v
	}
	return fm
}
