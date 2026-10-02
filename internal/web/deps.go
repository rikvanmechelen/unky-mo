// Package web serves a read-only HTTP/JSON dashboard over the data mo
// already tracks (session status, worktrees, PRs, tickets, usage), plus a
// chat-style live transcript of a session (tailed from its JSONL file) with
// a prompt box that injects text into the live session via tmux. It never
// touches the hook socket or status.Manager — those are owned exclusively by
// the running main TUI process — so session status (used to gate prompt
// submission on the session being idle) comes from the shared state file.
package web

import (
	"context"

	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tmux"
)

//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_web github.com/rvanmech/unky-mo/internal/web StateReader,ProjectLister,WorktreeReader,PRClient,TicketSource,PromptSender

// StateReader reads the shared state file written by the main TUI.
type StateReader interface {
	Read() (*state.StateFile, error)
}

// ProjectLister returns the configured projects.
type ProjectLister interface {
	LoadProjects() ([]project.Project, error)
}

// WorktreeReader reads git worktree/branch info for a project.
type WorktreeReader interface {
	ListBranches(projectPath string) ([]project.Branch, error)
}

// PRClient reads GitHub pull request info for a project.
type PRClient interface {
	ListPRs(projectPath string) ([]github.PullRequest, error)
	GetPRDetail(projectPath string, number int) (*github.PRDetail, error)
}

// TicketSource reads ticket info across all configured providers.
type TicketSource interface {
	MyTickets(ctx context.Context) ([]tickets.Ticket, []tickets.FetchResult)
	Detail(ctx context.Context, providerName, id string) (*tickets.TicketDetail, error)
}

// PromptSender injects a prompt into a live session's tmux pane.
type PromptSender interface {
	SendLiteralText(target, text string) error
}

// Deps bundles the data sources a Server reads from.
type Deps struct {
	State     StateReader
	Projects  ProjectLister
	Worktrees WorktreeReader
	PRs       PRClient
	Tickets   TicketSource
	Prompts   PromptSender
}

// realStateReader wraps state.Read for production use.
type realStateReader struct{ path string }

func NewStateReader(path string) StateReader { return realStateReader{path: path} }

func (r realStateReader) Read() (*state.StateFile, error) { return state.Read(r.path) }

// realProjectLister wraps config.Config.LoadProjects for production use.
type realProjectLister struct {
	load func() ([]project.Project, error)
}

// NewProjectLister wraps the given load func (typically cfg.LoadProjects) as
// a ProjectLister.
func NewProjectLister(load func() ([]project.Project, error)) ProjectLister {
	return realProjectLister{load: load}
}

func (r realProjectLister) LoadProjects() ([]project.Project, error) { return r.load() }

// realWorktreeReader wraps internal/project's package-level functions.
type realWorktreeReader struct{}

func NewWorktreeReader() WorktreeReader { return realWorktreeReader{} }

func (realWorktreeReader) ListBranches(projectPath string) ([]project.Branch, error) {
	return project.ListBranches(projectPath)
}

// realPRClient wraps a github.Client for production use.
type realPRClient struct{ client *github.Client }

func NewPRClient(client *github.Client) PRClient { return realPRClient{client: client} }

func (r realPRClient) ListPRs(projectPath string) ([]github.PullRequest, error) {
	return r.client.ListPRs(projectPath)
}

func (r realPRClient) GetPRDetail(projectPath string, number int) (*github.PRDetail, error) {
	return r.client.GetPRDetail(projectPath, number)
}

// realTicketSource wraps a fixed set of ticket providers.
type realTicketSource struct{ providers []tickets.Provider }

func NewTicketSource(providers []tickets.Provider) TicketSource {
	return realTicketSource{providers: providers}
}

func (r realTicketSource) MyTickets(ctx context.Context) ([]tickets.Ticket, []tickets.FetchResult) {
	return tickets.FetchAll(ctx, r.providers)
}

func (r realTicketSource) Detail(ctx context.Context, providerName, id string) (*tickets.TicketDetail, error) {
	for _, p := range r.providers {
		if p.Name() == providerName {
			return p.Detail(ctx, id)
		}
	}
	return nil, errUnknownProvider(providerName)
}

// realPromptSender wraps a *tmux.Client for production use.
type realPromptSender struct{ client *tmux.Client }

func NewPromptSender(client *tmux.Client) PromptSender { return realPromptSender{client: client} }

func (r realPromptSender) SendLiteralText(target, text string) error {
	return r.client.SendLiteralText(target, text)
}

type errUnknownProvider string

func (e errUnknownProvider) Error() string { return "unknown ticket provider: " + string(e) }
