package ops

import (
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// resolveTarget builds a tmux target for the given window name: its
// stable id (@N) when a window has exactly that name, else an exact-name
// target. A bare "session:name" target lets tmux match the start of another
// window's name, and dots in a name read as a pane separator.
func resolveTarget(ctx *Context, name string) string {
	session := ctx.Tmux.SessionName()
	windows, err := ctx.Tmux.ListWindows()
	if err != nil {
		return ttmux.ExactTarget(session, name)
	}
	return ttmux.SafeTarget(session, name, windows)
}
