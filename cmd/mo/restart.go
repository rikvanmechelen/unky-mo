package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/notify"
)

// restartCmd asks the running TUI to do what ctrl+alt+r does: restart
// itself, every sidebar and mo web, picking up a freshly-installed binary.
func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the running TUI, its sidebars and mo web (like ctrl+alt+r)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := notify.SendRestart(cfg.SocketPath); err != nil {
				return err
			}
			fmt.Println("Restarting mo…")
			return nil
		},
	}
}
