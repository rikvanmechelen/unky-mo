package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const jiraTokenGuideURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

func jiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira",
		Short: "Configure the Jira tickets integration",
	}
	cmd.AddCommand(jiraSetupCmd())
	cmd.AddCommand(jiraShowTokenCmd())
	cmd.AddCommand(jiraFetchCmd())
	cmd.AddCommand(jiraIssueCmd())
	return cmd
}

// jiraIssueCmd fetches a single issue's detail and prints it — useful to
// verify the /rest/api/3/issue endpoint, sprint extraction, and HTML
// stripping without having to open the TUI. Symmetric with `mo jira fetch`.
func jiraIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "issue <KEY>",
		Short: "Fetch and print a single Jira issue's detail (diagnostic)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			instances := make([]jira.Instance, 0, len(cfg.Tickets.Jira))
			for _, j := range cfg.Tickets.Jira {
				instances = append(instances, jira.Instance{
					Name:          j.Name,
					BaseURL:       j.BaseURL,
					Email:         j.Email,
					SprintFieldID: j.SprintFieldID,
				})
			}
			providers := jira.BuildProviders(instances)
			if len(providers) == 0 {
				return fmt.Errorf("no providers built — run 'mo jira setup' first")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			key := args[0]
			for _, p := range providers {
				detail, err := p.Detail(ctx, key)
				if err != nil {
					fmt.Printf("%s: ERROR — %s\n", p.Name(), err)
					continue
				}
				fmt.Printf("%s  %s\n", detail.ID, detail.Title)
				fmt.Printf("  Status:   %s\n", detail.RawStatus)
				fmt.Printf("  Priority: %d\n", detail.Priority)
				fmt.Printf("  Reporter: %s\n", detail.Reporter)
				fmt.Printf("  Assignee: %s\n", detail.AssigneeDisplay)
				if detail.InSprint {
					fmt.Printf("  Sprint:   %s (active)\n", detail.SprintName)
				}
				fmt.Printf("  URL:      %s\n", detail.URL)
				fmt.Println()
				fmt.Println("Description:")
				if detail.DescriptionText == "" {
					fmt.Println("(none)")
				} else {
					fmt.Println(detail.DescriptionText)
				}
				return nil
			}
			return fmt.Errorf("issue %q not found in any configured instance", key)
		},
	}
}

// jiraFetchCmd runs a single MyTickets call against each configured provider
// and prints a count + any error. Diagnostic command so the user can see
// exactly what the TUI's background fetch would produce without having to
// read it out of the panel.
func jiraFetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch",
		Short: "Run one fetch against each configured Jira instance and print the result",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			instances := make([]jira.Instance, 0, len(cfg.Tickets.Jira))
			for _, j := range cfg.Tickets.Jira {
				instances = append(instances, jira.Instance{
					Name:          j.Name,
					BaseURL:       j.BaseURL,
					Email:         j.Email,
					SprintFieldID: j.SprintFieldID,
				})
			}
			providers := jira.BuildProviders(instances)
			if len(providers) == 0 {
				return fmt.Errorf("no providers built — run 'mo jira setup' first")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			for _, p := range providers {
				name := p.Name()
				tickets, err := p.MyTickets(ctx)
				if err != nil {
					fmt.Printf("%s: ERROR — %s\n", name, err)
					continue
				}
				fmt.Printf("%s: %d ticket(s)\n", name, len(tickets))
			}
			return nil
		},
	}
}

func jiraSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup: prompts for Jira URL + email, reads API token securely, writes token file + config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJiraSetup()
		},
	}
}

func jiraShowTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-token",
		Short: "Print the current Jira API token (intended for copying to another machine)",
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := jira.LoadToken()
			if err != nil {
				return err
			}
			if tok == "" {
				return fmt.Errorf("no Jira token configured — run 'mo jira setup'")
			}
			fmt.Println(tok)
			return nil
		},
	}
}

