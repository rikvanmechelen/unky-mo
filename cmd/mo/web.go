package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
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
				Review:     web.NewChangeAnalyzer(moexec.DefaultCommander),
				Calls:      web.NewCallGrapher(moexec.DefaultCommander),
				Scope:      web.NewScopeChecker(moexec.DefaultCommander),
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
			if !cfg.Web.DisableBashDiffs {
				if store, err := bashsnap.NewStore(moexec.DefaultCommander); err == nil {
					deps.BashDiffs = web.NewBashChanges(store)
				}
			}

			if !cfg.Web.DisablePush {
				if deps.Push, err = web.NewPushService(secretPath("push"), nil, nil); err != nil {
					return fmt.Errorf("push: %w", err)
				}
			}

			credsPath := webCredentialsPath()
			creds, err := web.LoadCredentials(credsPath)
			if err != nil {
				return err
			}
			if creds == nil && !web.IsLoopbackAddr(addr) {
				return fmt.Errorf("refusing to serve on non-loopback %s without auth: run 'mo web auth set' first", addr)
			}

			// Loaded before the server is built so /ca.crt can hand out the CA.
			// The generated cert is served from a CertSource that re-signs it
			// when this machine's addresses change; cert_file stays fixed.
			hosts := func() []string { return web.TLSHosts(addr, cfg.Web.ExtraHosts) }
			var certs *web.CertSource
			var fixed tls.Certificate
			var res *web.TLSResult
			if !cfg.Web.DisableTLS {
				if cfg.Web.CertFile != "" || cfg.Web.KeyFile != "" {
					if fixed, _, err = loadWebTLS(cfg.Web, addr); err != nil {
						return fmt.Errorf("tls: %w", err)
					}
				} else {
					if certs, res, err = web.NewCertSource(webTLSDir(), hosts, nil); err != nil {
						return fmt.Errorf("tls: %w", err)
					}
					deps.CA = res.CA
					go certs.Run(cmd.Context(), 30*time.Second)
				}
			}

			scheme := "https"
			if cfg.Web.DisableTLS {
				scheme = "http"
			}
			machine, err := web.MachineID(config.DefaultStateDir())
			if err != nil {
				return err
			}
			hostname, _ := os.Hostname()
			deps.Hub = &web.HubInfo{
				Machine: machine,
				Name:    strings.TrimSuffix(hostname, ".local"),
				Version: version,
				Addrs:   func() []string { return web.HubAddrs(scheme, addr, hosts()) },
			}

			if creds != nil {
				deps.Login = creds
				deps.Devices = web.NewDeviceStore(webDevicesPath(), nil)
			}

			refresh := time.Duration(cfg.Tickets.RefreshSeconds) * time.Second
			webServer := web.NewServer(deps, refresh, cfg.TmuxSession)
			if deps.Push != nil {
				go webServer.RunNotifier(cmd.Context(), time.Second)
			}
			var handler http.Handler = webServer
			authNote := "no auth"
			if creds != nil {
				handler = web.RequireLogin(handler, creds, deps.Devices)
				authNote = "login as " + creds.Username
			}

			if cfg.Web.DisableTLS {
				// Without TLS, DNS rebinding could make a hostile page
				// same-origin with the dashboard: answer only our own names.
				webServer.SetPlainHTTPHosts(hosts)
				fmt.Printf("mo web listening on http://%s (%s, TLS disabled)\n", addr, authNote)
				return http.ListenAndServe(addr, handler)
			}

			tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
			leaf := func() *x509.Certificate { return fixed.Leaf }
			if certs != nil {
				tlsConfig.GetCertificate = certs.GetCertificate
				leaf = certs.Leaf
			} else {
				tlsConfig.Certificates = []tls.Certificate{fixed}
			}
			srv := &http.Server{
				Handler:           handler,
				TLSConfig:         tlsConfig,
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
				l := leaf()
				return l != nil && l.VerifyHostname(host) == nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7890", "address to listen on")
	cmd.AddCommand(webAuthCmd())
	cmd.AddCommand(webDevicesCmd())
	cmd.AddCommand(webTLSCmd())
	return cmd
}

// webCredentialsPath is where `mo web auth set` stores the dashboard login
// (username + PBKDF2 password hash, mode 0600).
func webCredentialsPath() string {
	return filepath.Join(config.DefaultConfigDir(), "web-auth.toml")
}

// webDevicesPath holds the devices paired with the login (token hashes,
// mode 0600).
func webDevicesPath() string {
	secretPath("web-devices.key") // the ticket key moves with the device list
	return secretPath("web-devices.json")
}

func webDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List or revoke the browsers that logged in to mo web",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the paired devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			devices := web.NewDeviceStore(webDevicesPath(), nil).List()
			if len(devices) == 0 {
				fmt.Println("No paired devices.")
				return nil
			}
			for _, d := range devices {
				fmt.Printf("%s  %-32s  last used %s  (paired %s)\n", d.ID, d.Label,
					d.LastUsed.Local().Format("2006-01-02 15:04"), d.Created.Local().Format("2006-01-02"))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <id>",
		Short: "Log a device out (it has to log in again with the password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !web.NewDeviceStore(webDevicesPath(), nil).Revoke(args[0]) {
				return fmt.Errorf("no paired device %q (see 'mo web devices list')", args[0])
			}
			fmt.Printf("Revoked %s.\n", args[0])
			return nil
		},
	})
	return cmd
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

// secretPath is where one of mo web's long-lived secrets lives (name is a
// file or directory: "tls", "push", "web-devices.json", "web-devices.key"):
// the state dir, only this user's, and not the config dir, which dotfiles
// repos often share (a published CA key would let anyone impersonate this
// machine to every device that trusts it). A secret still in the config
// dir from an earlier version is moved over, unchanged, the first time:
// renamed, so the CA every device trusts stays the same one.
func secretPath(name string) string {
	stateDir := config.DefaultStateDir()
	newPath := filepath.Join(stateDir, name)
	if _, err := os.Lstat(newPath); err == nil {
		return newPath
	}
	oldPath := filepath.Join(config.DefaultConfigDir(), name)
	if _, err := os.Lstat(oldPath); err == nil {
		if config.PrivateStateDir(stateDir) != nil || os.Rename(oldPath, newPath) != nil {
			return oldPath // couldn't move it (another filesystem?): keep using it
		}
	}
	return newPath
}
