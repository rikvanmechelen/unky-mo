package main

import (
	"fmt"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/spf13/cobra"
)

func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage coding agents (Claude, Gemini, Codex, etc.)",
	}
	cmd.AddCommand(agentsListCmd())
	cmd.AddCommand(agentsAddCmd())
	cmd.AddCommand(agentsRemoveCmd())
	cmd.AddCommand(agentsDefaultCmd())
	return cmd
}

func agentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured coding agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if len(cfg.Agents) == 0 {
				fmt.Println("No agents configured.")
				return nil
			}
			for _, a := range cfg.Agents {
				def := ""
				if a.Default {
					def = " (default)"
				}
				resume := ""
				if a.ResumeCmd != "" {
					resume = fmt.Sprintf("  resume: %s", a.ResumeCmd)
				}
				fmt.Printf("  [%s] %-20s cmd: %s%s%s\n", a.Key, a.Name, a.Cmd, resume, def)
			}
			return nil
		},
	}
}

func agentsAddCmd() *cobra.Command {
	var name, key, cmdStr, resumeCmd string
	var setDefault bool

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new coding agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			agent := config.AgentConfig{
				Name:      name,
				Key:       key,
				Cmd:       cmdStr,
				ResumeCmd: resumeCmd,
				Default:   setDefault,
			}
			if err := cfg.AddAgent(agent); err != nil {
				return err
			}
			// If this is marked default, clear the flag on others.
			if setDefault {
				if err := cfg.SetDefaultAgent(key); err != nil {
					return err
				}
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Printf("Added agent %q [%s] -> %s\n", name, key, cmdStr)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name (e.g. \"Gemini CLI\")")
	cmd.Flags().StringVar(&key, "key", "", "single-char mnemonic for the picker menu")
	cmd.Flags().StringVar(&cmdStr, "cmd", "", "shell command to exec (e.g. \"gemini\")")
	cmd.Flags().StringVar(&resumeCmd, "resume-cmd", "", "optional resume command prefix")
	cmd.Flags().BoolVar(&setDefault, "default", false, "set as the default agent")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("key")
	cmd.MarkFlagRequired("cmd")
	return cmd
}

func agentsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <key>",
		Short: "Remove a coding agent by its mnemonic key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			agent := cfg.AgentByKey(args[0])
			if agent == nil {
				return fmt.Errorf("no agent with key %q", args[0])
			}
			name := agent.Name
			if err := cfg.RemoveAgent(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Printf("Removed agent %q [%s]\n", name, args[0])
			return nil
		},
	}
}

func agentsDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <key>",
		Short: "Set the default coding agent by its mnemonic key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := cfg.SetDefaultAgent(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			agent := cfg.AgentByKey(args[0])
			fmt.Printf("Default agent set to %q [%s]\n", agent.Name, args[0])
			return nil
		},
	}
}
