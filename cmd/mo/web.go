package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/web"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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

			tmuxClient := tmux.NewClient(cfg.TmuxSession)
			deps := web.Deps{
				State:      web.NewStateReader(cfg.StateFilePath),
				Projects:   web.NewProjectLister(cfg.LoadProjects),
				Worktrees:  web.NewWorktreeReader(),
				PRs:        web.NewPRClient(github.NewClient(nil)),
				Tickets:    web.NewTicketSource(jira.BuildProviders(jiraInstancesFromConfig(cfg.Tickets))),
				Prompts:    web.NewPromptSender(tmuxClient),
				History:    web.NewSessionHistory(),
				Sessions:   web.NewSessionOps(ops.NewContext(tmuxClient)),
				Git:        web.NewGitFiles(moexec.DefaultCommander),
				Terminals:  web.NewTerminals(tmuxClient),
				ClaudePane: web.NewClaudePane(tmuxClient),
				Shells:     web.NewShells(),
				Subagents:  claude.NewSubagentReader(),
				Commands:   web.NewSlashCommands(),
				Restarter:  web.NewRestarter(cfg.SocketPath),
				Agents:     cfg.Agents,
			}
			if deps.Attachments, err = web.NewAttachmentStore(); err != nil {
				return err
			}

			credsPath := webCredentialsPath()
			creds, err := web.LoadCredentials(credsPath)
			if err != nil {
				return err
			}
			if creds == nil && !web.IsLoopbackAddr(addr) {
				return fmt.Errorf("refusing to serve on non-loopback %s without auth: run 'mo web auth set' first", addr)
			}

			refresh := time.Duration(cfg.Tickets.RefreshSeconds) * time.Second
			var handler http.Handler = web.NewServer(deps, refresh, cfg.TmuxSession)
			authNote := "no auth"
			if creds != nil {
				handler = web.BasicAuth(handler, creds)
				authNote = "basic auth as " + creds.Username
			}

			if cfg.Web.DisableTLS {
				fmt.Printf("mo web listening on http://%s (%s, TLS disabled)\n", addr, authNote)
				return http.ListenAndServe(addr, handler)
			}

			cert, res, err := loadWebTLS(cfg.Web, addr)
			if err != nil {
				return fmt.Errorf("tls: %w", err)
			}
			srv := &http.Server{
				Handler:           handler,
				TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
				ReadHeaderTimeout: 10 * time.Second,
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			fmt.Printf("mo web listening on https://%s (%s)\n", addr, authNote)
			if res != nil {
				if res.NewCA {
					fmt.Printf("created a local CA: %s — run 'mo web tls trust' to trust it\n", res.Files.CA)
				}
				fmt.Printf("CA sha256 %s\n", web.Fingerprint(res.CA))
			}
			return web.ServeTLSOrRedirect(ln, srv, func(host string) bool {
				return cert.Leaf != nil && cert.Leaf.VerifyHostname(host) == nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7890", "address to listen on")
	cmd.AddCommand(webAuthCmd())
	cmd.AddCommand(webTLSCmd())
	return cmd
}

// webCredentialsPath is where `mo web auth set` stores the dashboard login
// (username + PBKDF2 password hash, mode 0600).
func webCredentialsPath() string {
	return filepath.Join(config.DefaultConfigDir(), "web-auth.toml")
}

func webAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the basic-auth login for mo web",
	}

	var username string
	setCmd := &cobra.Command{
		Use:   "set",
		Short: "Set the mo web username and password (stored hashed, never in plaintext)",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)
			if username == "" {
				fmt.Print("Username: ")
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("read username: %w", err)
				}
				username = strings.TrimSpace(line)
			}
			password, err := readWebPassword(reader)
			if err != nil {
				return err
			}
			creds, err := web.NewCredentials(username, password)
			if err != nil {
				return err
			}
			path := webCredentialsPath()
			if err := web.SaveCredentials(path, creds); err != nil {
				return fmt.Errorf("save credentials: %w", err)
			}
			fmt.Printf("Saved mo web login for %q to %s\n", username, path)
			fmt.Println("Restart any running 'mo web' to pick it up.")
			return nil
		},
	}
	setCmd.Flags().StringVar(&username, "username", "", "username (prompted if omitted)")
	cmd.AddCommand(setCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether a mo web login is configured",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := webCredentialsPath()
			creds, err := web.LoadCredentials(path)
			if err != nil {
				return err
			}
			if creds == nil {
				fmt.Println("No mo web login configured (dashboard is localhost-only). Run 'mo web auth set'.")
				return nil
			}
			fmt.Printf("mo web login configured for %q (%s)\n", creds.Username, path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Remove the mo web login (dashboard goes back to localhost-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := webCredentialsPath()
			if err := os.Remove(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Println("No mo web login configured.")
					return nil
				}
				return err
			}
			fmt.Printf("Removed %s\n", path)
			return nil
		},
	})

	return cmd
}

// readWebPassword reads the password without echo, asking twice to catch
// typos. When stdin isn't a terminal (piped input) it reads one line
// instead, so the command stays scriptable.
func readWebPassword(reader *bufio.Reader) (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Print("Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Print("Confirm password: ")
	second, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passwords don't match")
	}
	return string(first), nil
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
