package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func concurrentCmd() *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:   "concurrent <project>",
		Short: "Launch a concurrent sibling Claude session at a project (auto-ordinalized window name)",
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
			tc := tmux.NewClient(cfg.TmuxSession)
			if err := tc.EnsureSession(); err != nil {
				return fmt.Errorf("tmux session: %w", err)
			}
			ctx := ops.NewContext(tc)
			ctx.SidebarWidth = 33

			res, err := ops.LaunchSibling(ctx, ops.SiblingParams{
				ProjectName: args[0],
				Branch:      branch,
				Cwd:         projectPath,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Launched concurrent sibling: %s\n", res.Target)
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "worktree branch (defaults to main checkout)")
	return cmd
}
