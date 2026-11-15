package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
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
	rootCmd.AddCommand(architectureCmd())
	rootCmd.AddCommand(callsCmd())
	rootCmd.AddCommand(hooksCmd())
	rootCmd.AddCommand(syncCmd())
	rootCmd.AddCommand(jiraCmd())
	rootCmd.AddCommand(agentsCmd())
	rootCmd.AddCommand(teamsCmd())
	rootCmd.AddCommand(suspendCmd())
	rootCmd.AddCommand(sidebarCmd())
	rootCmd.AddCommand(debugCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(webCmd())
	rootCmd.AddCommand(restartCmd())
	rootCmd.AddCommand(snapshotCmd())

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

	// Auto-resume suspended sessions before starting the TUI. Done here
	// (not inside Init) to avoid racing with refreshSessions/ListWindows.
	suspendPath := ops.SuspendedStatePath()
	if ops.HasSuspendedState(suspendPath) {
		tc := tmux.NewClient(cfg.TmuxSession)
		ctx := ops.NewContext(tc)
		res, err := ops.ResumeAll(ctx, suspendPath, ops.RestoreParams{Agents: cfg.Agents})
		if err != nil {
			fmt.Printf("Resume warning: %v\n", err)
		} else if res.Resumed > 0 {
			fmt.Printf("Resumed %d suspended session(s)\n", res.Resumed)
			if res.Skipped > 0 {
				fmt.Printf("  %d session(s) skipped\n", res.Skipped)
			}
			// Brief pause so Claude processes register their session files
			// before the TUI's first refreshSessions poll.
			time.Sleep(500 * time.Millisecond)
		}
	}

	projects, err := cfg.LoadProjects()
	if err != nil {
		return fmt.Errorf("loading projects: %w", err)
	}
	notices, warning := startupChecks(cfg)
	notice := strings.Join(notices, " · ")
	sticky := warning != ""
	if sticky {
		// The warning is several lines; the other notices go below it.
		notice = strings.TrimSuffix(warning+"\n"+notice, "\n")
	}
	return tui.Run(projects, cfg.TmuxSession, cfg.SocketPath, cfg.StateFilePath, cfg.Tickets, cfg.Agents, cfg.WorkspaceDirs, cfg.Projects, notice, sticky)
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

			agent := cfg.DefaultAgent()
			shellCmd := agent.Cmd
			if prompt != "" {
				// Single-quoted: Go's %q is double quotes, where the shell
				// still expands $(…), backticks and $VAR in the prompt.
				shellCmd = agent.Cmd + " -p " + ops.ShellQuote(prompt)
			}

			ctx := ops.NewContext(tc)
			// CLI uses a narrower sidebar historically (33 cols) — preserve.
			ctx.SidebarWidth = 33
			res, err := ops.LaunchSession(ctx, ops.LaunchParams{
				WindowName:    windowName,
				Cwd:           projectPath,
				ShellCmd:      shellCmd,
				AgentKey:      agent.Key,
				AttachSidebar: true,
				SwitchFocus:   false, // CLI invocation from outside tmux: skip switch
			})
			if err != nil {
				return err
			}
			fmt.Printf("Started %s session in %s (tmux: %s)\n", agent.Name, windowName, res.Target)
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

			if err := launchResumed(cfg, tc, windowName, projectPath, sessionID); err != nil {
				return err
			}
			fmt.Printf("Resuming session %s in %s\n", sessionID, windowName)
			return nil
		},
	}
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
		Short: "Install Unky Mo status hooks into Claude settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			statusScript, _, err := installStatusHooks(cfg)
			if err != nil {
				return err
			}
			fmt.Println("Hooks installed into", claude.ClaudeSettingsPath())
			fmt.Println("  Status hook:", statusScript)
			return nil
		},
	})

	var socket string
	var hc notify.HookContext
	sendCmd := &cobra.Command{
		Use:    "send",
		Short:  "Forward one hook event from stdin to the TUI's socket (used by the hook script)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if socket == "" {
				socket = config.DefaultSocketPath()
			}
			// MO_HOOK_DEBUG_LOG=<file> also appends every message to that
			// file — set it in a Claude session's environment (e.g. via
			// `tmux set-environment -g` before launching one) to see the raw
			// hook events Claude Code fires.
			logPath := os.Getenv("MO_HOOK_DEBUG_LOG")
			if !cmd.Flags().Changed("event") {
				// An older hook script sends the whole envelope itself.
				return notify.SendLogged(socket, os.Stdin, logPath)
			}
			return notify.SendHook(socket, os.Stdin, hc, logPath)
		},
	}
	sendCmd.Flags().StringVar(&socket, "socket", "", "TUI notification socket (default: the configured one)")
	sendCmd.Flags().StringVar(&hc.Event, "event", "", "the hook event's name")
	sendCmd.Flags().StringVar(&hc.SessionID, "session", "", "the session id from the environment")
	sendCmd.Flags().StringVar(&hc.Cwd, "cwd", "", "the hook's working directory")
	sendCmd.Flags().StringVar(&hc.Pane, "pane", "", "the tmux pane")
	cmd.AddCommand(sendCmd)

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
			if claude.HooksV2Installed() {
				fmt.Println("Hooks (v2) are installed in", claude.ClaudeSettingsPath())
			} else if claude.HooksInstalled() {
				fmt.Println("Hooks (v1) are installed in", claude.ClaudeSettingsPath())
				fmt.Println("Run 'mo hooks install' to upgrade to v2.")
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

// launchResumed opens a window resuming sessionID with the default agent,
// the way every other launch does (ops.LaunchSession): instance id, the
// pane-exited hook bound to Claude's pane, the sidebar, asdf shims.
func launchResumed(cfg *config.Config, tc *tmux.Client, windowName, cwd, sessionID string) error {
	agent := cfg.DefaultAgent()
	ctx := ops.NewContext(tc)
	ctx.SidebarWidth = 33 // the CLI's narrower sidebar, as mo start
	_, err := ops.LaunchSession(ctx, ops.LaunchParams{
		WindowName:    windowName,
		Cwd:           cwd,
		ShellCmd:      ops.AgentShellCmd(agent, sessionID),
		AgentKey:      agent.Key,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	return err
}
