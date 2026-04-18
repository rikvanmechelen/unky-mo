package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/tickets"
)

// EnvToken is the env var that supplies the Jira API token. Overrides the
// token file when set; useful for secret-manager integrations (pass,
// 1Password CLI) that prefer not to touch the filesystem.
const EnvToken = "UNKY_MO_JIRA_TOKEN"

// tokenFileMode is the expected permission mode for the token file; mirrors
// the sync.key convention (0600).
const tokenFileMode = 0600

// DefaultTokenPath returns the location of the Jira API token file, mirroring
// the ~/.config/unky-mo/sync.key pattern.
func DefaultTokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo", "jira.token")
}

// LoadToken returns the Jira API token. Env var wins if set; otherwise the
// token file at DefaultTokenPath is read. Returns ("", nil) when neither is
// available so callers can render the "not configured" state without
// treating it as a hard error. Loose file permissions (group/other readable)
// surface as an error so users notice.
func LoadToken() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvToken)); v != "" {
		return v, nil
	}
	path := DefaultTokenPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat jira token file: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("jira token file %s has loose permissions (%o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read jira token file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// HasToken reports whether a token is currently available (env var or file).
// Used by NeedsConfig; does not surface permission errors to callers.
func HasToken() bool {
	if strings.TrimSpace(os.Getenv(EnvToken)) != "" {
		return true
	}
	info, err := os.Stat(DefaultTokenPath())
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// Instance is a flat representation of one Jira config block. The config
// package owns the TOML shape; this is just the minimal data we need to
// build a Provider, without introducing a config → jira import.
type Instance struct {
	Name          string
	BaseURL       string
	Email         string
	SprintFieldID string
	StatusMap     tickets.StatusMap
	// ProjectMap maps Jira project keys (e.g. "OP") to Mo project names.
	// Hand-authored in config.toml; UI-saved overrides live in a separate
	// companion file and are merged in at the TUI layer.
	ProjectMap map[string]string
}

// BuildProviders constructs a Provider per configured instance, reading the
// API token via LoadToken. Instances missing required fields are skipped
// silently — the UI shows the "not configured" state via NeedsConfig.
// Token-load errors (e.g. loose permissions) also yield no providers so the
// panel renders its onboarding state rather than surfacing a cryptic error.
func BuildProviders(instances []Instance) []tickets.Provider {
	token, err := LoadToken()
	if err != nil || token == "" {
		return nil
	}
	var out []tickets.Provider
	for _, inst := range instances {
		sm := inst.StatusMap
		if isEmptyStatusMap(sm) {
			sm = tickets.DefaultStatusMap()
		}
		p, err := New(Config{
			BaseURL:       inst.BaseURL,
			Email:         inst.Email,
			Token:         token,
			Name:          inst.Name,
			SprintFieldID: inst.SprintFieldID,
			StatusMap:     sm,
		})
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// NeedsConfig returns true when no instance can be built — either no token
// is available or no instance has the required URL + email fields. Drives
// the first-run onboarding panel.
func NeedsConfig(instances []Instance) bool {
	if !HasToken() {
		return true
	}
	for _, inst := range instances {
		if inst.BaseURL != "" && inst.Email != "" {
			return false
		}
	}
	return true
}

// WriteToken persists a token to DefaultTokenPath with mode 0600, creating
// the parent directory if needed. Refuses to overwrite an existing file
// unless force is true.
func WriteToken(token string, force bool) (string, error) {
	path := DefaultTokenPath()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("token file already exists at %s; pass --force to overwrite", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)+"\n"), tokenFileMode); err != nil {
		return "", fmt.Errorf("write jira token file: %w", err)
	}
	return path, nil
}

func isEmptyStatusMap(sm tickets.StatusMap) bool {
	return len(sm.InProgress) == 0 && len(sm.Blocked) == 0 &&
		len(sm.Review) == 0 && len(sm.Todo) == 0
}

// VerifyCreds hits /rest/api/2/myself with the given credentials and returns
// the user's display name on success. Catches the three common setup
// misconfigurations (wrong URL, wrong email, wrong token) at once before
// anything lands on disk. Pure HTTP — safe to call from any CLI or TUI
// dialog.
func VerifyCreds(ctx context.Context, baseURL, email, token string) (string, error) {
	u := strings.TrimRight(baseURL, "/") + "/rest/api/2/myself"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(email+":"+token)))

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("HTTP %d — email/token rejected", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("HTTP 404 — is %s the right URL?", baseURL)
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("HTTP %d — %s", resp.StatusCode, extractJiraError(body))
	}
	var me struct {
		DisplayName string `json:"displayName"`
		EmailAddr   string `json:"emailAddress"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if me.DisplayName == "" {
		return me.EmailAddr, nil
	}
	return me.DisplayName, nil
}
