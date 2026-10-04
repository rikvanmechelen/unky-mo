package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	"github.com/spf13/cobra"
)

func architectureCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "architecture",
		Short: "Manage the layer rules the web Overview checks changes against",
	}
	var presetsFlag []string
	var print, force bool
	initCmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write a starting " + review.RulesPath + " for the repo containing dir (default: .)",
		Long: "Writes a rules file with the presets detected for the repo and, as comments, every\n" +
			"dependency between its parts today, to turn into layers. Nothing is committed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			ctx := context.Background()
			root, err := gitfiles.Root(ctx, moexec.DefaultCommander, dir)
			if err != nil {
				return fmt.Errorf("%s isn't in a git checkout", dir)
			}
			var chosen []string
			if cmd.Flags().Changed("preset") {
				chosen = presetsFlag
			}
			text, err := review.DraftRules(ctx, moexec.DefaultCommander, root, chosen)
			if err != nil {
				return err
			}
			if print {
				fmt.Print(text)
				return nil
			}
			path := filepath.Join(root, review.RulesPath)
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite, or --print)", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return err
			}
			fmt.Printf("Wrote %s — review it, then commit it to share the rules.\n", path)
			return nil
		},
	}
	initCmd.Flags().StringSliceVar(&presetsFlag, "preset", nil, "presets to use instead of the detected ones (empty for none)")
	initCmd.Flags().BoolVar(&print, "print", false, "print the rules instead of writing the file")
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing rules file")
	c.AddCommand(initCmd)
	return c
}
