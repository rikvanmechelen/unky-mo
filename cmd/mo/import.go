package main

import (
	"fmt"
	"strconv"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <pid>",
		Short: "Import an external Claude session into mo (SIGTERM + resume in a new tmux window)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PID %q: %w", args[0], err)
			}

			// Locate the session in ~/.claude/sessions so we know its cwd + ID.
			sessions, err := claude.ReadSessions()
			if err != nil {
				return fmt.Errorf("read sessions: %w", err)
			}
			var match *claude.Session
			for i := range sessions {
				if sessions[i].PID == pid {
					match = &sessions[i]
					break
				}
			}
			if match == nil {
				return fmt.Errorf("no session found for pid %d", pid)
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			tc := tmux.NewClient(cfg.TmuxSession)
			if err := tc.EnsureSession(); err != nil {
				return fmt.Errorf("tmux session: %w", err)
			}
			ctx := ops.NewContext(tc)
			ctx.SidebarWidth = 33

			// Derive a window name from the project's path basename (best
			// effort — user can rename later inside Claude).
			windowName := match.CWD
			if len(windowName) > 40 {
				windowName = windowName[len(windowName)-40:]
			}

			res, err := ops.ImportExternalSession(ctx, ops.ImportParams{
				PID:        pid,
				SessionID:  match.SessionID,
				Cwd:        match.CWD,
				WindowName: windowName,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Imported session %s as %s\n", match.SessionID, res.Target)
			return nil
		},
	}
}
