package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "permission", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type wantRow struct {
	label string // a prefix when it ends in "…"
	text  string
}

// The captures from probing Claude Code 2.1.289 (step 1 of
// docs/plans/permission-answers.md).
func TestParsePermissionDialogFixtures(t *testing.T) {
	alwaysAllow := wantRow{"Yes, and always allow access to /tmp/…", ""}
	autoMode := wantRow{"Yes, and switch to auto mode · auto mode handles these prompts for you", ""}
	acceptEdits := wantRow{"Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", ""}
	planQ := "Claude has written up a plan and is ready to execute. Would you like to proceed?"
	cases := []struct {
		file, title, question string
		cursor                int
		planFile              string
		rows                  []wantRow
	}{
		{"bash.txt", "Bash command", "Do you want to proceed?", 1, "",
			[]wantRow{{"Yes", "amend"}, alwaysAllow, autoMode, {"No", "amend"}}},
		// Amended: the row shows the text, the footer drops "Tab to amend".
		{"bash-amend-yes.txt", "Bash command", "Do you want to proceed?", 1, "",
			[]wantRow{{"Yes, then say done", ""}, alwaysAllow, autoMode, {"No", ""}}},
		{"bash-amend-no.txt", "Bash command", "Do you want to proceed?", 4, "",
			[]wantRow{{"Yes", ""}, alwaysAllow, autoMode, {"No, use printf instead", ""}}},
		{"edit.txt", "Edit file", "Do you want to make this edit to notes.txt?", 1, "",
			[]wantRow{{"Yes", "amend"}, acceptEdits, {"No", "amend"}}},
		{"write.txt", "Create file", "Do you want to create new.txt?", 1, "",
			[]wantRow{{"Yes", "amend"}, acceptEdits, {"No", "amend"}}},
		{"read-outside.txt", "Read file", "Do you want to proceed?", 1, "",
			[]wantRow{{"Yes", "amend"}, {"Yes, allow reading from /etc during this session", ""}, {"No", "amend"}}},
		{"webfetch.txt", "Fetch", "Do you want to allow Claude to fetch this content?", 1, "",
			[]wantRow{{"Yes", ""}, {"Yes, and don't ask again for example.com", ""}, {"No, and tell Claude what to do differently (esc)", ""}}},
		{"plan.txt", "Ready to code?", planQ, 1, "snazzy-churning-truffle.md",
			[]wantRow{{"Yes, and use auto mode", ""}, {"Yes, manually approve edits", ""}, {"Tell Claude what to change", "feedback"}}},
		{"plan-feedback.txt", "Ready to code?", planQ, 3, "snazzy-churning-truffle.md",
			[]wantRow{{"Yes, and use auto mode", ""}, {"Yes, manually approve edits", ""}, {"skip the verify step", "feedback"}}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			v := parsePermissionDialog(readFixture(t, c.file))
			if !v.found || v.title != c.title || v.question != c.question || v.cursor != c.cursor || v.planFile != c.planFile {
				t.Fatalf("got found=%v title=%q question=%q cursor=%d planFile=%q", v.found, v.title, v.question, v.cursor, v.planFile)
			}
			if len(v.rows) != len(c.rows) {
				t.Fatalf("got %d rows: %+v", len(v.rows), v.rows)
			}
			for i, w := range c.rows {
				r := v.rows[i]
				ok := r.Label == w.label
				if p, cut := strings.CutSuffix(w.label, "…"); cut {
					ok = strings.HasPrefix(r.Label, p) && strings.HasSuffix(r.Label, " from this project")
				}
				if r.N != i+1 || !ok || r.Text != w.text {
					t.Errorf("row %d = %d %q text=%q, want %q text=%q", i+1, r.N, r.Label, r.Text, w.label, w.text)
				}
			}
		})
	}
}

func TestParsePermissionDialogNotAPrompt(t *testing.T) {
	idle := rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on · ? for shortcuts\n"
	ask := newFakeDialog([]questionSpec{spec("Which pet?", false, "Cat", "Dog")}).screen()
	for name, screen := range map[string]string{
		"trust dialog":    readFixture(t, "trust-folder.txt"),
		"idle":            idle,
		"AskUserQuestion": ask,
		"empty":           "",
	} {
		if v := parsePermissionDialog(screen); v.found {
			t.Errorf("%s parsed as a permission prompt: %+v", name, v)
		}
	}
}

