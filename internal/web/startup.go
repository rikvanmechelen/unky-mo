package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/rvanmech/unky-mo/internal/tmux"
)

// A session that was just launched has no state row until Claude Code writes
// its session file, and a first run in a folder stops before that, on its
// trust dialog (probed on Claude Code 2.1.236; see testdata/startup/):
//
//	─────────────────────────      full-width rule
//	 Accessing workspace:
//	 /path/to/folder               may wrap on a narrow pane
//	 Quick safety check: Is this a project you created or one you trust? …
//	 ⚠ This folder pre-approves 11 tool permissions in .claude/settings.local.json:
//	   Bash(go build:*), …         only when the folder's settings allow tools
//	 These will apply without asking. Only proceed if you trust this configuration.
//	 Security guide
//	 ❯ 1. Yes, I trust this folder
//	   2. No, exit
//	 Enter to confirm · Esc to cancel
//
// A digit picks its row at once, like the permission dialogs ("2" ended
// Claude without an Enter). Older versions drew the rows unnumbered; those
// don't parse, so the browser can only tell the user to answer in the
// terminal. The server never answers for the user: the browser sends the
// row, with the sig of the dialog it showed.

var errStartupLost = errors.New("the dialog in Claude's terminal isn't the one shown here (answered there, or it changed); look again")

const startupTrust = "trust"

// startupView is what parseStartupDialog reads from a capture of the pane.
type startupView struct {
	found   bool
	kind    string
	folder  string
	warning string // the pre-approved tool permissions notice, if any
	rows    []permRowView
	cursor  int
}

func (v startupView) row(n int) (permRowView, bool) {
	for _, r := range v.rows {
		if r.N == n {
			return r, true
		}
	}
	return permRowView{}, false
}

// sig names this dialog as shown: its kind, folder and rows.
func (v startupView) sig() string {
	h := sha256.New()
	io.WriteString(h, v.kind+"\x00"+v.folder)
	for _, r := range v.rows {
		fmt.Fprintf(h, "\x00%d\x00%s", r.N, r.Label)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseStartupDialog reads Claude Code's folder trust dialog from a capture
// of its pane. It needs the "Accessing workspace:" heading, the "Quick
// safety check" text and at least two numbered rows, one with the cursor.
func parseStartupDialog(screen string) startupView {
	var v startupView
	lines := strings.Split(screen, "\n")
	hdr := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "Accessing workspace:" {
			hdr = i // the last one: older output may be above
		}
	}
	if hdr < 0 {
		return v
	}

	i := hdr + 1
	var folder []string
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "Quick safety check") || (t == "" && len(folder) > 0) {
			break
		}
		if t != "" {
			folder = append(folder, t)
		}
	}
	// A path wrapped by the pane has no spaces to lose.
	v.folder = strings.Join(folder, "")

	var warning []string
	inWarning, safety := false, false
	for ; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Quick safety check") {
			safety = true
		}
		if m := permRow.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[3])
			v.rows = append(v.rows, permRowView{N: n, Label: m[4]})
			if m[2] != "" {
				v.cursor = n
			}
			inWarning = false
			continue
		}
		switch {
		case strings.HasPrefix(t, "⚠"):
			inWarning = true
			warning = append(warning, strings.TrimSpace(strings.TrimPrefix(t, "⚠")))
		case inWarning && t != "" && !strings.HasPrefix(t, "These will apply") && t != "Security guide":
			warning = append(warning, t)
		default:
			inWarning = false
		}
	}
	v.warning = strings.Join(warning, "\n")
	if v.folder == "" || !safety || len(v.rows) < 2 || v.cursor == 0 {
		return startupView{}
	}
	v.found, v.kind = true, startupTrust
	return v
}

// startupDialogJSON is shaped like the permission banner's dialog, so the
// browser renders it with the same code.
type startupDialogJSON struct {
	Kind     string        `json:"kind"`
	Title    string        `json:"title"`
	Question string        `json:"question"`
	Preview  string        `json:"preview"`
	Rows     []permRowView `json:"rows"`
	Sig      string        `json:"sig"`
}

func (v startupView) json() *startupDialogJSON {
	return &startupDialogJSON{
		Kind:     v.kind,
		Title:    "Claude Code asks whether to trust this folder",
		Question: v.folder,
		Preview:  v.warning,
		Rows:     v.rows,
		Sig:      v.sig(),
	}
}

var windowIDPattern = regexp.MustCompile(`^@[0-9]+$`)

// startupWindow checks that windowID is a window mo launched (it carries the
// @mo_instance_id the launch sets, which the TUI's own window and strays
// lack) and has no session yet. Other windows' panes are never read.
func (s *Server) startupWindow(windowID string) (status int, err error) {
	if !windowIDPattern.MatchString(windowID) {
		return http.StatusBadRequest, fmt.Errorf("bad window id")
	}
	windows, err := s.deps.Sessions.ListWindows()
	if err != nil {
		return http.StatusInternalServerError, err
	}
	known := false
	for _, w := range windows {
		if w.ID == windowID && w.InstanceID != "" {
			known = true
			break
		}
	}
	if !known {
		return http.StatusNotFound, fmt.Errorf("no session window %s", windowID)
	}
	if _, _, _, ok := s.resolveSession(windowID); ok {
		return http.StatusConflict, fmt.Errorf("window %s already has a session", windowID)
	}
	return 0, nil
}

// handleStartup shows what a window without a session is waiting on:
// {"dialog": null} while it's just starting, or Claude Code's trust dialog.
func (s *Server) handleStartup(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	if status, err := s.startupWindow(windowID); err != nil {
		writeError(w, status, err)
		return
	}
	screen, err := s.deps.ClaudePane.Capture(s.claudeTarget(windowID))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	resp := struct {
		Dialog *startupDialogJSON `json:"dialog"`
	}{}
	if v := parseStartupDialog(screen); v.found {
		resp.Dialog = v.json()
	}
	writeJSON(w, resp)
}

type startupAnswer struct {
	Sig string `json:"sig"`
	Row int    `json:"row"`
}

// handleAnswerStartup presses a row of the dialog, if it's still the one the
// browser showed.
func (s *Server) handleAnswerStartup(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	var a startupAnswer
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&a); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if a.Row < 1 || a.Row > 9 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad row %d", a.Row))
		return
	}
	if status, err := s.startupWindow(windowID); err != nil {
		writeError(w, status, err)
		return
	}

	// One dialog-driving request at a time, like answers and mode changes.
	s.modeMu.Lock()
	defer s.modeMu.Unlock()

	target := s.claudeTarget(windowID)
	screen, err := s.deps.ClaudePane.Capture(target)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	v := parseStartupDialog(screen)
	if !v.found || v.sig() != a.Sig {
		writeError(w, http.StatusConflict, errStartupLost)
		return
	}
	if _, ok := v.row(a.Row); !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("the dialog has no choice %d", a.Row))
		return
	}
	if err := s.deps.ClaudePane.SendKeys(target, []tmux.Key{{Text: strconv.Itoa(a.Row)}}); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{})
}
