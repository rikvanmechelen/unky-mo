package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// maxPlanBytes caps a plan file read for the banner.
const maxPlanBytes = 512 << 10

var (
	permRow      = regexp.MustCompile(`^(\s*)(❯)?\s*(\d+)\.\s+(.*?)\s*$`)
	permPlanFile = regexp.MustCompile(`(?:~|/[^\s]*)/\.claude/plans/([A-Za-z0-9._-]+\.md)\b`)
	planFileName = regexp.MustCompile(`^[A-Za-z0-9._-]+\.md$`)
)

const (
	permFeedbackHint = "shift+tab to approve with this feedback"
	// permFeedbackEmpty is the plan's text row while it holds no text.
	permFeedbackEmpty = "Tell Claude what to change"
)

// permRowView is one numbered row of a permission dialog.
type permRowView struct {
	N     int    `json:"n"`
	Label string `json:"label"`
	// Text says how the row takes text: "amend" (Tab, then type),
	// "feedback" (a text field), or "" (none).
	Text string `json:"text"`
	col  int    // where the label starts, for wrapped lines
	hint bool   // has the shift+tab hint under it
}

// permView is what parsePermissionDialog reads from a capture of Claude's
// pane.
type permView struct {
	found    bool
	title    string
	question string
	preview  string // the lines above the question: title, tool preview
	// shown is the preview as the browser gets it: no title, dashed rules
	// or tips — what the dialog shows of the call, for when the state
	// file has no matching content.
	shown    string
	rows     []permRowView
	cursor   int // the row the ❯ is on
	tabAmend bool
	planFile string // base name of the plan file the footer names
}

func (v permView) row(n int) (permRowView, bool) {
	for _, r := range v.rows {
		if r.N == n {
			return r, true
		}
	}
	return permRowView{}, false
}

// sig names this dialog as shown: its title, what it shows of the call
// (the command, the file, the URL, the plan), its question and rows.
// Typing into a row changes its label, and so the sig. The call has to be
// in it: every Bash prompt in a project asks the same question with the
// same rows, so without it an answer meant for one command would approve
// the next one if the first was answered in the terminal meanwhile.
func (v permView) sig() string {
	h := sha256.New()
	io.WriteString(h, v.title)
	io.WriteString(h, "\x00"+v.shown)
	io.WriteString(h, "\x00"+v.question)
	for _, r := range v.rows {
		fmt.Fprintf(h, "\x00%d\x00%s", r.N, r.Label)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isFullRule(line string) bool {
	t := strings.TrimSpace(line)
	return len([]rune(t)) >= 20 && strings.Trim(t, "─") == ""
}

// labelCol is the column a row's label starts at, after "<num>. ".
func labelCol(line, num string) int {
	i := strings.Index(line, num+".") + len(num) + 1
	rest := line[i:]
	return len([]rune(line[:i])) + len(rest) - len(strings.TrimLeft(rest, " "))
}

func indentOf(line string) int {
	return len([]rune(line)) - len([]rune(strings.TrimLeft(line, " ")))
}

// parsePermissionDialog reads a permission prompt from the bottom of
// screen: the question and rows after its last full-width rule.
func parsePermissionDialog(screen string) permView {
	var v permView
	lines := strings.Split(cropSidePanel(screen), "\n")
	var rules []int
	for i, l := range lines {
		if isFullRule(l) {
			rules = append(rules, i)
		}
	}
	if len(rules) == 0 {
		return v
	}
	last := rules[len(rules)-1]
	block := lines[last+1:]

	first := -1
	for i, l := range block {
		if permRow.MatchString(l) {
			first = i
			break
		}
	}
	if first < 0 {
		return v
	}
	var above []string
	for _, l := range block[:first] {
		if strings.TrimSpace(l) != "" {
			above = append(above, strings.TrimSpace(l))
		}
	}
	if len(above) == 0 {
		return v // AskUserQuestion's "Chat about this" after its rule
	}
	v.question = above[len(above)-1]
	above = above[:len(above)-1]
	if len(above) == 0 && len(rules) > 1 {
		// Plan approval: the title and the plan sit between the last two
		// rules.
		for _, l := range lines[rules[len(rules)-2]+1 : last] {
			if strings.TrimSpace(l) != "" {
				above = append(above, strings.TrimSpace(l))
			}
		}
	}
	if len(above) > 0 {
		v.title = above[0]
	}
	v.preview = strings.Join(above, "\n")
	var shown []string
	for i, l := range above {
		if i == 0 || strings.Trim(l, "╌") == "" || strings.HasPrefix(l, "Tip:") {
			continue
		}
		shown = append(shown, l)
	}
	v.shown = strings.Join(shown, "\n")

	var footer []string
	for _, l := range block[first:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := permRow.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[3])
			r := permRowView{N: n, Label: m[4], col: labelCol(l, m[3])}
			if m[2] != "" {
				v.cursor = n
			}
			v.rows = append(v.rows, r)
			continue
		}
		if len(footer) == 0 && len(v.rows) > 0 && indentOf(l) >= v.rows[len(v.rows)-1].col {
			r := &v.rows[len(v.rows)-1]
			t := strings.TrimSpace(l)
			if t == permFeedbackHint {
				r.hint = true
			} else {
				r.Label += " " + t
			}
			continue
		}
		footer = append(footer, strings.TrimSpace(l))
	}
	foot := strings.Join(footer, " ")
	v.tabAmend = strings.Contains(foot, "Tab to amend")
	if m := permPlanFile.FindStringSubmatch(foot); m != nil {
		v.planFile = m[1]
	}
	for i := range v.rows {
		r := &v.rows[i]
		switch {
		case r.hint:
			r.Text = "feedback"
		case v.tabAmend && (r.Label == "Yes" || r.Label == "No"):
			r.Text = "amend"
		}
	}
	v.found = v.cursor != 0
	return v
}

// squash drops all whitespace: a wrapped line on screen breaks words with
// a newline and indentation.
func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// pendingMatches reports whether the dialog asks about this tool call —
// with parallel calls open, the state file's call may be another one.
func pendingMatches(tool string, input json.RawMessage, v permView) bool {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Notebook string `json:"notebook_path"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	shown := squash(v.question + "\n" + v.preview)
	switch tool {
	case "":
		return false
	case "Bash":
		cmd := strings.TrimSpace(strings.SplitN(in.Command, "\n", 2)[0])
		if len(cmd) > 80 {
			cmd = cmd[:80]
		}
		return cmd != "" && strings.Contains(squash(v.preview), squash(cmd))
	case "Edit", "MultiEdit", "Write", "Read", "NotebookEdit":
		p := in.FilePath
		if p == "" {
			p = in.Notebook
		}
		return p != "" && strings.Contains(shown, squash(path.Base(p)))
	case "WebFetch":
		u, err := url.Parse(in.URL)
		return err == nil && u.Hostname() != "" && strings.Contains(shown, u.Hostname())
	case "ExitPlanMode":
		return v.planFile != "" || v.title == "Ready to code?"
	}
	return true
}

// readPlanFile reads a plan named by the dialog's footer: only a .md file
// directly in ~/.claude/plans, never through a symlink.
func readPlanFile(name string) (json.RawMessage, error) {
	if !planFileName.MatchString(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("bad plan file name %q", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(home, ".claude", "plans", name)
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPlanBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPlanBytes {
		return nil, fmt.Errorf("%s is too large", p)
	}
	return json.Marshal(map[string]string{"plan": string(data), "planFilePath": p})
}