// The plan in a real session sits in a scroll box ending in ↓.
func TestParsePermissionDialogScrolledPlan(t *testing.T) {
	screen := "● Updated plan\n" + rule + "\n Ready to code?\n Here is Claude's plan:\n" +
		" ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\n # A plan\n   1. step one\n   2. step two          ↓\n" + rule + "\n" +
		" Claude has written up a plan and is ready to execute. Would you like to proceed?\n\n" +
		" ❯ 1. Yes, and use auto mode\n   2. Yes, manually approve edits\n   3. Tell Claude what to change\n" +
		"      shift+tab to approve with this feedback\n\n ctrl+g to edit in VS Code · ~/.claude/plans/a-b.md\n"
	v := parsePermissionDialog(screen)
	if !v.found || v.title != "Ready to code?" || len(v.rows) != 3 || v.rows[2].Text != "feedback" || v.planFile != "a-b.md" {
		t.Fatalf("got %+v", v)
	}
	if !strings.Contains(v.preview, "1. step one") {
		t.Errorf("plan's own numbered lines should be preview, got %q", v.preview)
	}
}

func TestPendingMatches(t *testing.T) {
	bash := parsePermissionDialog(readFixture(t, "bash.txt"))
	edit := parsePermissionDialog(readFixture(t, "edit.txt"))
	fetch := parsePermissionDialog(readFixture(t, "webfetch.txt"))
	plan := parsePermissionDialog(readFixture(t, "plan.txt"))
	wrapped := bash
	wrapped.preview = "Bash command\nfind . -name '*.go' -newer go.mod -exec grep -l\n  pattern {} +"
	cases := []struct {
		name  string
		tool  string
		input string
		v     permView
		want  bool
	}{
		{"bash", "Bash", `{"command":"echo probe > out.txt"}`, bash, true},
		{"bash, a parallel call's command", "Bash", `{"command":"echo other > x"}`, bash, false},
		{"bash wrapped on screen", "Bash", `{"command":"find . -name '*.go' -newer go.mod -exec grep -l pattern {} +"}`, wrapped, true},
		{"edit by base name", "Edit", `{"file_path":"/tmp/x/notes.txt"}`, edit, true},
		{"edit, another file", "Edit", `{"file_path":"/tmp/x/other.txt"}`, edit, false},
		{"webfetch by host", "WebFetch", `{"url":"https://example.com/a?b"}`, fetch, true},
		{"webfetch, another host", "WebFetch", `{"url":"https://example.org/"}`, fetch, false},
		{"plan", "ExitPlanMode", `{"plan":"x"}`, plan, true},
		{"plan for a bash prompt", "ExitPlanMode", `{"plan":"x"}`, bash, false},
		{"unknown tool", "mcp__x__y", `{}`, bash, true},
		{"nothing pending", "", ``, bash, false},
	}
	for _, c := range cases {
		if got := pendingMatches(c.tool, json.RawMessage(c.input), c.v); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReadPlanFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plans := filepath.Join(home, ".claude", "plans")
	if err := os.MkdirAll(plans, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(plans, "a-plan.md"), []byte("# Plan\n"), 0o644)
	os.WriteFile(filepath.Join(home, "secret.md"), []byte("secret"), 0o644)
	os.Symlink(filepath.Join(home, "secret.md"), filepath.Join(plans, "link.md"))
	os.WriteFile(filepath.Join(plans, "notes.txt"), []byte("x"), 0o644)

	got, err := readPlanFile("a-plan.md")
	var in struct{ Plan, PlanFilePath string }
	if err != nil || json.Unmarshal(got, &in) != nil || in.Plan != "# Plan\n" || in.PlanFilePath != filepath.Join(plans, "a-plan.md") {
		t.Fatalf("got %s, %v", got, err)
	}
	for _, name := range []string{"link.md", "../secret.md", "sub/a-plan.md", "notes.txt", "..md", ".md", "missing.md"} {
		if got, err := readPlanFile(name); err == nil {
			t.Errorf("%q read: %s", name, got)
		}
	}
}

// fakePermission behaves like Claude Code's permission prompt (as probed
// against 2.1.289): it draws the dialog and reacts to keys.
type fakePermission struct {
	title, preview, question string
	plan                     bool // plan approval: another rule above the question
	labels                   []string
	tabAmend                 bool
	cursor                   int
	editing                  int    // the row Tab opened, or the plan's text row
	typed                    string // text in it
	answer                   string // how it was answered, once done
	keys                     []string
}

func newFakeBash() *fakePermission {
	return &fakePermission{title: "Bash command", preview: "echo probe > out.txt", question: "Do you want to proceed?",
		labels: []string{"Yes", "Yes, and always allow access to /tmp/x from this project", "Yes, and switch to auto mode", "No"}, tabAmend: true, cursor: 1}
}

func newFakePlan() *fakePermission {
	return &fakePermission{title: "Ready to code?", preview: "# Plan\n- step", plan: true,
		question: "Claude has written up a plan and is ready to execute. Would you like to proceed?",
		labels:   []string{"Yes, and use auto mode", "Yes, manually approve edits", permFeedbackEmpty}, cursor: 1}
}

func (d *fakePermission) label(n int) string {
	l := d.labels[n-1]
	switch {
	case d.plan && n == 3 && d.typed != "":
		return d.typed
	case d.editing == n && !d.plan && l == "Yes":
		if d.typed == "" {
			return "Yes, and tell Claude what to do next"
		}
		return "Yes, " + d.typed
	case d.editing == n && !d.plan && l == "No":
		if d.typed == "" {
			return "No, and tell Claude what to do differently"
		}
		return "No, " + d.typed
	}
	return l
}

func (d *fakePermission) screen() string {
	var b strings.Builder
	b.WriteString("● Bash(…)\n  ⎿  Waiting…\n\n" + rule + "\n")
	if d.answer != "" {
		b.WriteString("❯ \n" + rule + "\n  ⏸ manual mode on\n")
		return b.String()
	}
	fmt.Fprintf(&b, " %s\n ╌╌╌╌╌╌╌╌╌╌\n %s\n ╌╌╌╌╌╌╌╌╌╌\n", d.title, strings.ReplaceAll(d.preview, "\n", "\n "))
	if d.plan {
		b.WriteString(rule + "\n")
	}
	fmt.Fprintf(&b, " %s\n\n", d.question)
	for n := range d.labels {
		mark := "  "
		if d.cursor == n+1 {
			mark = "❯ "
		}
		fmt.Fprintf(&b, " %s%d. %s\n", mark, n+1, d.label(n+1))
		if d.plan && n+1 == 3 {
			b.WriteString("      " + permFeedbackHint + "\n")
		}
	}
	switch {
	case d.plan:
		b.WriteString("\n ctrl+g to edit in VS Code · ~/.claude/plans/a-plan.md\n")
	case d.tabAmend && d.editing == 0:
		b.WriteString("\n Esc to cancel · Tab to amend\n")
	default:
		b.WriteString("\n Esc to cancel\n")
	}
	return b.String()
}

func (d *fakePermission) press(k tmux.Key) {
	d.keys = append(d.keys, k.Name+k.Text)
	if d.answer != "" {
		return
	}
	textRow := d.editing != 0 || (d.plan && d.cursor == 3)
	switch {
	case k.Name == "Up" && d.cursor > 1:
		d.cursor--
	case k.Name == "Down" && d.cursor < len(d.labels):
		d.cursor++
	case k.Name == "Tab" && !d.plan:
		if l := d.labels[d.cursor-1]; l == "Yes" || l == "No" {
			d.editing = d.cursor
		}
	case k.Name == "Enter":
		d.answer = fmt.Sprintf("%d:%s", d.cursor, d.typed)
	case k.Name == "BTab" && d.plan && d.typed != "":
		d.answer = "approve:" + d.typed
	case k.Name != "":
	case textRow:
		d.typed += k.Text
	default:
		n, err := strconv.Atoi(k.Text)
		if err != nil || n < 1 || n > len(d.labels) {
			return
		}
		if d.plan && n == 3 {
			d.cursor = 3
			return
		}
		d.answer = strconv.Itoa(n)
	}
}

func permissionServer(t *testing.T, row state.ProjectState, d *fakePermission) *Server {
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{row}}, nil).AnyTimes()
	pane := mock_web.NewMockClaudePane(ctrl)
	if d != nil {
		pane.EXPECT().Capture("mo:@4.0").DoAndReturn(func(string) (string, error) { return d.screen(), nil }).AnyTimes()
		pane.EXPECT().SendKeys("mo:@4.0", gomock.Any()).DoAndReturn(func(_ string, keys []tmux.Key) error {
			for _, k := range keys {
				d.press(k)
			}
			return nil
		}).AnyTimes()
	}
	srv := NewServer(Deps{State: st, ClaudePane: pane}, 0, "mo")
	srv.modeSettle = 0
	return srv
}

