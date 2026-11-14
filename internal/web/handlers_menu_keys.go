package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rvanmech/unky-mo/internal/tmux"
)

const (
	maxMenuKeys = 64
	maxMenuText = 200
	// menuSettleTries bounds the wait for Claude to redraw after a key
	// (tries × Server.modeSettle).
	menuSettleTries = 8
)

// menuKeyNames maps the browser's key vocabulary to tmux key names. The
// legend's single letters ("s", "t", "/") are sent as text instead, and
// Ctrl+letter is checked by menuCtrlKey.
var menuKeyNames = map[string]string{
	"up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"enter": "Enter", "esc": "Escape", "tab": "Tab", "btab": "BTab", "space": "Space",
	"pgup": "PPage", "pgdn": "NPage", "home": "Home", "end": "End", "backspace": "BSpace",
}

var menuCtrlKey = regexp.MustCompile(`^C-[a-z]$`)

// menuCtrlDenied are the Ctrl keys that would quit, suspend or interrupt
// Claude Code rather than operate a menu.
var menuCtrlDenied = map[string]bool{"C-c": true, "C-d": true, "C-z": true}

type menuKeyReq struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type menuKeysReq struct {
	Sig  string       `json:"sig"`
	Keys []menuKeyReq `json:"keys"`
	Goto *struct {
		Row int `json:"row"`
	} `json:"goto"`
}

// menuKeysToTmux validates the browser's keys and turns them into tmux keys.
// A key name is handed to tmux as a key, so only the vocabulary above gets
// through. Esc closes a menu (or clears its search), and an Esc on the empty
// prompt box opens /rewind: at most one, and last, so a request can never
// press the second one.
func menuKeysToTmux(keys []menuKeyReq) ([]tmux.Key, error) {
	if len(keys) > maxMenuKeys {
		return nil, fmt.Errorf("too many keys at once (max %d)", maxMenuKeys)
	}
	out := make([]tmux.Key, 0, len(keys))
	for i, k := range keys {
		switch {
		case k.Key != "" && k.Text == "":
			if name, ok := menuKeyNames[k.Key]; ok {
				if name == "Escape" && i != len(keys)-1 {
					return nil, fmt.Errorf("esc must be the last key of a request")
				}
				out = append(out, tmux.Key{Name: name})
			} else if menuCtrlKey.MatchString(k.Key) && !menuCtrlDenied[k.Key] {
				out = append(out, tmux.Key{Name: k.Key})
			} else if r, size := utf8.DecodeRuneInString(k.Key); size == len(k.Key) && unicode.IsPrint(r) {
				out = append(out, tmux.Key{Text: k.Key})
			} else {
				return nil, fmt.Errorf("unsupported key %q", k.Key)
			}
		case k.Text != "" && k.Key == "":
			if stripControl(k.Text) != k.Text || strings.ContainsAny(k.Text, "\n\t") {
				return nil, fmt.Errorf("control characters must be sent as keys")
			}
			if utf8.RuneCountInString(k.Text) > maxMenuText {
				return nil, fmt.Errorf("text too long (max %d characters)", maxMenuText)
			}
			out = append(out, tmux.Key{Text: k.Text})
		default:
			return nil, fmt.Errorf("each key needs exactly one of key or text")
		}
	}
	return out, nil
}

// writeMenuConflict answers 409 with the menu as it is now, so the browser
// can redraw instead of acting on a stale screen.
func writeMenuConflict(w http.ResponseWriter, msg string, m *menuView) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(struct {
		Error string    `json:"error"`
		Menu  *menuView `json:"menu"`
	}{msg, m})
}

