package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// PullRequest represents an open PR from GitHub.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    ghAuthor  `json:"author"`
	Branch    string    `json:"headRefName"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
}

type ghAuthor struct {
	Login string `json:"login"`
}

// PRDetail has extended info for a single PR.
type PRDetail struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	Author         ghAuthor  `json:"author"`
	Branch         string    `json:"headRefName"`
	BaseBranch     string    `json:"baseRefName"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"createdAt"`
	URL            string    `json:"url"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ReviewDecision string    `json:"reviewDecision"`
}

// Client talks to the `gh` CLI via an injectable Commander so tests can
// substitute a mock.
type Client struct {
	cmd moexec.Commander
}

// NewClient returns a Client backed by the given Commander. Pass
// [moexec.DefaultCommander] (or nil) in production.
func NewClient(cmd moexec.Commander) *Client {
	if cmd == nil {
		cmd = moexec.DefaultCommander
	}
	return &Client{cmd: cmd}
}

// defaultClient wraps the package-level API so existing callers keep working
// without threading a Commander through every call site.
var defaultClient = NewClient(moexec.DefaultCommander)

// ListPRs fetches open pull requests for the repo at projectPath.
func ListPRs(projectPath string) ([]PullRequest, error) {
	return defaultClient.ListPRs(projectPath)
}

// GetPRDetail fetches detailed info for a single PR.
func GetPRDetail(projectPath string, number int) (*PRDetail, error) {
	return defaultClient.GetPRDetail(projectPath, number)
}

// OpenPRInBrowser opens a PR in the default browser.
func OpenPRInBrowser(projectPath string, number int) error {
	return defaultClient.OpenPRInBrowser(projectPath, number)
}

// CheckoutPRBranch checks out a PR's branch in the given directory.
func CheckoutPRBranch(projectPath string, number int) error {
	return defaultClient.CheckoutPRBranch(projectPath, number)
}

// IsGHAvailable checks if the gh CLI is installed and authenticated.
func IsGHAvailable(projectPath string) bool {
	return defaultClient.IsGHAvailable(projectPath)
}

func (c *Client) ListPRs(projectPath string) ([]PullRequest, error) {
	stdout, stderr, err := c.cmd.Output(context.Background(), projectPath,
		"gh", "pr", "list",
		"--json", "number,title,author,headRefName,state,createdAt,url",
		"--limit", "20",
		"--state", "open",
	)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if bytes.Contains(stderr, []byte("auth")) || bytes.Contains(stderr, []byte("login")) {
			return nil, fmt.Errorf("gh not authenticated — run 'gh auth login'")
		}
		if msg != "" {
			return nil, fmt.Errorf("gh: %s", msg)
		}
		return nil, err
	}

	var prs []PullRequest
	if err := json.Unmarshal(stdout, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func (c *Client) GetPRDetail(projectPath string, number int) (*PRDetail, error) {
	stdout, _, err := c.cmd.Output(context.Background(), projectPath,
		"gh", "pr", "view", fmt.Sprintf("%d", number),
		"--json", "number,title,body,author,headRefName,baseRefName,state,createdAt,url,additions,deletions,reviewDecision",
	)
	if err != nil {
		return nil, err
	}
	var detail PRDetail
	if err := json.Unmarshal(stdout, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (c *Client) OpenPRInBrowser(projectPath string, number int) error {
	return c.cmd.Run(context.Background(), projectPath,
		"gh", "pr", "view", fmt.Sprintf("%d", number), "--web")
}

func (c *Client) CheckoutPRBranch(projectPath string, number int) error {
	out, err := c.cmd.CombinedOutput(context.Background(), projectPath,
		"gh", "pr", "checkout", fmt.Sprintf("%d", number))
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Client) IsGHAvailable(projectPath string) bool {
	return c.cmd.Run(context.Background(), projectPath, "gh", "auth", "status") == nil
}
