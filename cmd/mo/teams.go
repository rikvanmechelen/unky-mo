package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/spf13/cobra"
)

func teamsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "List active Claude Code agent teams",
		RunE: func(cmd *cobra.Command, args []string) error {
			configs, err := claude.ReadTeamConfigs()
			if err != nil {
				return fmt.Errorf("reading team configs: %w", err)
			}
			if len(configs) == 0 {
				fmt.Println("No active agent teams")
				return nil
			}
			for _, tc := range configs {
				lead := tc.LeadMember()
				leadName := "(unknown)"
				if lead != nil {
					leadName = lead.Name
				}
				teammates := tc.Teammates()
				fmt.Printf("  %-20s lead: %-12s  %d teammate(s)\n", tc.Name, leadName, len(teammates))
				for _, tm := range teammates {
					fmt.Printf("    %-18s %s\n", tm.Name, tm.AgentType)
				}
			}
			fmt.Printf("\n%d team(s)\n", len(configs))
			return nil
		},
	}
	return cmd
}
