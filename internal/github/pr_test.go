package github

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

const prListJSON = `[
  {"number":42,"title":"fix auth flow","author":{"login":"rik"},"headRefName":"fix-auth","state":"OPEN","createdAt":"2026-04-01T10:00:00Z","url":"https://github.com/foo/bar/pull/42"},
  {"number":41,"title":"add QR scanner","author":{"login":"alice"},"headRefName":"qr","state":"OPEN","createdAt":"2026-03-28T09:00:00Z","url":"https://github.com/foo/bar/pull/41"}
]`

const prDetailJSON = `{
  "number":42,
  "title":"fix auth flow",
  "body":"Fixes the aud-claim bug.",
  "author":{"login":"rik"},
  "headRefName":"fix-auth",
  "baseRefName":"main",
  "state":"OPEN",
  "createdAt":"2026-04-01T10:00:00Z",
  "url":"https://github.com/foo/bar/pull/42",
  "additions":42,
  "deletions":8,
  "reviewDecision":"APPROVED"
}`

func TestListPRs(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "/tmp/proj", "gh", "pr", "list",
			"--json", "number,title,author,headRefName,state,createdAt,url",
			"--limit", "20",
			"--state", "open",
		).
		Return([]byte(prListJSON), nil, nil)

	c := NewClient(cmd)
	prs, err := c.ListPRs("/tmp/proj")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("want 2 PRs, got %d", len(prs))
	}
	if prs[0].Number != 42 || prs[0].Title != "fix auth flow" || prs[0].Author.Login != "rik" {
		t.Errorf("first PR wrong: %+v", prs[0])
	}
	if prs[1].Branch != "qr" {
		t.Errorf("second branch: %q", prs[1].Branch)
	}
}

func TestListPRsAuthFailureRecognized(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "/tmp/proj", "gh", gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, []byte("gh auth login required"), errors.New("exit 1"))

	c := NewClient(cmd)
	_, err := c.ListPRs("/tmp/proj")
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("want auth error, got %v", err)
	}
}

func TestListPRsGenericError(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, []byte("some other error"), errors.New("exit 2"))

	c := NewClient(cmd)
	_, err := c.ListPRs("/tmp/proj")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("should NOT mention auth for non-auth stderr; got %v", err)
	}
	if !strings.Contains(err.Error(), "some other error") {
		t.Errorf("should surface stderr; got %v", err)
	}
}

func TestListPRsBadJSONIsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]byte("not json"), nil, nil)

	c := NewClient(cmd)
	if _, err := c.ListPRs("/tmp/proj"); err == nil {
		t.Error("malformed JSON should error")
	}
}

func TestGetPRDetail(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "/tmp/proj", "gh", "pr", "view", "42", "--json",
			"number,title,body,author,headRefName,baseRefName,state,createdAt,url,additions,deletions,reviewDecision").
		Return([]byte(prDetailJSON), nil, nil)

	c := NewClient(cmd)
	d, err := c.GetPRDetail("/tmp/proj", 42)
	if err != nil {
		t.Fatalf("GetPRDetail: %v", err)
	}
	if d.Number != 42 || d.Additions != 42 || d.ReviewDecision != "APPROVED" {
		t.Errorf("detail fields wrong: %+v", d)
	}
	if d.Body == "" {
		t.Error("body should round-trip")
	}
}

func TestCheckoutPRBranchFailureSurfacesStderr(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		CombinedOutput(gomock.Any(), "/tmp/proj", "gh", "pr", "checkout", "42").
		Return([]byte("branch already exists locally"), errors.New("exit 1"))

	c := NewClient(cmd)
	err := c.CheckoutPRBranch("/tmp/proj", 42)
	if err == nil || !strings.Contains(err.Error(), "branch already exists") {
		t.Errorf("want surfaced stderr, got %v", err)
	}
}

func TestCheckoutPRBranchSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		CombinedOutput(gomock.Any(), "/tmp/proj", "gh", "pr", "checkout", "42").
		Return(nil, nil)

	c := NewClient(cmd)
	if err := c.CheckoutPRBranch("/tmp/proj", 42); err != nil {
		t.Errorf("success path errored: %v", err)
	}
}

func TestOpenPRInBrowserPassesWebFlag(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Run(gomock.Any(), "/tmp/proj", "gh", "pr", "view", "42", "--web").
		Return(nil)

	c := NewClient(cmd)
	if err := c.OpenPRInBrowser("/tmp/proj", 42); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIsGHAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	// Available: Run returns nil.
	cmd.EXPECT().Run(gomock.Any(), "/tmp/proj", "gh", "auth", "status").Return(nil)
	if !NewClient(cmd).IsGHAvailable("/tmp/proj") {
		t.Error("should report available")
	}
	// Not available: Run returns an error.
	cmd.EXPECT().Run(gomock.Any(), "/tmp/proj", "gh", "auth", "status").Return(errors.New("not found"))
	if NewClient(cmd).IsGHAvailable("/tmp/proj") {
		t.Error("should report NOT available")
	}
}

// Sanity: make sure the default client wiring is in place so the package-level
// API works (doesn't panic) even without injecting. We don't actually invoke
// `gh` here — just verify the wiring.
func TestDefaultClientWired(t *testing.T) {
	if defaultClient == nil {
		t.Fatal("defaultClient must be non-nil so package-level funcs don't panic")
	}
	if defaultClient.cmd == nil {
		t.Error("defaultClient.cmd must be non-nil")
	}
	// Silence the unused-import warning on `exec` when none of the other tests
	// happen to exercise it directly.
	_ = (*exec.Cmd)(nil)
	_ = context.Background()
}
