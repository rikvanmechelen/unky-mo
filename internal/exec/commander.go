// Package exec provides a small test seam around os/exec so callers can
// be unit-tested by injecting a mock. Production code uses [DefaultCommander];
// tests construct a [gomock]-generated mock and inject it via the caller's
// constructor.
package exec

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// Commander wraps the three exec patterns used across the codebase:
// stdout-only output (with stderr captured separately so callers can inspect
// without type-asserting *exec.ExitError), combined stdout+stderr, and
// run-only (exit status only). Every method takes a working directory so
// callers don't have to mutate a returned *exec.Cmd.
type Commander interface {
	// Output runs name with args in dir and returns stdout and stderr
	// separately. On non-zero exit, err is non-nil and stderr still carries
	// the process's stderr bytes.
	Output(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error)

	// CombinedOutput runs name with args in dir and returns merged stdout+stderr.
	CombinedOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error)

	// Run runs name with args in dir and returns only the exit error.
	Run(ctx context.Context, dir, name string, args ...string) error

	// OutputStdin is Output with in fed to the process's stdin.
	OutputStdin(ctx context.Context, dir string, in []byte, name string, args ...string) (stdout, stderr []byte, err error)
}

// DefaultCommander is the production implementation — a thin wrapper over
// os/exec. Callers that don't want to accept a Commander can use this
// directly; tests substitute a mock.
var DefaultCommander Commander = realCommander{}

type realCommander struct{}

func (realCommander) Output(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	cmd := command(ctx, dir, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	return stdout, stderr.Bytes(), err
}

func (realCommander) CombinedOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := command(ctx, dir, name, args...)
	return cmd.CombinedOutput()
}

func (realCommander) Run(ctx context.Context, dir, name string, args ...string) error {
	cmd := command(ctx, dir, name, args...)
	return cmd.Run()
}

func (realCommander) OutputStdin(ctx context.Context, dir string, in []byte, name string, args ...string) ([]byte, []byte, error) {
	cmd := command(ctx, dir, name, args...)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	return stdout, stderr.Bytes(), err
}

// waitDelay bounds how long a cancelled command's output is waited for
// after it's killed: a child it started (claude forks helpers) can hold the
// pipes open, and Wait would otherwise block until that child exits.
const waitDelay = 2 * time.Second

func command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	return cmd
}
