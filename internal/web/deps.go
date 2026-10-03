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
	"os"
	"path/filepath"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tmux"
)

//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_web github.com/rvanmech/unky-mo/internal/web StateReader,ProjectLister,WorktreeReader,PRClient,TicketSource,PromptSender,SessionHistory,SessionOps

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

// SessionHistory reads a checkout's past Claude sessions from its JSONL
// transcripts under ~/.claude/projects.
type SessionHistory interface {
	RecentSessions(path string, n int) []claude.RecentSession
	TranscriptExists(path, sessionID string) bool
}

// SessionOps is the subset of internal/ops the web dashboard drives to
// start, resume, replace and stop sessions — the same operations the TUI
// calls, so the web is a full stand-in for it.
type SessionOps interface {
	LiveSessions() ([]claude.Session, error)
	WindowExists(name string) bool
	SwitchToWindow(target string) error
	Launch(p ops.LaunchParams) (*ops.LaunchResult, error)
	LaunchSibling(p ops.SiblingParams) (*ops.LaunchResult, error)
	ParkAndLaunch(p ops.ParkParams) (*ops.LaunchResult, error)
	CreateWorktreeAndLaunch(p ops.WorktreeParams) (*ops.WorktreeResult, error)
	StopSessions(sessions []claude.Session) int
	CleanupWorktree(p ops.CleanupParams) (*ops.CleanupResult, error)
	LiftSessionToWorktree(p ops.LiftParams) (*ops.LiftResult, error)
	IsDirty(path string) (bool, error)
}

// Deps bundles the data sources a Server reads from.
type Deps struct {
	State     StateReader
	Projects  ProjectLister
	Worktrees WorktreeReader
	PRs       PRClient
	Tickets   TicketSource
	Prompts   PromptSender
	History   SessionHistory
	Sessions  SessionOps
	// Agents is the configured [[agent]] list. Launches only ever run a
	// command from here — the browser picks an agent by key, never sends a
	// command itself.
	Agents []config.AgentConfig
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

// realSessionHistory wraps internal/claude's transcript readers.
type realSessionHistory struct{}

func NewSessionHistory() SessionHistory { return realSessionHistory{} }

func (realSessionHistory) RecentSessions(path string, n int) []claude.RecentSession {
	return claude.RecentSessions(path, n)
}

func (realSessionHistory) TranscriptExists(path, sessionID string) bool {
	_, err := os.Stat(filepath.Join(claude.ProjectsDirForPath(path), sessionID+".jsonl"))
	return err == nil
}

// realSessionOps adapts an *ops.Context to SessionOps.
type realSessionOps struct{ ctx *ops.Context }

func NewSessionOps(ctx *ops.Context) SessionOps { return realSessionOps{ctx: ctx} }

func (r realSessionOps) LiveSessions() ([]claude.Session, error) { return r.ctx.Claude.LiveSessions() }
func (r realSessionOps) WindowExists(name string) bool           { return r.ctx.Tmux.WindowExists(name) }
func (r realSessionOps) SwitchToWindow(target string) error      { return r.ctx.Tmux.SwitchToWindow(target) }

func (r realSessionOps) Launch(p ops.LaunchParams) (*ops.LaunchResult, error) {
	return ops.LaunchSession(r.ctx, p)
}

func (r realSessionOps) LaunchSibling(p ops.SiblingParams) (*ops.LaunchResult, error) {
	return ops.LaunchSibling(r.ctx, p)
}

func (r realSessionOps) ParkAndLaunch(p ops.ParkParams) (*ops.LaunchResult, error) {
	return ops.ParkAndLaunch(r.ctx, p)
}

func (r realSessionOps) CreateWorktreeAndLaunch(p ops.WorktreeParams) (*ops.WorktreeResult, error) {
	return ops.CreateWorktreeAndLaunch(r.ctx, p)
}

func (r realSessionOps) StopSessions(sessions []claude.Session) int {
	return ops.StopSessions(r.ctx, sessions)
}

func (r realSessionOps) CleanupWorktree(p ops.CleanupParams) (*ops.CleanupResult, error) {
	return ops.CleanupWorktree(r.ctx, p)
}

func (r realSessionOps) LiftSessionToWorktree(p ops.LiftParams) (*ops.LiftResult, error) {
	return ops.LiftSessionToWorktree(r.ctx, p)
}

func (realSessionOps) IsDirty(path string) (bool, error) { return project.IsDirty(path) }

type errUnknownProvider string

func (e errUnknownProvider) Error() string { return "unknown ticket provider: " + string(e) }
