package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	moSync "github.com/rvanmech/unky-mo/internal/sync"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync sessions between machines via a private git repo",
	}

	syncDir := moSync.DefaultSyncDir()

	cmd.AddCommand(&cobra.Command{
		Use:   "init <repo-url>",
		Short: "Connect to a private GitHub repo for syncing sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := moSync.Init(args[0], syncDir); err != nil {
				return err
			}
			fmt.Printf("Sync repo initialized at %s\n", syncDir)
			if _, err := moSync.LoadKey(); err != nil {
				fmt.Println("No sync key yet — run 'mo sync init-key' on one machine, then copy the key to any other machine that should sync.")
			}
			return nil
		},
	})

	var initKeyForce bool
	initKeyCmd := &cobra.Command{
		Use:   "init-key",
		Short: "Generate a shared encryption key for sync (run once, copy to other machines)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := moSync.InitKey(initKeyForce)
			if err != nil {
				return err
			}
			fmt.Printf("Wrote new sync key to %s\n", path)
			fmt.Println("Copy this file (or the output of 'mo sync show-key') to every machine that should sync.")
			fmt.Println("Anyone with this key can decrypt your synced sessions — treat it like a password.")
			return nil
		},
	}
	initKeyCmd.Flags().BoolVar(&initKeyForce, "force", false, "overwrite an existing key")
	cmd.AddCommand(initKeyCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "show-key",
		Short: "Print the current sync key (base64) for copying to another machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			b64, err := moSync.ShowKey()
			if err != nil {
				return err
			}
			fmt.Println(b64)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "migrate",
		Short: "Re-encrypt any legacy plaintext sessions in the sync repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := moSync.Migrate(syncDir)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Println("Nothing to migrate — all projects already use the encrypted layout.")
				return nil
			}
			fmt.Printf("Migrated %d project(s) to the encrypted layout and pushed.\n", n)
			fmt.Println()
			fmt.Println("WARNING: git history on the sync remote still contains the plaintext blobs.")
			fmt.Println("To fully purge the plaintext, either:")
			fmt.Println("  1. Delete the remote repo on GitHub, recreate it empty, then 'mo sync init <new-url>' and push again from each machine.")
			fmt.Println("  2. Rewrite history with git-filter-repo on the sync clone and force-push (advanced).")
			return nil
		},
	})

	pushCmd := &cobra.Command{
		Use:   "push <project>",
		Short: "Push a project's session to the sync repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			projectPath, err := findProjectOrWorktree(cfg, args[0])
			if err != nil {
				return err
			}

			sessionID, _ := cmd.Flags().GetString("session")
			if sessionID == "" {
				live := claude.SessionForPath(projectPath)
				if live == nil {
					return fmt.Errorf("no live session for %s; pass --session <id> to push a specific session", args[0])
				}
				sessionID = live.SessionID
			}

			if err := moSync.Push(args[0], projectPath, syncDir, sessionID); err != nil {
				return err
			}
			fmt.Printf("Pushed session %s for %s\n", sessionID, args[0])
			return nil
		},
	}
	pushCmd.Flags().String("session", "", "explicit session ID to push (defaults to the live session for the project)")
	cmd.AddCommand(pushCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "pull [project]",
		Short: "Pull sessions from the sync repo (all by default, or one project with resume)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if len(args) == 0 {
				resolver := func(name string) string {
					path, err := findProjectOrWorktree(cfg, name)
					if err != nil {
						return ""
					}
					return path
				}
				results, err := moSync.PullAll(syncDir, resolver)
				if err != nil {
					return err
				}
				if len(results) == 0 {
					fmt.Println("No synced sessions found")
					return nil
				}
				pulled := 0
				for _, r := range results {
					title := r.Meta.Title
					if title == "" {
						title = r.Meta.SessionID[:8] + "..."
					}
					age := time.Since(r.Meta.PushedAt).Truncate(time.Minute)
					marker := ""
					if !r.Pulled {
						if r.Skipped != "" {
							marker = fmt.Sprintf("  (skipped: %s)", r.Skipped)
						} else {
							marker = "  (skipped)"
						}
					} else {
						pulled++
					}
					fmt.Printf("  %-25s %s  from %s  %s ago%s\n", r.Meta.ProjectName, title, r.Meta.Hostname, age, marker)
				}
				fmt.Printf("\nPulled %d of %d sessions\n", pulled, len(results))
				return nil
			}

			projectPath, err := findProjectOrWorktree(cfg, args[0])
			if err != nil {
				return err
			}

			meta, err := moSync.Pull(args[0], projectPath, syncDir)
			if err != nil {
				return err
			}

			fmt.Printf("Pulled session %q (%s) from %s\n", meta.Title, meta.SessionID[:8], meta.Hostname)

			// Open tmux window and resume
			tc := tmux.NewClient(cfg.TmuxSession)
			if err := tc.EnsureSession(); err != nil {
				return fmt.Errorf("creating tmux session: %w", err)
			}

			windowName := args[0]
			if tc.WindowExists(windowName) {
				fmt.Printf("Window already exists. Use 'mo attach %s' to switch to it.\n", windowName)
				return nil
			}

			if err := launchResumed(cfg, tc, windowName, projectPath, meta.SessionID); err != nil {
				return err
			}

			fmt.Printf("Resumed session in %s\n", windowName)
			return nil
		},
	})

	listSyncCmd := &cobra.Command{
		Use:   "list",
		Short: "List available sessions in the sync repo, grouped by project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			noPull, _ := cmd.Flags().GetBool("no-pull")

			var sessions []moSync.SessionMeta
			if noPull {
				sessions, err = moSync.ListLocal(syncDir)
			} else {
				sessions, err = moSync.List(syncDir)
			}
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				fmt.Println("No synced sessions found")
				return nil
			}

			// Group entries by base project name ("foo" and "foo@branch" both
			// group under "foo"). Entries without an "@" are the main project;
			// entries with "@" are worktree scopes.
			type entry struct {
				branch string // "" for main-project scope
				meta   moSync.SessionMeta
			}
			groups := map[string][]entry{}
			var order []string
			for _, s := range sessions {
				base := s.ProjectName
				branch := ""
				if i := strings.Index(s.ProjectName, "@"); i >= 0 {
					base = s.ProjectName[:i]
					branch = s.ProjectName[i+1:]
				}
				if _, ok := groups[base]; !ok {
					order = append(order, base)
				}
				groups[base] = append(groups[base], entry{branch: branch, meta: s})
			}
			sort.Strings(order)

			total := 0
			for _, base := range order {
				entries := groups[base]
				sort.Slice(entries, func(i, j int) bool {
					// main first, then worktrees alphabetically by branch
					if entries[i].branch == "" {
						return true
					}
					if entries[j].branch == "" {
						return false
					}
					return entries[i].branch < entries[j].branch
				})

				basePath, baseErr := findProjectOrWorktree(cfg, base)
				headerSuffix := ""
				if baseErr != nil {
					headerSuffix = "  (no local project)"
				}
				fmt.Printf("%s%s\n", base, headerSuffix)

				for _, e := range entries {
					scope := "(main)"
					if e.branch != "" {
						scope = "@" + e.branch
					}
					title := e.meta.Title
					if title == "" {
						title = "(untitled)"
					}
					shortID := e.meta.SessionID
					if len(shortID) > 8 {
						shortID = shortID[:8]
					}
					age := time.Since(e.meta.PushedAt).Truncate(time.Minute)

					// Resolve the local path for this scope so we can report
					// whether the decrypted JSONL is already on disk.
					localPath := ""
					if e.branch == "" {
						localPath = basePath
					} else if basePath != "" {
						if wts, wtErr := project.ListWorktrees(basePath); wtErr == nil {
							for _, wt := range wts {
								if wt.Branch == e.branch {
									localPath = wt.Path
									break
								}
							}
						}
					}

					localMarker := ""
					switch {
					case localPath == "":
						localMarker = "  [no local worktree]"
					default:
						jsonl := filepath.Join(claude.ProjectsDirForPath(localPath), e.meta.SessionID+".jsonl")
						if _, err := os.Stat(jsonl); err == nil {
							localMarker = "  [local ✓]"
						} else {
							localMarker = "  [not pulled]"
						}
					}

					fmt.Printf("  %-22s %s  %s  from %s  %s ago%s\n",
						scope, shortID, title, e.meta.Hostname, age, localMarker)
					total++
				}
				fmt.Println()
			}
			fmt.Printf("%d synced sessions across %d project(s)\n", total, len(order))
			return nil
		},
	}
	listSyncCmd.Flags().Bool("no-pull", false, "skip git pull; list only what's already in the local sync clone")
	cmd.AddCommand(listSyncCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "repair-names",
		Short: "Fix sessions pushed with bracket-suffixed window names",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := moSync.RepairNames(syncDir)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Println("No sessions needed repair")
			} else {
				fmt.Printf("Repaired %d session(s)\n", n)
			}
			return nil
		},
	})

	return cmd
}
