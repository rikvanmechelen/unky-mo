package claude

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

// These used to skip entirely on any OS without /proc (macOS included),
// since parentPID only ever read /proc/<pid>/status. That silently made
// IsDescendantOf a no-op on macOS dev boxes: parentPID returned 0 for any
// pid, so the ancestry walk could only ever match pid itself, never an
// ancestor. parentPID now falls back to `ps -o ppid=` when /proc isn't
// there, so these run for real everywhere mo runs.

func TestIsDescendantOfSelfIsHost(t *testing.T) {
	self := os.Getpid()
	if !IsDescendantOf(self, map[int]bool{self: true}) {
		t.Errorf("a process should be its own descendant for this check")
	}
}

func TestIsDescendantOfParentIsHost(t *testing.T) {
	parent := os.Getppid()
	if parent <= 1 {
		t.Skip("parent is init; chain trivially not an ancestor")
	}
	if !IsDescendantOf(os.Getpid(), map[int]bool{parent: true}) {
		t.Errorf("self should be a descendant of its own parent (%d)", parent)
	}
}

func TestIsDescendantOfUnrelated(t *testing.T) {
	// A huge/unused PID won't be in the ancestor chain.
	if IsDescendantOf(os.Getpid(), map[int]bool{2147483640: true}) {
		t.Errorf("unrelated PID should not be claimed as ancestor")
	}
}

func TestIsDescendantOfEmptyHosts(t *testing.T) {
	if IsDescendantOf(os.Getpid(), map[int]bool{}) {
		t.Errorf("empty host set should never match")
	}
}

func TestIsDescendantOfZero(t *testing.T) {
	if IsDescendantOf(0, map[int]bool{1: true}) {
		t.Errorf("pid=0 should short-circuit to false")
	}
}

// TestIsDescendantOfMultiHop reproduces the shape of Claude Code's
// background-agent daemon chain (bg-pty-host -> daemon run -> pane), which
// is more than one fork removed from the tmux pane PID mo tracks. Before
// the ps fallback, parentPID couldn't walk past the first hop on macOS, so
// this chain was wrongly classified as StatusExternal.
func TestIsDescendantOfMultiHop(t *testing.T) {
	// Grandchild (sleep) backgrounded by an intermediate shell, which itself
	// stays alive (`wait`) so the chain holds for the duration of the test:
	// test process -> sh (direct child) -> sleep (grandchild).
	cmd := exec.Command("sh", "-c", `sleep 30 & echo $!; wait`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start intermediate shell: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("read grandchild pid: %v", scanner.Err())
	}
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", scanner.Text(), err)
	}

	// This test is about the walk, not the cache: a table read before the
	// child existed would legitimately not contain it. A real poll tick
	// always starts past the TTL, so model that.
	resetPPIDTable()
	t.Cleanup(resetPPIDTable)

	if !IsDescendantOf(grandchildPID, map[int]bool{os.Getpid(): true}) {
		t.Errorf("grandchild %d should resolve up through %d (sh) to the test process (%d)",
			grandchildPID, cmd.Process.Pid, os.Getpid())
	}
}

// TestParentPIDFallsBackToPS pins the ps(1) parsing directly, independent
// of real process trees or OS quirks in ps's own column whitespace.
func TestParentPIDFallsBackToPS(t *testing.T) {
	if _, err := os.Stat("/proc/1/status"); err == nil {
		t.Skip("this OS has /proc; the ps fallback path isn't exercised")
	}
	resetPPIDTable()
	t.Cleanup(resetPPIDTable)

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "ps", "-Ao", "pid=,ppid=").
		Return([]byte("    1     0\n 4242   777\n  777     1\ngarbage\n"), nil, nil)
	withSessionsCommander(t, cmd)

	if got := parentPID(4242); got != 777 {
		t.Errorf("parentPID(4242) = %d, want 777", got)
	}
	// An unknown pid resolves to 0, which ends the ancestry walk.
	if got := parentPID(999999); got != 0 {
		t.Errorf("parentPID(unknown) = %d, want 0", got)
	}
}

