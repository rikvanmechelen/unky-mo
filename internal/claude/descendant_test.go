package claude

import (
	"os"
	"testing"
)

// The real IsDescendantOf reads /proc, so we test against real PIDs on the
// host (Linux CI and dev boxes). On other OSes parentPID returns 0, so we
// skip.

func TestIsDescendantOfSelfIsHost(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc on this OS")
	}
	self := os.Getpid()
	if !IsDescendantOf(self, map[int]bool{self: true}) {
		t.Errorf("a process should be its own descendant for this check")
	}
}

func TestIsDescendantOfParentIsHost(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc on this OS")
	}
	parent := os.Getppid()
	if parent <= 1 {
		t.Skip("parent is init; chain trivially not an ancestor")
	}
	if !IsDescendantOf(os.Getpid(), map[int]bool{parent: true}) {
		t.Errorf("self should be a descendant of its own parent (%d)", parent)
	}
}

func TestIsDescendantOfUnrelated(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc on this OS")
	}
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
