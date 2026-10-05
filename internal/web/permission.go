package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
)

// A permission prompt, as Claude Code (2.1.289) draws it (probed; see
// docs/plans/permission-answers.md and testdata/permission/):
//
//	─────────────────────────      full-width rule opens the dialog
//	 Bash command                  title, then optional subtitle lines
//	╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌      the tool's preview between dashed rules
//	 echo probe > out.txt
//	╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
//	 Do you want to proceed?       the question
//	 ❯ 1. Yes                      rows; a long label wraps, indented
//	   2. Yes, and always allow access to
//	      /some/dir from this project
//	   4. No
//	 Esc to cancel · Tab to amend  footer (none for WebFetch)
//
// Plan approval puts another full-width rule between the plan and the
// question, and its row 3 ("Tell Claude what to change") is a text field
// with a "shift+tab to approve with this feedback" hint under it.
//
// Keys: a digit picks its row at once (No, like Esc, rejects the call and
// ends the turn). "Tab to amend" on the Yes or No row turns it into a text
// field ("Yes, and tell Claude what to do next"); typed text shows in the
// label and Enter answers with it. On the plan's text row a digit only
// moves the cursor; Enter sends the text as feedback, shift+tab approves
// with it. answerPermission re-reads the screen around every key, so a
// dialog that isn't the one the browser answered gets no keys.

var errPermissionLost = errors.New("the permission prompt in Claude's terminal isn't the one shown here (answered there, or it changed); look again")

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

// sig names this dialog as shown: its question and rows. Typing into a
// row changes its label, and so the sig.
func (v permView) sig() string {
	h := sha256.New()
	io.WriteString(h, v.question)
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
	lines := strings.Split(screen, "\n")
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

// permissionRow finds the window's live session row. found is false when
// the window has none.
func (s *Server) permissionRow(windowID string) (state.ProjectState, bool, error) {
	st, err := s.deps.State.Read()
	if err != nil {
		return state.ProjectState{}, false, err
	}
	for _, p := range st.Projects {
		if p.WindowID == windowID && p.SessionID != "" {
			return p, true, nil
		}
	}
	return state.ProjectState{}, false, nil
}

type permDialogJSON struct {
	Title    string        `json:"title"`
	Question string        `json:"question"`
	Rows     []permRowView `json:"rows"`
	Sig      string        `json:"sig"`
}

// handlePermission shows a pending permission prompt: the tool call it's
// about (when the dialog matches it, or the plan file its footer names)
// and the dialog's own question and choices, read from Claude's pane.
func (s *Server) handlePermission(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	row, found, err := s.permissionRow(windowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if row.Status != "permission" {
		writeError(w, http.StatusConflict, fmt.Errorf("no permission prompt is waiting"))
		return
	}
	screen, err := s.deps.ClaudePane.Capture(s.claudeTarget(windowID))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	v := parsePermissionDialog(screen)
	resp := struct {
		Tool   string          `json:"tool,omitempty"`
		Input  json.RawMessage `json:"input,omitempty"`
		Dialog *permDialogJSON `json:"dialog"`
	}{}
	if v.found {
		resp.Dialog = &permDialogJSON{Title: v.title, Question: v.question, Rows: v.rows, Sig: v.sig()}
		if pendingMatches(row.PendingTool, row.PendingInput, v) {
			resp.Tool, resp.Input = row.PendingTool, row.PendingInput
		} else if v.planFile != "" {
			if plan, err := readPlanFile(v.planFile); err == nil {
				resp.Tool, resp.Input = "ExitPlanMode", plan
			}
		}
	} else if row.PendingTool != "" {
		// No dialog to check it against: still the best guess.
		resp.Tool, resp.Input = row.PendingTool, row.PendingInput
	}
	writeJSON(w, resp)
}

// permissionAnswer is the browser's answer: a row of the dialog it saw
// (sig), with text for a row that takes it; approve sends a plan's
// feedback with shift+tab (approve with it) instead of Enter.
type permissionAnswer struct {
	Sig     string `json:"sig"`
	Row     int    `json:"row"`
	Text    string `json:"text"`
	Approve bool   `json:"approve"`
}

// handleAnswerPermission answers a pending permission prompt by driving its
// dialog in Claude's pane, if it's still the one the browser showed.
func (s *Server) handleAnswerPermission(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	var a permissionAnswer
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&a); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	a.Text = strings.Join(strings.Fields(stripControl(a.Text)), " ")
	switch {
	case a.Row < 1 || a.Row > 9:
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad row %d", a.Row))
		return
	case len(a.Text) > maxAnswerText:
		writeError(w, http.StatusBadRequest, fmt.Errorf("the text is longer than %d bytes", maxAnswerText))
		return
	case a.Approve && a.Text == "":
		writeError(w, http.StatusBadRequest, fmt.Errorf("approving with feedback needs the feedback"))
		return
	}
	row, found, err := s.permissionRow(windowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if row.Status != "permission" {
		writeError(w, http.StatusConflict, fmt.Errorf("no permission prompt is waiting"))
		return
	}

	// One dialog-driving request at a time, like mode changes and answers.
	s.modeMu.Lock()
	defer s.modeMu.Unlock()

	err = s.answerPermission(s.claudeTarget(windowID), a)
	var bad badAnswerError
	switch {
	case err == nil:
		writeJSON(w, map[string]any{})
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, errPermissionLost):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadGateway, err)
	}
}