func runJiraSetup() error {
	fmt.Println("Unky Mo — Jira setup")
	fmt.Println()
	fmt.Println("This writes:")
	fmt.Printf("  · token  → %s (mode 0600)\n", jira.DefaultTokenPath())
	fmt.Printf("  · config → %s (appends [[tickets.jira]] block)\n", config.DefaultConfigPath())
	fmt.Println()
	fmt.Printf("You'll need a Jira API token. Create one at:\n  %s\n\n", jiraTokenGuideURL)

	reader := bufio.NewReader(os.Stdin)

	baseURL, err := promptLine(reader, "Jira base URL (e.g. https://moma.atlassian.net): ", true)
	if err != nil {
		return err
	}
	baseURL = normalizeBaseURL(baseURL)

	email, err := promptLine(reader, "Atlassian email: ", true)
	if err != nil {
		return err
	}

	token, err := promptSecret("API token (input hidden): ")
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Verifying credentials…")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	who, err := verifyJiraCreds(ctx, baseURL, email, token)
	if err != nil {
		return fmt.Errorf("verification failed: %w\n(check the URL, email, and token at %s)", err, jiraTokenGuideURL)
	}
	fmt.Printf("Authenticated as %s.\n", who)

	// Write token file (refuses to overwrite without consent).
	tokenPath := jira.DefaultTokenPath()
	force := false
	if _, err := os.Stat(tokenPath); err == nil {
		overwrite, err := confirm(reader, fmt.Sprintf("Token file already exists at %s. Overwrite? [y/N]: ", tokenPath), false)
		if err != nil {
			return err
		}
		if !overwrite {
			fmt.Println("Leaving existing token file in place.")
		} else {
			force = true
		}
	}
	if force || !fileExists(tokenPath) {
		if _, err := jira.WriteToken(token, force); err != nil {
			return fmt.Errorf("write token: %w", err)
		}
		fmt.Printf("Wrote token to %s\n", tokenPath)
	}

	// Append config block if missing.
	added, err := ensureJiraConfigBlock(baseURL, email)
	if err != nil {
		return fmt.Errorf("update config: %w", err)
	}
	if added {
		fmt.Printf("Added [[tickets.jira]] block to %s\n", config.DefaultConfigPath())
	} else {
		fmt.Printf("Config already has a [[tickets.jira]] entry for %s — left as-is.\n", baseURL)
	}

	fmt.Println()
	fmt.Println("Done. Open Mo and press ctrl+alt+r to refresh, then the Tickets panel will populate.")
	return nil
}

// ensureJiraConfigBlock appends a new [[tickets.jira]] block to the user's
// config.toml if one for the given base_url doesn't already exist. Returns
// true when a block was added. Creates the config file if it doesn't exist.
func ensureJiraConfigBlock(baseURL, email string) (bool, error) {
	path := config.DefaultConfigPath()
	cfg, err := config.Load()
	if err != nil {
		return false, fmt.Errorf("load existing config: %w", err)
	}
	for _, j := range cfg.Tickets.Jira {
		if strings.EqualFold(strings.TrimRight(j.BaseURL, "/"), strings.TrimRight(baseURL, "/")) {
			return false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}

	block := fmt.Sprintf("\n[[tickets.jira]]\nbase_url = %q\nemail = %q\n", baseURL, email)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return false, err
	}
	return true, nil
}

// verifyJiraCreds is a thin wrapper over jira.VerifyCreds kept for backward
// compatibility with the existing cmd/mo tests. New callers should use the
// jira package directly.
func verifyJiraCreds(ctx context.Context, baseURL, email, token string) (string, error) {
	return jira.VerifyCreds(ctx, baseURL, email, token)
}

// promptLine reads a single trimmed line from stdin, re-prompting on empty
// input when required is true.
func promptLine(r *bufio.Reader, prompt string, required bool) (string, error) {
	for {
		fmt.Print(prompt)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" && required {
			fmt.Println("  (required)")
			continue
		}
		return line, nil
	}
}

// promptSecret reads a password from the controlling terminal without echo.
// Falls back to a plain read if stdin isn't a TTY (e.g. piped input), with a
// warning so the user knows the token was echoed.
func promptSecret(prompt string) (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr, "warning: stdin is not a terminal — token input will be echoed")
		return promptLine(bufio.NewReader(os.Stdin), prompt, true)
	}
	fmt.Print(prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("empty token")
	}
	return token, nil
}

func confirm(r *bufio.Reader, prompt string, def bool) (bool, error) {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return def, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def, nil
	}
	return line == "y" || line == "yes", nil
}

func normalizeBaseURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "https://" + s
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractJiraMessage pulls the human-readable message out of a Jira error
// body (errorMessages[] / errors{} / message). Falls back to a truncated
// raw body when none of those fields are populated.
func extractJiraMessage(body []byte) string {
	var j struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if err := json.Unmarshal(body, &j); err == nil {
		var parts []string
		parts = append(parts, j.ErrorMessages...)
		for field, msg := range j.Errors {
			parts = append(parts, field+": "+msg)
		}
		if j.Message != "" {
			parts = append(parts, j.Message)
		}
		if len(parts) > 0 {
			s := strings.Join(parts, "; ")
			if len(s) > 200 {
				s = s[:200] + "..."
			}
			return s
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
