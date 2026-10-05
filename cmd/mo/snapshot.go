package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// snapshotCmd is what the Bash PreToolUse/PostToolUse hooks run: it reads
// the hook payload on stdin and records the checkout before or after the
// command (internal/bashsnap). It never fails the hook and never prints to
// stdout, which Claude Code would read as a hook decision.
func snapshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "snapshot pre|post",
		Short:     "Record the checkout around a Bash call (used by Claude Code hooks)",
		Hidden:    true,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"pre", "post"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			store, err := bashsnap.NewStore(moexec.DefaultCommander)
			if err == nil {
				err = store.Hook(ctx, args[0], os.Stdin)
			}
			if err != nil && !errors.Is(err, bashsnap.ErrSkip) {
				fmt.Fprintln(os.Stderr, "mo snapshot:", err)
			}
			return nil
		},
	}
}
