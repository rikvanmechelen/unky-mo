package tui

import (
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/config"
)

func TestStickyStatusSurvivesAutoClear(t *testing.T) {
	var m Model
	next, cmd := m.Update(stickyStatusMsgEvent("web: browsers here don't trust the mo web CA yet"))
	m = next.(Model)
	if cmd != nil {
		t.Error("a sticky message must not schedule an auto-clear")
	}
	// A 4s timer left over from an earlier success message.
	next, _ = m.Update(clearStatusMsg{})
	m = next.(Model)
	if m.statusMsg == "" || !m.statusSticky {
		t.Fatal("sticky message was cleared by a timer")
	}

	next, cmd = m.Update(statusMsgEvent("Refreshed"))
	m = next.(Model)
	if m.statusSticky || cmd == nil {
		t.Error("a plain message replaces the sticky one and auto-clears")
	}
}

func TestIsErrorStatus(t *testing.T) {
	for msg, want := range map[string]bool{
		"Launch failed: boom":         true,
		"sync ERROR":                  true,
		"Refreshed":                   false,
		"web: https://localhost:7890": false,
	} {
		if got := isErrorStatus(msg); got != want {
			t.Errorf("isErrorStatus(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestDashboardShowsStickyNoticeInFull(t *testing.T) {
	m := NewModel(nil, nil, nil, "", config.TicketsConfig{}, nil, nil, nil)
	m.width, m.height, m.ready = 120, 40, true
	cmd := "sudo trust anchor /home/me/.config/unky-mo/tls/ca.pem"
	next, _ := m.Update(stickyStatusMsgEvent("⚠ web: browsers on this machine don't trust the mo web CA yet. To fix, run:\n  system:  " + cmd + "\n  then press ctrl+alt+r"))
	view := next.(Model).dashboardView()
	for _, want := range []string{cmd, "then press ctrl+alt+r"} {
		if !strings.Contains(view, want) {
			t.Errorf("dashboard doesn't show %q in full:\n%s", want, view)
		}
	}
}
