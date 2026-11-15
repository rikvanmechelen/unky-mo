package ops

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A pid that isn't a Claude process (reused by something else) isn't
// signalled; this test's own child, a sleep, stands in for it.
func TestSignalClaudeSkipsOtherProcesses(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	signalClaude(cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Fatal("a non-Claude process was signalled")
	case <-time.After(200 * time.Millisecond):
	}
	if isClaudeProcess(os.Getpid()) {
		t.Error("the test binary isn't claude")
	}
}

// A process running something named claude is signalled.
func TestSignalClaudeSignalsClaude(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/claude"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(50 * time.Millisecond)
	if !isClaudeProcess(cmd.Process.Pid) {
		t.Fatal("a script named claude wasn't recognised")
	}
	signalClaude(cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("not signalled")
	}
}