// badAnswerError is an answer that doesn't fit the dialog (a 400).
type badAnswerError struct{ msg string }

func (e badAnswerError) Error() string { return e.msg }

func (s *Server) answerPermission(target string, a permissionAnswer) error {
	v, err := s.waitPermission(target, func(v permView) bool { return v.found })
	if err != nil {
		return err
	}
	if v.sig() != a.Sig {
		return errPermissionLost
	}
	row, ok := v.row(a.Row)
	if !ok {
		return badAnswerError{fmt.Sprintf("the prompt has no choice %d", a.Row)}
	}
	digit := tmux.Key{Text: strconv.Itoa(a.Row)}
	switch {
	case a.Text == "" && row.Text == "feedback":
		return badAnswerError{fmt.Sprintf("choice %d needs text", a.Row)}
	case a.Text == "":
		return s.dialogKeys(target, digit)
	case row.Text == "":
		return badAnswerError{fmt.Sprintf("choice %d takes no text", a.Row)}
	case a.Approve && row.Text != "feedback":
		return badAnswerError{fmt.Sprintf("choice %d can't approve with feedback", a.Row)}
	}

	labelOf := func(v permView) string {
		r, _ := v.row(a.Row)
		return r.Label
	}
	if row.Text == "amend" {
		// Walk the cursor to the row, one step at a time.
		for tries := 0; v.cursor != a.Row; tries++ {
			if tries > len(v.rows)+2 {
				return errPermissionLost
			}
			key, from := tmux.Key{Name: "Down"}, v.cursor
			if v.cursor > a.Row {
				key.Name = "Up"
			}
			if err := s.dialogKeys(target, key); err != nil {
				return err
			}
			if v, err = s.waitPermission(target, func(v permView) bool { return v.found && v.cursor != from }); err != nil {
				return err
			}
		}
		if labelOf(v) != row.Label {
			return errPermissionLost
		}
		if err := s.dialogKeys(target, tmux.Key{Name: "Tab"}); err != nil {
			return err
		}
	} else {
		// The plan's text row: its digit only moves the cursor there. Text
		// already in it (the terminal user's) would get ours appended.
		if row.Label != permFeedbackEmpty {
			return errPermissionLost
		}
		if err := s.dialogKeys(target, digit); err != nil {
			return err
		}
	}
	// Wait for the field: opened (amend: the label changed) or reached.
	opened, err := s.waitPermission(target, func(v permView) bool {
		return v.found && v.cursor == a.Row && (row.Text == "feedback" || labelOf(v) != row.Label)
	})
	if err != nil {
		return err
	}
	placeholder := labelOf(opened)
	if err := s.dialogKeys(target, tmux.Key{Text: a.Text}); err != nil {
		return err
	}
	if _, err := s.waitPermission(target, func(v permView) bool {
		return v.found && v.cursor == a.Row && labelOf(v) != placeholder
	}); err != nil {
		return err
	}
	final := tmux.Key{Name: "Enter"}
	if a.Approve {
		final.Name = "BTab"
	}
	return s.dialogKeys(target, final)
}

// waitPermission re-reads the dialog until ok holds, or gives up with
// errPermissionLost.
func (s *Server) waitPermission(target string, ok func(permView) bool) (permView, error) {
	for i := 0; i < answerSettleTries; i++ {
		screen, err := s.deps.ClaudePane.Capture(target)
		if err != nil {
			return permView{}, err
		}
		if v := parsePermissionDialog(screen); ok(v) {
			return v, nil
		}
		time.Sleep(s.modeSettle)
	}
	return permView{}, errPermissionLost
}
