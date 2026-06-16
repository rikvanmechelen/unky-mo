package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func suspendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suspend",
		Short: "Suspend all active Claude sessions and save state for resume on next launch",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			sessions, err := claude.LiveSessions()
			if err != nil {
				return fmt.Errorf("reading sessions: %w", err)
			}
			if len(sessions) == 0 {
				fmt.Println("No active sessions to suspend")
				return nil
			}

			tc := tmux.NewClient(cfg.TmuxSession)
			ctx := ops.NewContext(tc)

			// Build window-name map so each saved session carries its real
			// tmux window name (may differ from the project name if renamed).
			windowBySession := ops.SessionToWindowMap(ctx)

			toStop := make([]ops.SessionToStop, 0, len(sessions))
			for _, s := range sessions {
				windowName := windowBySession[s.SessionID]
				if windowName == "" {
					windowName = s.Name
					if windowName == "" {
						windowName = s.SessionID[:8]
					}
				}
				toStop = append(toStop, ops.SessionToStop{
					SuspendedSession: ops.SuspendedSession{
						WindowName:  windowName,
						Cwd:         s.CWD,
						SessionID:   s.SessionID,
						ProjectName: windowName,
					},
					PID: s.PID,
				})
			}

			path := ops.SuspendedStatePath()
			res, err := ops.SuspendAll(ctx, path, ops.SuspendParams{
				Sessions:    toStop,
				TmuxSession: cfg.TmuxSession,
			})
			if err != nil {
				return fmt.Errorf("suspend: %w", err)
			}

			fmt.Printf("Suspended %d session(s)", res.Saved)
			if res.Stopped > 0 {
				fmt.Printf(" (%d stopped)", res.Stopped)
			}
			fmt.Println()

			if len(res.Errors) > 0 {
				for _, e := range res.Errors {
					fmt.Printf("  warning: %s\n", e)
				}
			}

			return nil
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Discard suspended state without resuming",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ops.SuspendedStatePath()
			if !ops.HasSuspendedState(path) {
				fmt.Println("No suspended state to clear")
				return nil
			}
			ops.RemoveSuspendedState(path)
			fmt.Println("Cleared suspended state")
			return nil
		},
	})

	return cmd
}