// handleMenuKeys types keys into the slash-command menu on Claude's pane,
// the way a person at the terminal would (plan: docs/plans/slash-menus.md).
//
// The screen is the authority: the request names the menu it was made
// from by sig, and nothing is pressed unless that is still what's drawn
// (another viewer or the terminal user may have moved on). "goto" walks
// the cursor to a row of that menu as a closed loop on the row's text:
// rows scroll in and out of view and description lines count as rows, so
// counting key presses would drift. The answer is the screen afterwards.
func (s *Server) handleMenuKeys(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	var req menuKeysReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Sig == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("sig is required"))
		return
	}
	keys, err := menuKeysToTmux(req.Keys)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(keys) == 0 && req.Goto == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("nothing to press"))
		return
	}
	if req.Goto != nil && req.Goto.Row < 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("goto.row must not be negative"))
		return
	}
	if _, _, _, ok := s.resolveSession(windowID); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}

	s.menuMu.Lock()
	defer s.menuMu.Unlock()

	target := s.claudeTarget(windowID)
	cur, err := s.readMenu(windowID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if cur == nil {
		writeMenuConflict(w, "no menu is open", nil)
		return
	}
	if cur.Sig != req.Sig {
		writeMenuConflict(w, "the menu changed — look again", cur)
		return
	}
	if req.Goto != nil && req.Goto.Row >= len(cur.Rows) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("goto.row %d: the menu has %d rows", req.Goto.Row, len(cur.Rows)))
		return
	}

	if req.Goto != nil {
		var msg string
		if cur, msg, err = s.menuGoto(windowID, target, cur, req.Goto.Row); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		} else if msg != "" {
			writeMenuConflict(w, msg, cur)
			return
		}
	}
	if len(keys) > 0 {
		if cur == nil {
			writeMenuConflict(w, "the menu closed", nil)
			return
		}
		// An Esc Claude hasn't acted on yet leaves the screen (and sig) as
		// it was, so the browser can't tell it went through. A second one
		// would land on the empty prompt once the first closes the menu,
		// and open /rewind: refuse it until the screen changes.
		esc := keys[len(keys)-1].Name == "Escape"
		if esc && s.menuEscOn[windowID] == cur.Sig {
			writeMenuConflict(w, "Esc was already sent and Claude hasn't redrawn yet", cur)
			return
		}
		pre := cur.Sig
		if err := s.deps.ClaudePane.SendKeys(target, keys); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		if cur, err = s.menuSettle(windowID, pre); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		if esc && cur != nil && cur.Sig == pre {
			if s.menuEscOn == nil {
				s.menuEscOn = map[string]string{}
			}
			s.menuEscOn[windowID] = pre
		} else {
			delete(s.menuEscOn, windowID)
		}
	}
	writeJSON(w, map[string]*menuView{"menu": cur})
}

// menuSettle waits for the menu to differ from prevSig (Claude redraws a
// moment after a key) and returns what's drawn then. A key that changes
// nothing costs the whole wait.
func (s *Server) menuSettle(windowID, prevSig string) (*menuView, error) {
	var m *menuView
	for i := 0; i < menuSettleTries; i++ {
		var err error
		if m, err = s.readMenu(windowID); err != nil {
			return nil, err
		}
		if m == nil || m.Sig != prevSig {
			return m, nil
		}
		time.Sleep(s.modeSettle)
	}
	return m, nil
}

// menuGoto presses ↓ or ↑ until the cursor is on the row whose text the
// row'th entry of from has. A non-empty message means it gave up (the
// returned menu is where the cursor ended up).
func (s *Server) menuGoto(windowID, target string, from *menuView, row int) (*menuView, string, error) {
	want := from.Rows[row].Text
	cur := from
	dir := "Down"
	if row < from.Cursor {
		dir = "Up"
	}
	budget := len(from.Rows) + from.Above + from.Below + 2
	stalled := 0
	for press := 0; ; press++ {
		if cur.Cursor < 0 {
			return cur, "the menu has no cursor to move", nil
		}
		if cur.Rows[cur.Cursor].Text == want {
			return cur, "", nil
		}
		if i := nearestRow(cur, want, row); i >= 0 {
			dir = "Down"
			if i < cur.Cursor {
				dir = "Up"
			}
		}
		if press >= budget {
			return cur, fmt.Sprintf("couldn't reach %q", want), nil
		}
		if err := s.deps.ClaudePane.SendKeys(target, []tmux.Key{{Name: dir}}); err != nil {
			return cur, "", err
		}
		next, err := s.menuSettle(windowID, cur.Sig)
		if err != nil {
			return cur, "", err
		}
		if next == nil {
			return nil, "the menu closed", nil
		}
		if next.Sig == cur.Sig {
			if stalled++; stalled >= 2 {
				return next, fmt.Sprintf("the cursor won't move to %q", want), nil
			}
		} else {
			stalled = 0
		}
		cur = next
	}
}

// nearestRow is the index of the row with text, the one closest to hint.
func nearestRow(m *menuView, text string, hint int) int {
	best := -1
	for i, r := range m.Rows {
		if r.Text == text && (best < 0 || abs(i-hint) < abs(best-hint)) {
			best = i
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
