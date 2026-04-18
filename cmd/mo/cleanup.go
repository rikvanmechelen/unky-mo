package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func cleanupCmd() *cobra.Command {
	var branch string
	var deleteBranch bool
	var forceKill bool
	cmd := &cobra.Command{
		Use:   "cleanup <project>",
		Short: "Remove a worktree (and optionally the branch) for a project",
		Long: `Remove the git worktree for the given branch under a project. When --delete-branch is passed,
the local branch is also deleted. If live Claude sessions are running at the target,
--force-kill SIGINTs them first and closes their windows; otherwise the command refuses.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if branch == "" {
				return fmt.Errorf("--branch is required")
			}
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			projectPath, err := findProject(cfg, args[0])
			if err != nil {
				return err
			}

			// Resolve the worktree path so we can check for live sessions there.
			wts, _ := project.ListWorktrees(projectPath)
			var wtPath string
			for _, w := range wts {
				if w.Branch == branch && w.Path != projectPath {
					wtPath = w.Path
					break
				}
			}
			var sessions []claude.Session
			if wtPath != "" {
				sessions = claude.SessionsForPath(wtPath)
			}
			if len(sessions) > 0 && !forceKill {
				return fmt.Errorf("%d live session(s) at %s — pass --force-kill to SIGINT them first", len(sessions), wtPath)
			}

			tc := tmux.NewClient(cfg.TmuxSession)
			ctx := ops.NewContext(tc)
			res, err := ops.CleanupWorktree(ctx, ops.CleanupParams{
				ProjectPath:  projectPath,
				Branch:       branch,
				DeleteBranch: deleteBranch,
				Sessions:     sessions,
			})
			if err != nil {
				return err
			}
			fmt.Println(res.Status)
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "branch to clean up (required)")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "also delete the local branch (git branch -D)")
	cmd.Flags().BoolVar(&forceKill, "force-kill", false, "SIGINT any live sessions at the target first")
	return cmd
}
