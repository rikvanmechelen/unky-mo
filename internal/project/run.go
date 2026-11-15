package project

import (
	"context"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// Commander runs this package's git commands. Tests can swap it; every
// call is bounded by GitTimeout, so a hung git (a lock, a network mount)
// can't hang the TUI or mo web.
var Commander moexec.Commander = moexec.DefaultCommander

// GitTimeout bounds one git command.
var GitTimeout = 30 * time.Second

func gitOutput(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	out, _, err := Commander.Output(ctx, dir, "git", append([]string{"-C", dir}, args...)...)
	return out, err
}

func gitCombined(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	return Commander.CombinedOutput(ctx, dir, "git", append([]string{"-C", dir}, args...)...)
}

func runGit(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	return Commander.Run(ctx, dir, "git", append([]string{"-C", dir}, args...)...)
}
