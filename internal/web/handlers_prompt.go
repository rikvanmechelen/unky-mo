package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rvanmech/unky-mo/internal/tmux"
)

// maxPromptBytes bounds one prompt; tmux takes the text as a command-line
// argument.
const maxPromptBytes = 64 << 10

// maxPromptAttachments bounds the images sent with one prompt.
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
// Attachments are ids from /attachments (never paths); their files are
// pasted ahead of the text and become "[Image #N]" (SendPrompt).
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
		writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d images per prompt", maxPromptAttachments))
		return
	}

	_, _, status, ok := s.resolveSession(windowID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	if status != "idle" && status != "question" && status != "active" {
		writeError(w, http.StatusConflict, fmt.Errorf("session is %s, not idle", status))
		return
	}
	if status == "active" {
		// Typing into a working session queues the message. It would mix
		// with anything already typed in the terminal's message box.
		if err := s.checkInputBoxEmpty(s.claudeTarget(windowID)); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
	}

	paths := make([]string, 0, len(body.Attachments))
	for _, id := range body.Attachments {
		p, ok := s.deps.Attachments.Path(windowID, id)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown attachment %q (expired? attach it again)", id))
			return
		}
		paths = append(paths, p)
	}

	target := s.tmuxSession + ":" + windowID + ".0"
	var err error
	switch {
	case len(paths) > 0:
		err = s.deps.Prompts.SendPrompt(target, text, paths)
	case strings.ContainsAny(text, "\r\n"):
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
	if body.SendNow && status == "active" {
		if err := s.sendNow(windowID, target); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("queued, but couldn't send it now: %w", err))
			return
		}
	}
	writeJSON(w, map[string]any{})
}

// sendNowKeys is Claude Code's "send now" chord: it moves a running tool to
// the background (or stops streaming text) and delivers the queued message.
// Nothing is killed, unlike Esc.
var sendNowKeys = []tmux.Key{{Name: "C-x"}, {Name: "C-s"}}

// sendNow presses the chord, unless the session stopped working since the
// message was typed (then there's no queue to flush, and the keys would
// land in an idle message box).
func (s *Server) sendNow(windowID, target string) error {
	if _, _, status, ok := s.resolveSession(windowID); !ok || status != "active" {
		return nil
	}
	return s.deps.ClaudePane.SendKeys(target, sendNowKeys)
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
	screen, err := s.deps.ClaudePane.Capture(target)
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
