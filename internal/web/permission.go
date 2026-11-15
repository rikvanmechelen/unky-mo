package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	Preview  string        `json:"preview"`
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
	if row.Status != state.StatusPermission {
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
		resp.Dialog = &permDialogJSON{Title: v.title, Preview: v.shown, Question: v.question, Rows: v.rows, Sig: v.sig()}
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
	if row.Status != state.StatusPermission {
		writeError(w, http.StatusConflict, fmt.Errorf("no permission prompt is waiting"))
		return
	}

	// One dialog-driving request at a time, like mode changes and answers.
	defer s.paneLock(windowID)()

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
