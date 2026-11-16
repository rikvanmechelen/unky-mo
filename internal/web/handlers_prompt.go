package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
)

// maxPromptBytes bounds one prompt; tmux takes the text as a command-line
// argument.
const maxPromptBytes = 64 << 10

// maxPromptAttachments bounds the files sent with one prompt.
const maxPromptAttachments = 10

// handlePrompt injects a prompt into a live session's tmux pane, gated on
// the session currently being idle, working (Claude Code queues what is typed
// mid-turn and reads it at its next tool boundary) or blocked on an interactive question
// (status "question" — e.g. AskUserQuestion) — answering is exactly what's
// needed to unblock those. A genuine "permission" status stays rejected: we
// don't yet know the tool/input Claude Code's PermissionRequest hook carries,
// so there's nothing to safely render or confirm the user is answering. The
// actual response is not in this response body — it streams back over the
// session's already-open /api/transcript/{windowID} SSE connection.
// Multi-line text goes in as one bracketed paste (SendPastedText).
// Attachments are ids from /attachments (never paths). Images are pasted
// ahead of the text and become "[Image #N]" (SendPrompt); other files are
// named after the text as paths for Claude to read (withFileLines).
func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")

	var body struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
		// SendNow asks Claude to take the message at once instead of at
		// its next tool boundary (Claude Code's ctrl+x ctrl+s). It only
		// means something while the session is working.
		SendNow bool `json:"sendNow"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPromptBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	text := strings.TrimSpace(stripControl(strings.ReplaceAll(body.Text, "\r\n", "\n")))
	if text == "" && len(body.Attachments) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("empty prompt"))
		return
	}
	if len(text) > maxPromptBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("prompt is longer than %d bytes", maxPromptBytes))
		return
	}
	if len(body.Attachments) > maxPromptAttachments {
		writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d attachments per prompt", maxPromptAttachments))
		return
	}

	_, _, status, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if status != state.StatusIdle && status != state.StatusQuestion && status != state.StatusActive {
		writeError(w, http.StatusConflict, fmt.Errorf("session is %s, not idle", status))
		return
	}
	defer s.paneLock(windowID)()
	if status == state.StatusQuestion {
		// A captured AskUserQuestion is answered through its form; text
		// from the message box only goes to its "Type something" row,
		// since anywhere else in the dialog digits pick rows and Enter
		// moves on. An uncaptured question has no form, and the browser
		// asks for an option number or a reply: that is typed as is.
		if row, found, err := s.permissionRow(windowID); err == nil && found && row.PendingTool == "AskUserQuestion" {
			if err := s.checkTypeSomething(s.claudeTarget(windowID)); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
		}
	} else if err := s.checkInputBoxEmpty(s.claudeTarget(windowID)); err != nil {
		// Idle or working, the text goes into the message box: it would
		// mix with text already typed in the terminal, or land in a dialog
		// (/config, the trust prompt…) the state file knows nothing about,
		// where Enter changes a setting.
		writeError(w, http.StatusConflict, err)
		return
	}

	var paths, files []string
	for _, id := range body.Attachments {
		p, ok := s.deps.Attachments.Path(windowID, id)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown attachment %q (expired? attach it again)", id))
			return
		}
		if isImageAttachmentID(id) {
			paths = append(paths, p)
		} else {
			files = append(files, p)
		}
	}
	text = withFileLines(text, files)

	target := s.tmuxSession + ":" + windowID + ".0"
	var err error
	switch {
	case len(paths) > 0:
		err = s.deps.Prompts.SendPrompt(target, text, paths)
	case strings.ContainsAny(text, "\r\n\t"):
		// A typed Tab would be a key (completion), not text.
		// Typed newlines would submit at the first line; a bracketed
		// paste arrives as one block (e.g. a review from the editor tabs).
		err = s.deps.Prompts.SendPastedText(target, text)
	default:
		err = s.deps.Prompts.SendLiteralText(target, text)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to deliver prompt: %w", err))
		return
	}
	if body.SendNow && status == state.StatusActive {
		if err := s.sendNow(windowID, target); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("queued, but couldn't send it now: %w", err))
			return
		}
	}
	writeJSON(w, map[string]any{})
}

// withFileLines names attached files after the prompt's text, as paths on
// this machine that Claude can read.
func withFileLines(text string, files []string) string {
	var lines string
	switch len(files) {
	case 0:
		return text
	case 1:
		lines = "Attached file: " + files[0]
	default:
		lines = "Attached files:\n- " + strings.Join(files, "\n- ")
	}
	if text == "" {
		return lines
	}
	return text + "\n\n" + lines
}

// sendNowKeys is Claude Code's "send now" chord: it moves a running tool to
// the background (or stops streaming text) and delivers the queued message.
// Nothing is killed, unlike Esc.
var sendNowKeys = []tmux.Key{{Name: "C-x"}, {Name: "C-s"}}

// sendNowHint is what Claude Code shows under a queued message.
const sendNowHint = "ctrl+x ctrl+s to send now"

// sendNowTries bounds the wait for the queued message to show up on screen
// (tries × Server.modeSettle).
const sendNowTries = 80

// sendNow presses the chord once Claude's screen shows the message as
// queued. Pressing right after Enter is too early: the chord does nothing
// until the message is in the queue. It does nothing if the session
// stopped working meanwhile (the message went in as a normal prompt, and
// the keys would land in an idle message box).
func (s *Server) sendNow(windowID, target string) error {
	for range sendNowTries {
		if _, _, status, ok := s.resolveSession(windowID); !ok || status != state.StatusActive {
			return nil
		}
		screen, err := s.deps.ClaudePane.Capture(target)
		if err != nil {
			return err
		}
		if queuedHintShown(screen) {
			return s.deps.ClaudePane.SendKeys(target, sendNowKeys)
		}
		time.Sleep(s.modeSettle)
	}
	return fmt.Errorf("Claude didn't show the message as queued")
}

// stripControl drops control characters other than newline and tab. The
// text is typed or pasted into Claude Code's terminal UI, where an escape
// sequence (e.g. one ending a bracketed paste early, quoted from a file in
// a review) would act as keystrokes instead of text.
func stripControl(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			return r
		}
		return -1
	}, text)
}

var errInputBoxBusy = fmt.Errorf("there's text in the terminal's message box; send or clear it first")

// checkInputBoxEmpty refuses to type into a pane whose message box already
// holds text, or whose box can't be read.
func (s *Server) checkInputBoxEmpty(target string) error {
	if s.deps.ClaudePane == nil {
		return fmt.Errorf("can't check Claude's message box")
	}
	// Styled, so a dim suggestion in the box isn't taken for typed text.
	screen, err := s.deps.ClaudePane.CaptureStyled(target)
	if err != nil {
		return fmt.Errorf("can't read Claude's message box: %w", err)
	}
	text, ok := parseInputBox(screen)
	if !ok {
		return fmt.Errorf("can't see Claude's message box (a dialog may be open)")
	}
	if text != "" {
		return errInputBoxBusy
	}
	return nil
}

// queuedHintShown reports whether Claude shows a queued message's "send
// now" hint: a line of its own just above the message box, not the phrase
// anywhere on screen (the transcript can quote it, e.g. a cat of the docs).
func queuedHintShown(screen string) bool {
	lines := strings.Split(cropSidePanel(screen), "\n")
	rules := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if !isBoxRule(lines[i]) {
			continue
		}
		if rules++; rules < 2 {
			continue
		}
		for j := i - 1; j >= max(0, i-queuedHintLines); j-- {
			if strings.TrimSpace(lines[j]) == sendNowHint {
				return true
			}
		}
		return false
	}
	return false
}

// queuedHintLines is how far above the message box the hint may sit
// (queued messages, the hint, a blank line and the spinner between).
const queuedHintLines = 12

var errNotTypeSomething = fmt.Errorf("answer the question in its banner (the terminal's cursor isn't on \"Type something\")")

// checkTypeSomething refuses to type into an AskUserQuestion dialog unless
// its cursor is on an empty "Type something" row.
func (s *Server) checkTypeSomething(target string) error {
	if s.deps.ClaudePane == nil {
		return fmt.Errorf("can't read the question")
	}
	screen, err := s.deps.ClaudePane.Capture(target)
	if err != nil {
		return fmt.Errorf("can't read the question: %w", err)
	}
	v := parseDialog(screen)
	if !v.found {
		return fmt.Errorf("can't see the question in Claude's terminal")
	}
	if row, ok := v.rows[v.cursor]; !ok || strings.TrimRight(row.label, ".") != "Type something" {
		return errNotTypeSomething
	}
	return nil
}