func permissionRow(status, tool, input string) state.ProjectState {
	r := state.ProjectState{WindowID: "@4", SessionID: "s1", Status: status, PendingTool: tool}
	if input != "" {
		r.PendingInput = json.RawMessage(input)
	}
	return r
}

type permGET struct {
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Dialog *permDialogJSON `json:"dialog"`
}

func getPermission(t *testing.T, srv *Server) (int, permGET) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/@4/permission", nil))
	var got permGET
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, got
}

func postPermission(srv *Server, a permissionAnswer) *httptest.ResponseRecorder {
	body, _ := json.Marshal(a)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/@4/permission", strings.NewReader(string(body))))
	return rec
}

func TestGetPermission(t *testing.T) {
	d := newFakeBash()
	srv := permissionServer(t, permissionRow("permission", "Bash", `{"command":"echo probe > out.txt"}`), d)
	code, got := getPermission(t, srv)
	if code != http.StatusOK || got.Tool != "Bash" || got.Dialog == nil {
		t.Fatalf("%d %+v", code, got)
	}
	if got.Dialog.Question != "Do you want to proceed?" || len(got.Dialog.Rows) != 4 || got.Dialog.Rows[3].Text != "amend" || got.Dialog.Sig == "" {
		t.Errorf("dialog %+v", got.Dialog)
	}

	// A parallel call's command: the dialog isn't about it.
	srv = permissionServer(t, permissionRow("permission", "Bash", `{"command":"rm -rf build"}`), newFakeBash())
	if _, got := getPermission(t, srv); got.Tool != "" || got.Input != nil || got.Dialog == nil {
		t.Errorf("unmatched call shown: %+v", got)
	}
	if len(d.keys) != 0 {
		t.Errorf("GET pressed keys %q", d.keys)
	}
}

func TestGetPermissionReadsThePlanFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude", "plans"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "plans", "a-plan.md"), []byte("# The plan\n"), 0o644)
	srv := permissionServer(t, permissionRow("permission", "", ""), newFakePlan())
	_, got := getPermission(t, srv)
	var in struct{ Plan string }
	if got.Tool != "ExitPlanMode" || json.Unmarshal(got.Input, &in) != nil || in.Plan != "# The plan\n" {
		t.Fatalf("got %+v", got)
	}
}

func TestGetPermissionRefusals(t *testing.T) {
	srv := permissionServer(t, permissionRow("question", "", ""), nil)
	if code, _ := getPermission(t, srv); code != http.StatusConflict {
		t.Errorf("question: %d, want 409", code)
	}
	srv = permissionServer(t, state.ProjectState{WindowID: "@9", SessionID: "s1", Status: "permission"}, nil)
	if code, _ := getPermission(t, srv); code != http.StatusNotFound {
		t.Errorf("other window: %d, want 404", code)
	}
}

func sigOf(d *fakePermission) string { return parsePermissionDialog(d.screen()).sig() }

func TestAnswerPermission(t *testing.T) {
	cases := []struct {
		name    string
		fake    func() *fakePermission
		answer  func(sig string) permissionAnswer
		want    string
		keys    []string
		setupFn func(*fakePermission)
	}{
		{"a digit", newFakeBash, func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 2} },
			"2", []string{"2"}, nil},
		{"amend No: walk down, Tab, type, Enter", newFakeBash,
			func(sig string) permissionAnswer {
				return permissionAnswer{Sig: sig, Row: 4, Text: "use printf\ninstead"}
			},
			"4:use printf instead", []string{"Down", "Down", "Down", "Tab", "use printf instead", "Enter"}, nil},
		{"amend Yes from a moved cursor", newFakeBash,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 1, Text: "then say done"} },
			"1:then say done", []string{"Up", "Tab", "then say done", "Enter"}, func(d *fakePermission) { d.cursor = 2 }},
		{"plan feedback", newFakePlan,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 3, Text: "skip verify"} },
			"3:skip verify", []string{"3", "skip verify", "Enter"}, nil},
		{"plan approve with feedback", newFakePlan,
			func(sig string) permissionAnswer {
				return permissionAnswer{Sig: sig, Row: 3, Text: "keep it tiny", Approve: true}
			},
			"approve:keep it tiny", []string{"3", "keep it tiny", "BTab"}, nil},
		{"plan row 1", newFakePlan, func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 1} },
			"1", []string{"1"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.fake()
			if c.setupFn != nil {
				c.setupFn(d)
			}
			srv := permissionServer(t, permissionRow("permission", "", ""), d)
			rec := postPermission(srv, c.answer(sigOf(d)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s (keys %q)", rec.Code, rec.Body, d.keys)
			}
			if d.answer != c.want || !reflect.DeepEqual(d.keys, c.keys) {
				t.Errorf("answer %q keys %q, want %q %q", d.answer, d.keys, c.want, c.keys)
			}
		})
	}
}

