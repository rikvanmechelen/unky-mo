package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

// fakeDialog behaves like Claude Code's AskUserQuestion dialog (as probed
// against 2.1.289; see answer.go): it draws the screen and reacts to keys.
type fakeDialog struct {
	qs      []questionSpec
	tab     int // len(qs) = the review page
	cursor  int // 1-based row, len(options)+2 = Next
	checked []map[int]bool
	other   []string
	picked  []int // single-select: the chosen row
	done    bool
	keys    []string
	above   string // transcript lines above the dialog
}

func newFakeDialog(qs []questionSpec) *fakeDialog {
	d := &fakeDialog{qs: qs, cursor: 1}
	for range qs {
		d.checked = append(d.checked, map[int]bool{})
		d.other = append(d.other, "")
		d.picked = append(d.picked, 0)
	}
	return d
}

func (d *fakeDialog) tabbed() bool { return len(d.qs) > 1 || d.qs[0].MultiSelect }

func (d *fakeDialog) screen() string {
	var b strings.Builder
	b.WriteString(d.above + "\n" + rule + "\n")
	if d.done {
		b.WriteString("● User answered Claude's questions:\n" + rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on\n")
		return b.String()
	}
	tabs := []string{}
	for i, q := range d.qs {
		_ = q
		box := "☐"
		if len(d.checked[i]) > 0 || d.other[i] != "" || d.picked[i] != 0 {
			box = "☒"
		}
		tabs = append(tabs, box+" Q"+strconv.Itoa(i))
	}
	if d.tabbed() {
		b.WriteString("←  " + strings.Join(tabs, "  ") + "  ✔ Submit  →\n")
	} else {
		b.WriteString(" " + tabs[0] + "\n")
	}
	if d.tab == len(d.qs) {
		b.WriteString("Review your answers\nReady to submit your answers?\n❯ 1. Submit answers\n  2. Cancel\n")
		return b.String()
	}
	q := d.qs[d.tab]
	b.WriteString(q.Question + "\n")
	n := len(q.Options)
	mark := func(k int) string {
		if d.cursor == k {
			return "❯ "
		}
		return "  "
	}
	for k := 1; k <= n+1; k++ {
		label := "Type something"
		if k <= n {
			label = q.Options[k-1].Label
		} else if d.other[d.tab] != "" {
			label = d.other[d.tab]
		} else if !q.MultiSelect {
			label += "."
		}
		if q.MultiSelect {
			box := "[ ]"
			if d.checked[d.tab][k] {
				box = "[✔]"
			}
			fmt.Fprintf(&b, "%s%d. %s %s\n", mark(k), k, box, label)
		} else {
			fmt.Fprintf(&b, "%s%d. %s\n", mark(k), k, label)
		}
		if k <= n {
			b.WriteString("         a description\n")
		}
	}
	if q.MultiSelect {
		next := "Next"
		if d.tab == len(d.qs)-1 {
			next = "Submit"
		}
		fmt.Fprintf(&b, "%s   %s\n", mark(n+2), next)
	}
	b.WriteString(rule + "\n  " + strconv.Itoa(n+2) + ". Chat about this\nEnter to select · Esc to cancel\n")
	return b.String()
}

func (d *fakeDialog) advance() {
	if !d.tabbed() {
		d.done = true
		return
	}
	d.tab++
	d.cursor = 1
}

func (d *fakeDialog) press(k tmux.Key) {
	d.keys = append(d.keys, k.Name+k.Text)
	if d.done {
		return
	}
	if d.tab == len(d.qs) {
		if k.Text == "1" {
			d.done = true
		}
		return
	}
	q := d.qs[d.tab]
	n := len(q.Options)
	last := n + 1
	if q.MultiSelect {
		last = n + 2
	}
	typing := d.cursor == n+1
	switch {
	case k.Name == "Up":
		if d.cursor > 1 {
			d.cursor--
		}
	case k.Name == "Down":
		if d.cursor < last {
			d.cursor++
		}
	case k.Name == "Enter":
		if q.MultiSelect && d.cursor == n+2 {
			d.advance()
		} else if !q.MultiSelect && typing && d.other[d.tab] != "" {
			d.picked[d.tab] = n + 1
			d.advance()
		}
	case typing:
		d.other[d.tab] += k.Text
		if q.MultiSelect {
			d.checked[d.tab][n+1] = true
		}
	default:
		row, err := strconv.Atoi(k.Text)
		if err != nil || row < 1 || row > n+1 {
			return
		}
		switch {
		case row == n+1 && !q.MultiSelect:
			d.cursor = row
		case q.MultiSelect:
			d.checked[d.tab][row] = !d.checked[d.tab][row]
			if !d.checked[d.tab][row] {
				delete(d.checked[d.tab], row)
			}
		default:
			d.picked[d.tab] = row
			d.advance()
		}
	}
}

func answerServer(t *testing.T, row state.ProjectState, d *fakeDialog) *Server {
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

func questionRow(t *testing.T, qs []questionSpec) state.ProjectState {
	input, err := json.Marshal(map[string]any{"questions": qs})
	if err != nil {
		t.Fatal(err)
	}
	return state.ProjectState{WindowID: "@4", SessionID: "s1", Status: "question",
		PendingTool: "AskUserQuestion", PendingInput: input}
}

func spec(question string, multi bool, labels ...string) questionSpec {
	q := questionSpec{Question: question, MultiSelect: multi}
	for _, l := range labels {
		q.Options = append(q.Options, struct {
			Label string `json:"label"`
		}{l})
	}
	return q
}

func postAnswer(srv *Server, questions []string, answers []questionAnswer) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"questions": questions, "answers": answers})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/@4/answer", strings.NewReader(string(body))))
	return rec
}

