package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/tmux"
)

// AskUserQuestion's dialog, as Claude Code (2.1.289) draws it:
//
//	←  ☒ Fruit  ☐ Size  ✔ Submit  →      tabs (just " ☐ Pet" for one
//	Which fruits do you like?             single-select question)
//	❯ 1. [✔] Apple                        multiSelect rows have a box
//	         A crisp, sweet fruit
//	  4. [ ] Type something               free text, typed in place
//	     Next                             "Submit" on the last tab
//	───────
//	  5. Chat about this
//
// Its keys, found by driving it: a digit toggles that row of a multiSelect
// question without moving the cursor, and picks that row of a single-select
// one (moving on to the next tab). On "Type something" a digit only moves
// the cursor there; typed text fills it in and checks it. ↑/↓ move the
// cursor (↓ from the last row reaches Next), Enter on Next or after typed
// text moves on. With tabs, the last one is a "Review your answers" page
// whose "1" submits. answerQuestion presses those keys one question at a
// time and reads the screen back after each step, so a dialog that isn't
// where it should be (the terminal user answering at the same time, a
// changed layout) stops the answer rather than sending keys blind.

// questionSpec is one question of AskUserQuestion's input.
type questionSpec struct {
	Question string `json:"question"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options"`
	MultiSelect bool `json:"multiSelect"`
}

// questionAnswer is the browser's answer to one question: option indices
// (0-based) and/or the "Type something" text.
type questionAnswer struct {
	Options []int  `json:"options"`
	Other   string `json:"other"`
}

// maxAnswerText bounds one "Type something" answer.
const maxAnswerText = 2000

// answerSettleTries bounds each wait for the dialog to redraw
// (tries × Server.modeSettle).
const answerSettleTries = 80

var errDialogLost = errors.New("the question in Claude's terminal isn't where the web expected (answered there, or the dialog changed); finish it in the terminal")

var (
	dialogTabs = regexp.MustCompile(`^\s*(?:←\s+)?[☐☒]\s`)
	dialogRow  = regexp.MustCompile(`^\s*(❯)?\s*(\d+)\.\s+(?:\[(.)\]\s+)?(.*?)\s*$`)
	dialogNext = regexp.MustCompile(`^\s*(❯)?\s+(?:Next|Submit)\s*$`)
)

// dialogRowView is one numbered row of the dialog.
type dialogRowView struct {
	checked bool
	label   string
}

// dialogView is what parseDialog reads from a capture of Claude's pane.
type dialogView struct {
	found  bool
	review bool
	text   string // the dialog's lines, whitespace collapsed
	rows   map[int]dialogRowView
	cursor int // the row number the ❯ is on, -1 on Next/Submit, 0 if unseen
}

// parseDialog reads the AskUserQuestion dialog from the bottom of screen,
// starting at its tab line.
func parseDialog(screen string) dialogView {
	lines := strings.Split(screen, "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if dialogTabs.MatchString(lines[i]) {
			start = i
			break
		}
	}
	v := dialogView{rows: map[int]dialogRowView{}}
	if start < 0 {
		return v
	}
	lines = lines[start:]
	v.text = strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	v.review = strings.Contains(v.text, "Ready to submit your answers?")
	for _, l := range lines {
		if m := dialogNext.FindStringSubmatch(l); m != nil {
			if m[1] != "" {
				v.cursor = -1
			}
			continue
		}
		m := dialogRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if _, dup := v.rows[n]; dup {
			continue
		}
		v.rows[n] = dialogRowView{checked: m[3] != "" && m[3] != " ", label: m[4]}
		if m[1] != "" {
			v.cursor = n
		}
	}
	// A todo list's "☐ task" lines look like tabs too; the dialog also has
	// its cursor, or the review page.
	v.found = v.cursor != 0 || v.review
	return v
}

// shows reports whether the dialog is on question q's tab.
func (v dialogView) shows(q string) bool {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 80 {
		q = q[:80]
	}
	return v.found && !v.review && strings.Contains(v.text, q)
}

