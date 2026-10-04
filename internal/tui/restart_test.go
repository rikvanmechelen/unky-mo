package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rvanmech/unky-mo/internal/notify"
)

// A NotifyRestart socket message (`mo restart`, the web's "Restart mo")
// restarts like ctrl+alt+r: it flags the re-exec and quits.
func TestRestartNotificationQuitsForReExec(t *testing.T) {
	m := Model{}
	next, cmd := m.Update(notificationMsg(notify.Notification{Type: notify.NotifyRestart}))
	if !next.(Model).restartRequested {
		t.Fatal("restartRequested should be set")
	}
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
}