func TestAnswerDrivesDialog(t *testing.T) {
	qs := []questionSpec{
		spec("Which fruits do you like?", true, "Apple", "Banana", "Cherry"),
		spec("Which size?", false, "Small", "Large"),
		spec("Anything else?", false, "No", "Yes"),
		spec("Which colors?", true, "Red", "Green"),
	}
	d := newFakeDialog(qs)
	d.above = "  ☐ a todo item\n  ☒ another"
	srv := answerServer(t, questionRow(t, qs), d)
	rec := postAnswer(srv, []string{qs[0].Question, qs[1].Question, qs[2].Question, qs[3].Question}, []questionAnswer{
		{Options: []int{0, 2}, Other: "Mango,\nkiwi"},
		{Options: []int{1}},
		{Other: "Maybe"},
		{Options: []int{1}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (keys %q)", rec.Code, rec.Body, d.keys)
	}
	if !d.done {
		t.Fatalf("dialog not submitted; keys %q", d.keys)
	}
	if want := []map[int]bool{{1: true, 3: true, 4: true}, {}, {}, {2: true}}; !reflect.DeepEqual(d.checked, want) {
		t.Errorf("checked = %v, want %v", d.checked, want)
	}
	if want := []string{"Mango, kiwi", "", "Maybe", ""}; !reflect.DeepEqual(d.other, want) {
		t.Errorf("other = %q, want %q", d.other, want)
	}
	if d.picked[1] != 2 || d.picked[2] != 3 {
		t.Errorf("picked = %v", d.picked)
	}
}

func TestAnswerSingleQuestionNeedsNoReview(t *testing.T) {
	qs := []questionSpec{spec("Which pet?", false, "Cat", "Dog")}
	d := newFakeDialog(qs)
	srv := answerServer(t, questionRow(t, qs), d)
	if rec := postAnswer(srv, []string{"Which pet?"}, []questionAnswer{{Options: []int{1}}}); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !d.done || d.picked[0] != 2 || !reflect.DeepEqual(d.keys, []string{"2"}) {
		t.Fatalf("done %v picked %v keys %q", d.done, d.picked, d.keys)
	}
}

func TestAnswerFixesTerminalState(t *testing.T) {
	// The terminal user moved the cursor and ticked a box first.
	qs := []questionSpec{spec("Which colors?", true, "Red", "Green", "Blue")}
	d := newFakeDialog(qs)
	d.cursor = 3
	d.checked[0][1] = true
	srv := answerServer(t, questionRow(t, qs), d)
	if rec := postAnswer(srv, []string{"Which colors?"}, []questionAnswer{{Options: []int{1, 2}}}); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (keys %q)", rec.Code, rec.Body, d.keys)
	}
	if want := map[int]bool{2: true, 3: true}; !d.done || !reflect.DeepEqual(d.checked[0], want) {
		t.Fatalf("done %v checked %v keys %q", d.done, d.checked[0], d.keys)
	}
}

func TestAnswerStopsWhenDialogMoved(t *testing.T) {
	qs := []questionSpec{spec("Q one?", false, "A", "B"), spec("Q two?", false, "C", "D")}
	d := newFakeDialog(qs)
	d.tab = 1 // answered the first one in the terminal
	srv := answerServer(t, questionRow(t, qs), d)
	rec := postAnswer(srv, []string{"Q one?", "Q two?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{0}}})
	if rec.Code != http.StatusConflict || len(d.keys) != 0 {
		t.Fatalf("status %d, keys %q", rec.Code, d.keys)
	}

	// Text the terminal user already typed isn't appended to.
	qs = []questionSpec{spec("Q?", false, "A", "B")}
	d = newFakeDialog(qs)
	d.other[0] = "theirs"
	srv = answerServer(t, questionRow(t, qs), d)
	if rec := postAnswer(srv, []string{"Q?"}, []questionAnswer{{Other: "mine"}}); rec.Code != http.StatusConflict || d.other[0] != "theirs" {
		t.Fatalf("status %d, other %q", rec.Code, d.other[0])
	}
}

func TestAnswerRefusals(t *testing.T) {
	qs := []questionSpec{spec("Q?", false, "A", "B"), spec("M?", true, "C", "D")}
	cases := []struct {
		name      string
		row       func(state.ProjectState) state.ProjectState
		questions []string
		answers   []questionAnswer
		want      int
	}{
		{"question changed", nil, []string{"Old?", "M?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{0}}}, http.StatusConflict},
		{"not waiting", func(r state.ProjectState) state.ProjectState { r.Status = "idle"; return r },
			[]string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{0}}}, http.StatusConflict},
		{"other tool", func(r state.ProjectState) state.ProjectState { r.PendingTool = "Other"; return r },
			[]string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{0}}}, http.StatusConflict},
		{"no session", func(r state.ProjectState) state.ProjectState { r.WindowID = "@9"; return r },
			[]string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{0}}}, http.StatusNotFound},
		{"missing answer", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}}, http.StatusBadRequest},
		{"empty answer", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Other: " \n"}}, http.StatusBadRequest},
		{"two for single", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0, 1}}, {Options: []int{0}}}, http.StatusBadRequest},
		{"option and text for single", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}, Other: "x"}, {Options: []int{0}}}, http.StatusBadRequest},
		{"out of range", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{2}}, {Options: []int{0}}}, http.StatusBadRequest},
		{"duplicate", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Options: []int{1, 1}}}, http.StatusBadRequest},
		{"text too long", nil, []string{"Q?", "M?"}, []questionAnswer{{Options: []int{0}}, {Other: strings.Repeat("x", maxAnswerText+1)}}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := questionRow(t, qs)
			if c.row != nil {
				row = c.row(row)
			}
			// No pane expectations: a refusal must send no keys.
			srv := answerServer(t, row, nil)
			if rec := postAnswer(srv, c.questions, c.answers); rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestParseDialogIgnoresTodoList(t *testing.T) {
	v := parseDialog("● Tasks\n  ☐ write tests\n  ☒ build it\n" + rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on")
	if v.found {
		t.Fatalf("a todo list read as a dialog: %+v", v)
	}
}