func TestAnswerPermissionRefusals(t *testing.T) {
	long := strings.Repeat("x", maxAnswerText+1)
	cases := []struct {
		name   string
		status string
		fake   func() *fakePermission
		setup  func(*fakePermission)
		answer func(sig string) permissionAnswer
		code   int
	}{
		{"stale sig", "permission", newFakeBash, nil,
			func(string) permissionAnswer { return permissionAnswer{Sig: "old", Row: 1} }, http.StatusConflict},
		{"not a permission prompt", "active", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 1} }, http.StatusConflict},
		{"no dialog on screen", "permission", newFakeBash, func(d *fakePermission) { d.answer = "1" },
			func(string) permissionAnswer { return permissionAnswer{Sig: "x", Row: 1} }, http.StatusConflict},
		// The terminal user already typed into the plan's field; the browser
		// saw that dialog, but ours would be appended.
		{"plan field holds text", "permission", newFakePlan, func(d *fakePermission) { d.typed = "theirs"; d.cursor = 3 },
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 3, Text: "mine"} }, http.StatusConflict},
		{"row out of range", "permission", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 0} }, http.StatusBadRequest},
		{"row not in the dialog", "permission", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 7} }, http.StatusBadRequest},
		{"text on a plain row", "permission", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 2, Text: "x"} }, http.StatusBadRequest},
		{"feedback row without text", "permission", newFakePlan, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 3} }, http.StatusBadRequest},
		{"approve without text", "permission", newFakePlan, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 3, Approve: true} }, http.StatusBadRequest},
		{"approve on an amend row", "permission", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 1, Text: "x", Approve: true} }, http.StatusBadRequest},
		{"text too long", "permission", newFakeBash, nil,
			func(sig string) permissionAnswer { return permissionAnswer{Sig: sig, Row: 1, Text: long} }, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.fake()
			if c.setup != nil {
				c.setup(d)
			}
			srv := permissionServer(t, permissionRow(c.status, "", ""), d)
			rec := postPermission(srv, c.answer(sigOf(d)))
			if rec.Code != c.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.code, rec.Body)
			}
			if len(d.keys) != 0 {
				t.Errorf("keys sent: %q", d.keys)
			}
		})
	}
}
