package ops

import (
	"testing"
)

func TestEnsureWebServerStartsSessionWhenMissing(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	ctx.MoBinaryPath = "/home/me/go/bin/mo"

	tmux.EXPECT().SessionExistsNamed("mo-web").Return(false)
	tmux.EXPECT().StartPersistentSession("mo-web", "", `'/home/me/go/bin/mo' web --addr ':7890'`).Return(nil)

	if err := EnsureWebServer(ctx, ":7890"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureWebServerRespawnsExistingSession(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	ctx.MoBinaryPath = "/opt/mo"

	// Always respawned, even if alive, so a freshly-installed binary is picked up.
	tmux.EXPECT().SessionExistsNamed("mo-web").Return(true)
	tmux.EXPECT().RespawnPane("mo-web:", `'/opt/mo' web --addr '127.0.0.1:7890'`).Return(nil)

	if err := EnsureWebServer(ctx, "127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/it's/mo"); got != `'/it'\''s/mo'` {
		t.Errorf("shellQuote: got %s", got)
	}
}
