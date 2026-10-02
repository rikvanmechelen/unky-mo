package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/web"
	"github.com/spf13/cobra"
)

func webCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Serve a web dashboard (sessions, worktrees, PRs, tickets, usage) with a live chat view per session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			deps := web.Deps{
				State:     web.NewStateReader(cfg.StateFilePath),
				Projects:  web.NewProjectLister(cfg.LoadProjects),
				Worktrees: web.NewWorktreeReader(),
				PRs:       web.NewPRClient(github.NewClient(nil)),
				Tickets:   web.NewTicketSource(jira.BuildProviders(jiraInstancesFromConfig(cfg.Tickets))),
				Prompts:   web.NewPromptSender(tmux.NewClient(cfg.TmuxSession)),
			}

			refresh := time.Duration(cfg.Tickets.RefreshSeconds) * time.Second
			srv := web.NewServer(deps, refresh, cfg.TmuxSession)

			fmt.Printf("mo web listening on http://%s\n", addr)
			return http.ListenAndServe(addr, srv)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7890", "address to listen on")
	return cmd
}

// jiraInstancesFromConfig bridges config.TicketsConfig to the jira package's
// flat Instance type. Mirrors internal/tui/app.go's ticketsInstancesFromConfig
// — kept as a small separate copy since cmd/mo can't import the tui package
// (and the conversion is pure config plumbing, not TUI behavior).
func jiraInstancesFromConfig(cfg config.TicketsConfig) []jira.Instance {
	out := make([]jira.Instance, 0, len(cfg.Jira))
	for _, j := range cfg.Jira {
		out = append(out, jira.Instance{
			Name:          j.Name,
			BaseURL:       j.BaseURL,
			Email:         j.Email,
			SprintFieldID: j.SprintFieldID,
			StatusMap: tickets.StatusMap{
				InProgress: j.StatusMap.InProgress,
				Blocked:    j.StatusMap.Blocked,
				Review:     j.StatusMap.Review,
				Todo:       j.StatusMap.Todo,
			},
			ProjectMap: j.ProjectMap,
		})
	}
	return out
}
