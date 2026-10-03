package ops

import (
	"fmt"
	"strings"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// EnsureWebServer runs `mo web --addr <addr>` in the detached mo-web tmux
// session, (re)starting it with ctx.MoBinaryPath. Called on every TUI start —
// including ctrl+alt+r — so a freshly-installed binary's web changes are
// always picked up. The pane is remain-on-exit (see StartPersistentSession) so a server that fails to
// start (e.g. port in use) leaves its error visible via `tmux attach -t mo-web`
// rather than taking the session with it.
func EnsureWebServer(ctx *Context, addr string) error {
	command := shellQuote(ctx.MoBinaryPath) + " web --addr " + shellQuote(addr)
	target := ttmux.MoWebSession + ":"
	if ctx.Tmux.SessionExistsNamed(ttmux.MoWebSession) {
		if err := ctx.Tmux.RespawnPane(target, command); err != nil {
			return fmt.Errorf("restarting web server: %w", err)
		}
		return nil
	}
	if err := ctx.Tmux.StartPersistentSession(ttmux.MoWebSession, "", command); err != nil {
		return fmt.Errorf("starting web server: %w", err)
	}
	return nil
}

// shellQuote single-quotes s for the shell tmux hands pane commands to.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
