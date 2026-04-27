package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/tui"
	"github.com/rvanmech/unky-mo/internal/tui/sidebar"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:   "mo",
		Short: "Unky Mo — Claude Code session orchestrator",
		Long:  "Manage multiple Claude Code sessions across your projects from a single TUI.",
		RunE:  runTUI,
	}

	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(scanCmd())
	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(attachCmd())
	rootCmd.AddCommand(sessionsCmd())
	rootCmd.AddCommand(resumeCmd())
	rootCmd.AddCommand(parkCmd())
	rootCmd.AddCommand(cleanupCmd())
	rootCmd.AddCommand(importCmd())
	rootCmd.AddCommand(concurrentCmd())
	rootCmd.AddCommand(hooksCmd())
	rootCmd.AddCommand(syncCmd())
	rootCmd.AddCommand(jiraCmd())
	rootCmd.AddCommand(sidebarCmd())
	rootCmd.AddCommand(debugCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// If not inside tmux, auto-launch into a tmux session
	if !tmux.IsInsideTmux() {
		return launchInTmux(cfg.TmuxSession)
	}

	projects, err := cfg.LoadProjects()
	if err != nil {
		return fmt.Errorf("loading projects: %w", err)
	}
	return tui.Run(projects, cfg.TmuxSession, cfg.SocketPath, cfg.StateFilePath, cfg.Tickets)
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all known projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			projects, err := cfg.LoadProjects()
			if err != nil {
				return fmt.Errorf("loading projects: %w", err)
			}
			if len(projects) == 0 {
				fmt.Println("No projects found. Configure workspace_dirs in", config.DefaultConfigPath())
				return nil
			}
			for _, p := range projects {
				lang := p.Language
				if lang == "" {
					lang = "?"
				}
				desc := ""
				if p.Description != "" {
					desc = " — " + p.Description
				}
				fmt.Printf("  %-30s [%-6s] %s%s\n", p.Name, lang, p.Path, desc)
			}
			fmt.Printf("\n%d projects found\n", len(projects))
			return nil
		},
	}
}

func scanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Re-scan workspace directories for projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if len(cfg.WorkspaceDirs) == 0 {
				fmt.Println("No workspace_dirs configured in", config.DefaultConfigPath())
				return nil
			}
			projects, err := cfg.LoadProjects()
			if err != nil {
				return fmt.Errorf("scanning: %w", err)
			}
			for _, p := range projects {
				lang := p.Language
				if lang == "" {
					lang = "?"
				}
				fmt.Printf("  %-30s [%-6s] %s\n", p.Name, lang, p.Path)
			}
			fmt.Printf("\n%d projects found in %v\n", len(projects), cfg.WorkspaceDirs)
			return nil
		},
	}
}

// launchInTmux creates (or attaches to) the mo tmux session and runs the mo binary inside it.
func launchInTmux(sessionName string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}

	tc := tmux.NewClient(sessionName)
	if tc.SessionExists() {
		// Check if TUI is alive in window 0
		paneCmd := tc.PaneCommand(sessionName + ":0")
		if paneCmd != "mo" && paneCmd != "" {
			// TUI died, shell is showing — restart it
			fmt.Printf("Restarting TUI in existing session %q...\n", sessionName)
			tc.SendKeys(sessionName+":0", self)
		} else {
			fmt.Printf("Attaching to existing session %q...\n", sessionName)
		}
		tc.EnableMouse()
		tc.ConfigureStatusFormat()
		argv := []string{"tmux", "attach-session", "-t", sessionName}
		return syscall.Exec(tmuxBin, argv, os.Environ())
	}

	// Create a new session detached so we can configure it (mouse, etc.) before
	// attaching. Using new-session + attach instead of a single foreground
	// new-session ensures the session options are applied even when the user
	// has no global tmux mouse setting (common on fresh Linux installs).
	fmt.Printf("Starting tmux session %q...\n", sessionName)
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, self).Run(); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}
	tc.EnableMouse()
	tc.ConfigureStatusFormat()
	argv := []string{"tmux", "attach-session", "-t", sessionName}
	return syscall.Exec(tmuxBin, argv, os.Environ())
}

// addCLISidebarPane adds a sidebar pane to a tmux window target with the
// given cwd.
func addCLISidebarPane(tc *tmux.Client, target, cwd string) {
	moPath, err := os.Executable()
	if err != nil {
		return
	}
	sidebarCmd := fmt.Sprintf("%s sidebar", moPath)
	tc.SplitWindow(target, 33, cwd, sidebarCmd)
	tc.SelectPane(target + ".0")
}

func findProject(cfg *config.Config, name string) (string, error) {
	projects, err := cfg.LoadProjects()
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Name == name {
			return p.Path, nil
		}
	}
	return "", fmt.Errorf("project %q not found. Run 'mo list' to see available projects", name)
}

