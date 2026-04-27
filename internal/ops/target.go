package ops

import (
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// resolveTarget builds a tmux target for the given window name. When the
// name contains dots (which tmux misinterprets as pane separators), it
// resolves the name to the window's stable ID via ListWindows. For names
// without dots the target is returned directly with no extra tmux call.
func resolveTarget(ctx *Context, name string) string {
	session := ctx.Tmux.SessionName()
	if !ttmux.NeedsSafeTarget(name) {
		return session + ":" + name
	}
	windows, err := ctx.Tmux.ListWindows()
	if err != nil {
		return session + ":" + name
	}
	return ttmux.SafeTarget(session, name, windows)
}