// TestPPIDTableIsCached guards the reason the whole table is read at once:
// IsDescendantOf runs once per (window × session) on every 5s poll, so a
// per-pid ps would spawn dozens of processes each tick. The gomock
// expectation is .Times(1) — a second ps call fails the test.
func TestPPIDTableIsCached(t *testing.T) {
	if _, err := os.Stat("/proc/1/status"); err == nil {
		t.Skip("this OS has /proc; the ps fallback path isn't exercised")
	}
	resetPPIDTable()
	t.Cleanup(resetPPIDTable)

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "ps", "-Ao", "pid=,ppid=").
		Return([]byte(" 30   20\n 20   10\n 10    1\n"), nil, nil).
		Times(1)
	withSessionsCommander(t, cmd)

	for i := 0; i < 20; i++ {
		if !IsDescendantOf(30, map[int]bool{10: true}) {
			t.Fatalf("walk %d: 30 should resolve up through 20 to 10", i)
		}
	}
}

// TestPPIDTableRefreshesOnMiss covers a session launched since the last ps
// read: the sidebar polls every 1s, under a 2s table TTL, so without a
// miss-triggered re-read a brand-new session would read as external for a
// poll or two before resolving to its window.
func TestPPIDTableRefreshesOnMiss(t *testing.T) {
	if _, err := os.Stat("/proc/1/status"); err == nil {
		t.Skip("this OS has /proc; the ps fallback path isn't exercised")
	}
	t.Cleanup(resetPPIDTable)

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "ps", "-Ao", "pid=,ppid=").
		Return([]byte(" 30   20\n 20   10\n 10    1\n"), nil, nil).
		Times(1)
	withSessionsCommander(t, cmd)

	// A still-valid table (under the 2s TTL) that predates pid 30, aged
	// past the miss throttle so the miss is allowed to trigger a re-read.
	seedPPIDTable(map[int]int{10: 1}, 500*time.Millisecond)

	if !IsDescendantOf(30, map[int]bool{10: true}) {
		t.Error("a pid missing from the cached table should trigger a re-read and resolve")
	}
}

// TestPPIDTableMissThrottled is the other half: a miss on a table that was
// just read must NOT spawn another ps, or a dead pid would cost one process
// per lookup on every poll.
func TestPPIDTableMissThrottled(t *testing.T) {
	if _, err := os.Stat("/proc/1/status"); err == nil {
		t.Skip("this OS has /proc; the ps fallback path isn't exercised")
	}
	t.Cleanup(resetPPIDTable)

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	// No EXPECT: any ps call fails the test.
	withSessionsCommander(t, cmd)

	seedPPIDTable(map[int]int{10: 1}, 0) // just read
	for i := 0; i < 10; i++ {
		if got := parentPID(4242); got != 0 {
			t.Fatalf("parentPID(dead pid) = %d, want 0", got)
		}
	}
}

// TestIsDescendantOfCycleTerminates ensures a malformed process table can't
// hang the poll goroutine in an endless ancestry walk.
func TestIsDescendantOfCycleTerminates(t *testing.T) {
	if _, err := os.Stat("/proc/1/status"); err == nil {
		t.Skip("this OS has /proc; the ps fallback path isn't exercised")
	}
	resetPPIDTable()
	t.Cleanup(resetPPIDTable)

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "ps", "-Ao", "pid=,ppid=").
		Return([]byte(" 50   60\n 60   50\n"), nil, nil).
		AnyTimes()
	withSessionsCommander(t, cmd)

	done := make(chan bool, 1)
	go func() { done <- IsDescendantOf(50, map[int]bool{999: true}) }()
	select {
	case got := <-done:
		if got {
			t.Error("a cycle must not report a false ancestor match")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ancestry walk did not terminate on a cyclic process table")
	}
}
