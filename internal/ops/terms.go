package ops

import (
	"strings"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// TermSessionName is the mo-terms parking session that holds a window's
// terminal tabs while they aren't shown in its drawer. Each window gets its
// own so terminals don't leak between windows' drawers and popups. Prefers
// the window's instance ID (mo-generated hex key), then its stable tmux
// window ID ("@N" → "N"), then its sanitized name, and finally the bare
// global name. Shared by the sidebar and mo web so both agree on where a
// window's terminals live.
func TermSessionName(instanceID, windowID, windowName string) string {
	switch {
	case instanceID != "":
		return ttmux.MoTermsSession + "-" + instanceID
	case windowID != "":
		return ttmux.MoTermsSession + "-" + strings.TrimPrefix(windowID, "@")
	case windowName != "":
		return ttmux.MoTermsSession + "-" + sanitizeTermSessionSuffix(windowName)
	default:
		return ttmux.MoTermsSession
	}
}

// sanitizeTermSessionSuffix replaces characters that tmux treats specially
// in session names (':', '.', whitespace) with '-' so arbitrary window
// names produce a valid session target.
func sanitizeTermSessionSuffix(s string) string {
	r := strings.NewReplacer(":", "-", ".", "-", " ", "-", "\t", "-")
	return r.Replace(s)
}

// Terminal is one of a window's drawer terminals: shown in the window's
// drawer (named by tmux.DrawerPaneOption) or parked in its TermSessionName
// session.
type Terminal struct {
	ID      string // tmux pane ID, "%N"
	Command string // what's running, e.g. "fish"
	Cwd     string
	Visible bool // shown in the window's drawer right now (vs parked)
}

// TermSessionTmux is the tmux surface EnsureTermSession needs. Both the
// sidebar's TmuxClient and *tmux.Client satisfy it.
type TermSessionTmux interface {
	UnbindKey(table, key string) error
	BindKey(table, key, command string, args ...string) error
	SessionExistsNamed(name string) bool
	NewDetachedSession(name, cwd string) (string, error)
	SetSessionOption(session, option, value string) error
}

// EnsureTermSession lazily creates a mo-terms parking session. It returns
// the pane ID of the session's initial window when this call created the
// session — callers decide whether that pane becomes a terminal tab (the
// popup and mo web's "New terminal") or gets killed (the sidebar's drawer
// hide path). Returns "" when the session already existed.
//
// The new session is configured so clients attached to it (i.e. the
// sidebar's popup) use the popup-keys key table, where backtick is bound to
// detach-client.
func EnsureTermSession(t TermSessionTmux, name, cwd string) (string, error) {
	// Clear legacy Tab/BTab bindings that older sidebar versions installed
	// on the popup-keys table. tmux key tables are server-global and
	// persist across sidebar restarts until the tmux server dies, so a
	// fresh binary cannot rely on "we just didn't rebind them" — we have
	// to actively unbind to reach a clean state. Unbind is idempotent, so
	// running it every time is safe.
	_ = t.UnbindKey("popup-keys", "Tab")
	_ = t.UnbindKey("popup-keys", "BTab")

	if t.SessionExistsNamed(name) {
		return "", nil
	}
	ghost, err := t.NewDetachedSession(name, cwd)
	if err != nil {
		return "", err
	}
	if err := t.SetSessionOption(name, "key-table", "popup-keys"); err != nil {
		return "", err
	}
	_ = t.SetSessionOption(name, "mouse", "on")
	_ = t.BindKey("popup-keys", "`", "detach-client")
	// Mouse bindings so the popup supports scroll and text selection.
	// WheelUp enters copy-mode (auto-exits at bottom), WheelDown passes
	// through, and drag starts a selection.
	_ = t.BindKey("popup-keys", "WheelUpPane", "copy-mode", "-e")
	_ = t.BindKey("popup-keys", "WheelDownPane", "send-keys", "-M")
	_ = t.BindKey("popup-keys", "MouseDrag1Pane", "copy-mode", "-M")
	return ghost, nil
}