// findProjectOrWorktree resolves a sync project name to a local filesystem
// path. A bare name (e.g. "unky-mo") resolves to the project root; a
// worktree name (e.g. "unky-mo@feature-branch") resolves to the local
// worktree whose git branch matches the suffix.
func findProjectOrWorktree(cfg *config.Config, name string) (string, error) {
	base, branch, isWT := strings.Cut(name, "@")
	mainPath, err := findProject(cfg, base)
	if err != nil {
		return "", err
	}
	if !isWT {
		return mainPath, nil
	}
	wts, err := project.ListWorktrees(mainPath)
	if err != nil {
		return "", fmt.Errorf("listing worktrees for %s: %w", base, err)
	}
	for _, wt := range wts {
		if wt.Path != mainPath && wt.Branch == branch {
			return wt.Path, nil
		}
	}
	return "", fmt.Errorf("no local worktree on branch %q for project %q", branch, base)
}

func startCmd() *cobra.Command {
	var prompt string
	cmd := &cobra.Command{
		Use:   "start <project>",
		Short: "Start a new Claude session in a project",
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
				return fmt.Errorf("creating tmux session: %w", err)
			}

			windowName := args[0]
			if tc.WindowExists(windowName) {
				fmt.Printf("Session already exists for %s. Use 'mo attach %s' to switch to it.\n", windowName, windowName)
				return nil
			}

			shellCmd := "claude"
			if prompt != "" {
				shellCmd = fmt.Sprintf("claude -p %q", prompt)
			}

			ctx := ops.NewContext(tc)
			// CLI uses a narrower sidebar historically (33 cols) — preserve.
			ctx.SidebarWidth = 33
			res, err := ops.LaunchSession(ctx, ops.LaunchParams{
				WindowName:    windowName,
				Cwd:           projectPath,
				ShellCmd:      shellCmd,
				AttachSidebar: true,
				SwitchFocus:   false, // CLI invocation from outside tmux: skip switch
			})
			if err != nil {
				return err
			}
			fmt.Printf("Started Claude session in %s (tmux: %s)\n", windowName, res.Target)
			return nil
		},
	}
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "Initial prompt for Claude")
	return cmd
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <project>",
		Short: "Switch to a project's tmux window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			tc := tmux.NewClient(cfg.TmuxSession)
			windowName := args[0]

			windows, err := tc.ListWindows()
			if err != nil {
				return fmt.Errorf("listing windows: %w", err)
			}
			target := ""
			for _, w := range windows {
				if w.Name == windowName {
					target = cfg.TmuxSession + ":" + w.ID
					break
				}
			}
			if target == "" {
				return fmt.Errorf("no session for %q. Use 'mo start %s' to create one", windowName, windowName)
			}

			if err := tc.SwitchToWindow(target); err != nil {
				return fmt.Errorf("switching to window: %w", err)
			}

			fmt.Printf("Attached to %s\n", windowName)
			return nil
		},
	}
}

func sessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List active Claude Code sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := claude.LiveSessions()
			if err != nil {
				return fmt.Errorf("reading sessions: %w", err)
			}
			if len(sessions) == 0 {
				fmt.Println("No active Claude sessions")
				return nil
			}
			for _, s := range sessions {
				name := s.Name
				if name == "" {
					name = "(unnamed)"
				}
				age := time.Since(time.UnixMilli(s.StartedAt)).Truncate(time.Minute)
				fmt.Printf("  %-20s %-36s PID:%-6d %s ago  %s\n", name, s.SessionID, s.PID, age, s.CWD)
			}
			fmt.Printf("\n%d active sessions\n", len(sessions))
			return nil
		},
	}
}

func resumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <project>",
		Short: "Resume the most recent Claude session in a project",
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

			// Find session to resume: prefer live session, fall back to most recent historical
			var sessionID string
			if live := claude.SessionForPath(projectPath); live != nil {
				sessionID = live.SessionID
			} else {
				recent := claude.RecentSessions(projectPath, 1)
				if len(recent) > 0 {
					sessionID = recent[0].SessionID
				}
			}
			if sessionID == "" {
				return fmt.Errorf("no sessions found for %q", args[0])
			}

			tc := tmux.NewClient(cfg.TmuxSession)
			if err := tc.EnsureSession(); err != nil {
				return fmt.Errorf("creating tmux session: %w", err)
			}

			windowName := args[0]
			if tc.WindowExists(windowName) {
				fmt.Printf("Window already exists. Use 'mo attach %s' to switch to it.\n", windowName)
				return nil
			}

			target, err := tc.CreateWindow(windowName, projectPath)
			if err != nil {
				return fmt.Errorf("creating window: %w", err)
			}

			resumeCmd := fmt.Sprintf("exec claude --resume %s", sessionID)
			if err := tc.SendKeys(target, resumeCmd); err != nil {
				return fmt.Errorf("resuming session: %w", err)
			}

			tc.SetWindowHook(target, "pane-exited", "kill-window")
			addCLISidebarPane(tc, target, projectPath)

			fmt.Printf("Resuming session %s in %s\n", sessionID, windowName)
			return nil
		},
	}
}

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

			target, err := tc.CreateWindow(windowName, projectPath)
			if err != nil {
				return fmt.Errorf("creating window: %w", err)
			}

			resumeCmd := fmt.Sprintf("exec claude --resume %s", meta.SessionID)
			if err := tc.SendKeys(target, resumeCmd); err != nil {
				return fmt.Errorf("resuming session: %w", err)
			}

			tc.SetWindowHook(target, "pane-exited", "kill-window")
			addCLISidebarPane(tc, target, projectPath)

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

func debugCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "debug <project>",
		Short: "Show debug info for a project (sessions, worktrees, encoding)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			projectPath, err := findProject(cfg, args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Project: %s\n", args[0])
			fmt.Printf("Path: %s\n", projectPath)
			fmt.Printf("Claude dir: %s\n\n", claude.ProjectsDirForPath(projectPath))

			// Main sessions
			mainSessions := claude.RecentSessions(projectPath, 10)
			fmt.Printf("Main sessions: %d\n", len(mainSessions))
			for _, s := range mainSessions {
				fmt.Printf("  %s  %s  %s\n", s.DisplayName(), s.SessionID[:8], s.LastActive.Format("Jan 02 15:04"))
			}

			// Worktrees
			wts, _ := project.ListWorktrees(projectPath)
			fmt.Printf("\nWorktrees: %d\n", len(wts))
			for _, wt := range wts {
				if wt.Path == projectPath {
					fmt.Printf("  %s  %s  (main - skipped)\n", wt.Branch, wt.Path)
					continue
				}
				claudeDir := claude.ProjectsDirForPath(wt.Path)
				fmt.Printf("  %s  %s\n", wt.Branch, wt.Path)
				fmt.Printf("    Claude dir: %s\n", claudeDir)

				// Check if Claude dir exists
				if _, err := os.Stat(claudeDir); err != nil {
					fmt.Printf("    ERROR: Claude dir does not exist!\n")
				}

				wtSessions := claude.RecentSessions(wt.Path, 5)
				fmt.Printf("    Sessions: %d\n", len(wtSessions))
				for _, s := range wtSessions {
					fmt.Printf("      %s  %s  %s\n", s.DisplayName(), s.SessionID[:8], s.LastActive.Format("Jan 02 15:04"))
				}
			}

			return nil
		},
	}
}

func sidebarCmd() *cobra.Command {
	var instanceID string
	cmd := &cobra.Command{
		Use:    "sidebar",
		Short:  "Run the session status sidebar (used in tmux panes)",
		Hidden: true, // Internal command, launched automatically
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tmux.IsInsideTmux() {
				return fmt.Errorf("sidebar must run inside tmux")
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			session := tmux.CurrentSessionName()
			if session == "" {
				session = cfg.TmuxSession
			}

			return sidebar.RunWithOpts(sidebar.RunOpts{
				SessionName: session,
				StateFile:   cfg.StateFilePath,
				InstanceID:  instanceID,
			})
		},
	}
	cmd.Flags().StringVar(&instanceID, "instance-id", "", "Mo-generated instance ID for this window")
	return cmd
}

func hooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage Claude Code notification hooks",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install Unky Mo notification hooks into Claude settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			notifyScript, _ := filepath.Abs("scripts/notify-hook.sh")
			stopScript, _ := filepath.Abs("scripts/stop-hook.sh")

			// Check scripts exist
			for _, s := range []string{notifyScript, stopScript} {
				if _, err := os.Stat(s); err != nil {
					return fmt.Errorf("script not found: %s\nRun this from the unky-mo project directory", s)
				}
			}

			if err := claude.InstallHooks(notifyScript, stopScript); err != nil {
				return fmt.Errorf("installing hooks: %w", err)
			}
			fmt.Println("Hooks installed into", claude.ClaudeSettingsPath())
			fmt.Println("  Notification hook:", notifyScript)
			fmt.Println("  Stop hook:", stopScript)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove Unky Mo hooks from Claude settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := claude.UninstallHooks(); err != nil {
				return fmt.Errorf("uninstalling hooks: %w", err)
			}
			fmt.Println("Hooks removed from", claude.ClaudeSettingsPath())
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check if Unky Mo hooks are installed",
		Run: func(cmd *cobra.Command, args []string) {
			if claude.HooksInstalled() {
				fmt.Println("Hooks are installed in", claude.ClaudeSettingsPath())
			} else {
				fmt.Println("Hooks are NOT installed. Run 'mo hooks install' to set up.")
			}
		},
	})

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("mo", version)
		},
	}
}
