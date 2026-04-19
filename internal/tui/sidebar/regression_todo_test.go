//go:build sidebarregression

// These tests document sidebar bugs that are not yet fixed. They're tagged
// so `go test ./...` stays green in CI; run `go test -tags sidebarregression
// ./internal/tui/sidebar/...` to confirm which bugs still bite.
//
// When a bug is fixed, move its test out of this file into regression_test.go
// (untagged) so CI enforces it going forward.

package sidebar

import (
	"testing"
)

// refreshSyncStatus matches synced sessions by `s.ProjectName == m.windowName`.
// When the main TUI renames the sidebar's window (e.g. "/rename wip"),
// m.windowName drifts from the project name. The sync lookup then fails and
// the sidebar shows "sync ↑" indefinitely even after a successful push.
//
// Fix direction: cache the project name at init (derived from the first
// state-file tick showing the own-window row), and use that as the match
// key — not m.windowName.
func TestRefreshSyncStatusSurvivesWindowRename(t *testing.T) {
	t.Skip("not yet fixed — see doc comment for fix direction")
	// When this fix lands, replace Skip with:
	//   1. Seed sync.ListLocal to return a session with ProjectName="alpha".
	//   2. Set m.windowName = "alpha [wip]" (after rename).
	//   3. Set m.windowPath = "/ws/alpha" and store cached project = "alpha".
	//   4. Call refreshSyncStatus() and assert m.syncStatus != "".
}

// The drawer helpers target tmux with `sessionName + ":" + m.windowName + ".0"`.
// If main TUI renamed the sidebar's window, m.windowName is updated via the
// resolver on the next tick — but there's a ≤1s window where drawer ops
// would fail silently with "window not found". Fix: target by m.windowID,
// which is stable.
func TestDrawerTargetsSurviveImmediateRename(t *testing.T) {
	t.Skip("not yet fixed — drawer targets still use sessionName:windowName")
}
