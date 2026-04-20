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

// The drawer helpers target tmux with `sessionName + ":" + m.windowName + ".0"`.
// If main TUI renamed the sidebar's window, m.windowName is updated via the
// resolver on the next tick — but there's a ≤1s window where drawer ops
// would fail silently with "window not found". Fix: target by m.windowID,
// which is stable.
func TestDrawerTargetsSurviveImmediateRename(t *testing.T) {
	t.Skip("not yet fixed — drawer targets still use sessionName:windowName")
}
