package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Author      ghAuthor  `json:"author"`
	Branch      string    `json:"headRefName"`
	BaseBranch  string    `json:"baseRefName"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"createdAt"`
	URL         string    `json:"url"`
	Additions   int       `json:"additions"`
	Deletions   int       `json:"deletions"`
	ReviewDecision string `json:"reviewDecision"`
}

// ListPRs fetches open pull requests for the repo at projectPath.
func ListPRs(projectPath string) ([]PullRequest, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--json", "number,title,author,headRefName,state,createdAt,url",
		"--limit", "20",
		"--state", "open",
	)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(stderr, "auth") || strings.Contains(stderr, "login") {
				return nil, fmt.Errorf("gh not authenticated — run 'gh auth login'")
			}
			if stderr != "" {
				return nil, fmt.Errorf("gh: %s", stderr)
			}
		}
		return nil, err
	}

	var prs []PullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// GetPRDetail fetches detailed info for a single PR.
func GetPRDetail(projectPath string, number int) (*PRDetail, error) {
	cmd := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--json", "number,title,body,author,headRefName,baseRefName,state,createdAt,url,additions,deletions,reviewDecision",
	)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var detail PRDetail
	if err := json.Unmarshal(out, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// OpenPRInBrowser opens a PR in the default browser.
func OpenPRInBrowser(projectPath string, number int) error {
	cmd := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", number), "--web")
	cmd.Dir = projectPath
	return cmd.Run()
}

// CheckoutPRBranch checks out a PR's branch in the given directory.
func CheckoutPRBranch(projectPath string, number int) error {
	cmd := exec.Command("gh", "pr", "checkout", fmt.Sprintf("%d", number))
	cmd.Dir = projectPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// IsGHAvailable checks if the gh CLI is installed and authenticated.
func IsGHAvailable(projectPath string) bool {
	cmd := exec.Command("gh", "auth", "status")
	cmd.Dir = projectPath
	return cmd.Run() == nil
}
