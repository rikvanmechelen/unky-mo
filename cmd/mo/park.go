package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func parkCmd() *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:   "park <project>",
		Short: "Park the current Claude session at a project (SIGINT + relaunch in a new window)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			projectPath, err := findProject(cfg, args[0])
			if err != nil {
				return err
			}
			windowName := args[0]
			if branch != "" {
				windowName = fmt.Sprintf("%s@%s", args[0], branch)
			}

			// Locate the live session at this project so ops.ParkAndLaunch can
			// SIGINT it. Fall through if none — the op will simply launch fresh.
			sessions := claude.SessionsForPath(projectPath)
			var pid int
			if len(sessions) > 0 {
				pid = sessions[0].PID
			}

			tc := tmux.NewClient(cfg.TmuxSession)
			if err := tc.EnsureSession(); err != nil {
				return fmt.Errorf("tmux session: %w", err)
			}
			ctx := ops.NewContext(tc)
			ctx.SidebarWidth = 33

			res, err := ops.ParkAndLaunch(ctx, ops.ParkParams{
				PID:               pid,
				PrimaryWindowName: windowName,
				Cwd:               projectPath,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Parked session at %s and relaunched in %s\n", windowName, res.Target)
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "worktree branch (defaults to main checkout)")
	return cmd
}