// handleAnswer answers a pending AskUserQuestion by driving its dialog in
// Claude's pane. The body is {"questions": [question text…], "answers":
// [{options: [index…], other: "text"}…]}: the question texts must still be
// the pending ones (409 otherwise), and each answer must fit its question
// (one choice for single-select, at least one for multiSelect).
func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	var body struct {
		Questions []string         `json:"questions"`
		Answers   []questionAnswer `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	st, err := s.deps.State.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var tool string
	var input json.RawMessage
	found := false
	for _, p := range st.Projects {
		if p.WindowID == windowID && p.SessionID != "" {
			found = true
			if p.Status == "question" {
				tool, input = p.PendingQuestionTool, p.PendingQuestionInput
			}
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	var pending struct {
		Questions []questionSpec `json:"questions"`
	}
	if tool != "AskUserQuestion" || json.Unmarshal(input, &pending) != nil || len(pending.Questions) == 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("no question is waiting for an answer"))
		return
	}
	if len(body.Questions) != len(pending.Questions) {
		writeError(w, http.StatusConflict, fmt.Errorf("the question changed; look again"))
		return
	}
	for i, q := range pending.Questions {
		if body.Questions[i] != q.Question {
			writeError(w, http.StatusConflict, fmt.Errorf("the question changed; look again"))
			return
		}
	}
	answers, err := checkAnswers(pending.Questions, body.Answers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// One dialog-driving request at a time, like mode changes: two
	// interleaved ones would each see the other's keys.
	s.modeMu.Lock()
	defer s.modeMu.Unlock()

	if err := s.answerQuestions(s.claudeTarget(windowID), pending.Questions, answers); err != nil {
		if errors.Is(err, errDialogLost) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{})
}

// checkAnswers validates answers against qs and cleans their text: the
// text is typed into a one-line field, so control characters go and
// newlines become spaces.
func checkAnswers(qs []questionSpec, answers []questionAnswer) ([]questionAnswer, error) {
	if len(answers) != len(qs) {
		return nil, fmt.Errorf("expected %d answers, got %d", len(qs), len(answers))
	}
	out := make([]questionAnswer, len(qs))
	for i, q := range qs {
		a := answers[i]
		n := len(q.Options)
		if n == 0 || n > 8 {
			// A digit picks a row; 9 is the last single key.
			return nil, fmt.Errorf("question %d has %d options; answer it in the terminal", i+1, n)
		}
		other := strings.TrimSpace(strings.Join(strings.Fields(stripControl(a.Other)), " "))
		if len(other) > maxAnswerText {
			return nil, fmt.Errorf("answer %d is longer than %d bytes", i+1, maxAnswerText)
		}
		seen := map[int]bool{}
		for _, o := range a.Options {
			if o < 0 || o >= n || seen[o] {
				return nil, fmt.Errorf("answer %d: bad option %d", i+1, o)
			}
			seen[o] = true
		}
		picked := len(a.Options)
		if other != "" {
			picked++
		}
		if picked == 0 {
			return nil, fmt.Errorf("question %d has no answer", i+1)
		}
		if !q.MultiSelect && picked > 1 {
			return nil, fmt.Errorf("question %d takes one answer", i+1)
		}
		out[i] = questionAnswer{Options: append([]int(nil), a.Options...), Other: other}
	}
	return out, nil
}

// answerQuestions drives the dialog through qs, then submits.
func (s *Server) answerQuestions(target string, qs []questionSpec, answers []questionAnswer) error {
	for i, q := range qs {
		if err := s.answerOne(target, q, answers[i]); err != nil {
			return err
		}
	}
	v, err := s.waitDialog(target, func(v dialogView) bool { return v.review || !v.found })
	if err != nil {
		return err
	}
	if v.review {
		if err := s.dialogKeys(target, tmux.Key{Text: "1"}); err != nil {
			return err
		}
		if _, err := s.waitDialog(target, func(v dialogView) bool { return !v.found }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) answerOne(target string, q questionSpec, a questionAnswer) error {
	n := len(q.Options)
	other := n + 1 // the "Type something" row
	v, err := s.waitDialog(target, func(v dialogView) bool { return v.shows(q.Question) })
	if err != nil {
		return err
	}
	// Start from the first row: a tab opens there, but the terminal user
	// may have moved.
	for tries := 0; v.cursor != 1; tries++ {
		if tries > n+2 {
			return errDialogLost
		}
		from := v.cursor
		if err := s.dialogKeys(target, tmux.Key{Name: "Up"}); err != nil {
			return err
		}
		if v, err = s.waitDialog(target, func(v dialogView) bool { return v.cursor != from }); err != nil {
			return err
		}
	}
	row := func(k int) tmux.Key { return tmux.Key{Text: strconv.Itoa(k)} }
	down := func(k int) []tmux.Key {
		keys := make([]tmux.Key, k)
		for i := range keys {
			keys[i] = tmux.Key{Name: "Down"}
		}
		return keys
	}

	if !q.MultiSelect {
		if a.Other == "" {
			return s.dialogKeys(target, row(a.Options[0]+1))
		}
		return s.typeOther(target, v, other, row(other), a.Other, tmux.Key{Name: "Enter"})
	}

	want := map[int]bool{}
	for _, o := range a.Options {
		want[o+1] = true
	}
	// The "Type something" row stays unchecked here: typing checks it.
	var toggles []tmux.Key
	for k := 1; k <= other; k++ {
		if v.rows[k].checked != want[k] {
			toggles = append(toggles, row(k))
		}
	}
	if len(toggles) > 0 {
		if err := s.dialogKeys(target, toggles...); err != nil {
			return err
		}
		if v, err = s.waitDialog(target, func(v dialogView) bool {
			for k := 1; k <= other; k++ {
				if v.rows[k].checked != want[k] {
					return false
				}
			}
			return true
		}); err != nil {
			return err
		}
	}
	if a.Other != "" {
		if err := s.typeOther(target, v, other, tmux.Key{}, a.Other); err != nil {
			return err
		}
		v.cursor = other
	}
	// ↓ from the cursor down to Next (or Submit), then Enter.
	if err := s.dialogKeys(target, down(other+1-v.cursor)...); err != nil {
		return err
	}
	if _, err := s.waitDialog(target, func(v dialogView) bool { return v.cursor == -1 }); err != nil {
		return err
	}
	return s.dialogKeys(target, tmux.Key{Name: "Enter"})
}

// typeOther fills in the "Type something" row: moves there (with the given
// key, or ↓ from the first row), checks it's still empty, types text, waits
// for it to show, then presses then.
func (s *Server) typeOther(target string, v dialogView, other int, move tmux.Key, text string, then ...tmux.Key) error {
	if strings.TrimRight(v.rows[other].label, ".") != "Type something" {
		// Already holds the terminal user's text; ours would append to it.
		return errDialogLost
	}
	keys := []tmux.Key{move}
	if move == (tmux.Key{}) {
		keys = keys[:0]
		for range other - v.cursor {
			keys = append(keys, tmux.Key{Name: "Down"})
		}
	}
	if err := s.dialogKeys(target, keys...); err != nil {
		return err
	}
	if _, err := s.waitDialog(target, func(v dialogView) bool { return v.cursor == other }); err != nil {
		return err
	}
	if err := s.dialogKeys(target, tmux.Key{Text: text}); err != nil {
		return err
	}
	if _, err := s.waitDialog(target, func(v dialogView) bool {
		return v.cursor == other && strings.TrimRight(v.rows[other].label, ".") != "Type something"
	}); err != nil {
		return err
	}
	if len(then) == 0 {
		return nil
	}
	return s.dialogKeys(target, then...)
}

func (s *Server) dialogKeys(target string, keys ...tmux.Key) error {
	if len(keys) == 0 {
		return nil
	}
	return s.deps.ClaudePane.SendKeys(target, keys)
}

// waitDialog re-reads the dialog until ok holds, or gives up with
// errDialogLost.
func (s *Server) waitDialog(target string, ok func(dialogView) bool) (dialogView, error) {
	for i := 0; i < answerSettleTries; i++ {
		screen, err := s.deps.ClaudePane.Capture(target)
		if err != nil {
			return dialogView{}, err
		}
		if v := parseDialog(screen); ok(v) {
			return v, nil
		}
		time.Sleep(s.modeSettle)
	}
	return dialogView{}, errDialogLost
}
