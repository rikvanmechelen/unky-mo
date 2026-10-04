// Package web serves a read-only HTTP/JSON dashboard over the data mo
// already tracks (session status, worktrees, PRs, tickets, usage), plus a
// chat-style live transcript of a session (tailed from its JSONL file) with
// a prompt box that injects text into the live session via tmux. It never
// touches the hook socket or status.Manager — those are owned exclusively by
// the running main TUI process (apart from the write-only restart message,
// see Restarter) — so session status (used to gate prompt
// submission on the session being idle) comes from the shared state file.
package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/review"
	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
)

//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_web github.com/rvanmech/unky-mo/internal/web StateReader,ProjectLister,WorktreeReader,PRClient,TicketSource,PromptSender,SessionHistory,SessionOps,GitFiles,ChangeAnalyzer,ScopeChecker,Terminals,Shells,ClaudePane,Subagents,SlashCommands,Restarter

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

// PromptSender injects a prompt into a live session's tmux pane: one line
// typed literally, multi-line text as a single bracketed paste, or a prompt
// with image attachments (each image path pasted on its own, then the text).
type PromptSender interface {
	SendLiteralText(target, text string) error
	SendPastedText(target, text string) error
	SendPrompt(target, text string, imagePaths []string) error
}

// SessionHistory reads a checkout's past Claude sessions from its JSONL
// transcripts under ~/.claude/projects.
type SessionHistory interface {
	RecentSessions(path string, n int) []claude.RecentSession
	TranscriptExists(path, sessionID string) bool
	// ContextTokens is the session's current context footprint — the same
	// number the TUI sidebar shows next to its 5h bar (usage.SessionTokens).
	ContextTokens(path, sessionID string) int
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

// GitFiles reads a checkout's changes, file list and branch for the chat
// view's Files panel and the session nav. dir is always a live session's
// cwd from the state file, never a browser-supplied path.
type GitFiles interface {
	Changes(dir string) (*gitfiles.Changes, error)
	Tree(dir string) (root string, paths []string, err error)
	Branch(dir string) string
	// ReadFile and ReadHEAD read one file's working-tree and committed
	// versions for the editor tabs. root comes from Tree/Changes and path
	// is one they listed; both re-check that path stays inside root.
	ReadFile(root, path string) (*gitfiles.Content, error)
	ReadHEAD(root, path string) (*gitfiles.Content, error)
	// WriteFile saves an editor tab, only if the file still hashes to
	// baseHash (a *gitfiles.ConflictError otherwise). Same root/path rules.
	WriteFile(root, path, text, baseHash string) (*gitfiles.Content, error)
	// Log reads the commit graph for the Graph tab (scope is
	// gitfiles.ScopeBranch or ScopeAll).
	Log(dir, scope string) (*gitfiles.Log, error)
	// Commit and CommitFile read one commit and one file it changed, from
	// git's object store. hash must be a full commit id
	// (gitfiles.ErrUnknownCommit otherwise) and path one of the commit's
	// changed files (gitfiles.ErrNotInCommit otherwise).
	Commit(root, hash string) (*gitfiles.CommitDetail, error)
	CommitFile(root, hash, path string) (*gitfiles.CommitFileDiff, error)
	// Overview reads the change for the Overview tab (mode is
	// gitfiles.ModeBranch or ModeHead).
	Overview(dir, mode string) (*gitfiles.Overview, error)
	// ReadAt reads one file as committed in rev, a full commit id
	// (gitfiles.ErrUnknownCommit otherwise): the base side of a branch diff.
	ReadAt(root, rev, path string) (*gitfiles.Content, error)
	// ResolveBranch, ResolvePR, OverviewAt and TreeAt serve the reviewer
	// view for a branch that isn't checked out: its head commit (origin's
	// copy, fetched first, when remote), a pull request's head (fetched
	// from origin's refs/pull/<n>/head), its change from the merge base
	// with base (default branch when empty) to that commit, and its files.
	ResolveBranch(root, branch string, remote bool) (string, error)
	ResolvePR(root string, n int, base string) (string, error)
	OverviewAt(root, branch, head, base string) (*gitfiles.Overview, error)
	TreeAt(root, head string) ([]string, error)
}

// ChangeAnalyzer works out a change's architecture delta (package imports
// that appear or disappear, checked against the repo's layer rules) and
// contract surface, for the Overview tab. o is an overview from GitFiles.
type ChangeAnalyzer interface {
	Analyze(o *gitfiles.Overview) (*review.Analysis, error)
}

// ScopeChecker asks Claude (headless, no tools) whether each changed file
// fits the ticket and the prompts that changed it — the Overview tab's
// on-demand drift check.
type ScopeChecker interface {
	Check(req review.ScopeRequest) (*review.ScopeResult, error)
}

// Terminals reads and drives a window's drawer terminals for the chat
// view's terminal drawer. Handlers only ever pass pane IDs that List
// returned for the requested window.
type Terminals interface {
	List(w state.ProjectState) ([]ops.Terminal, error)
	Capture(paneID string) (string, error)
	SendLine(paneID, text string) error
	Interrupt(paneID string) error
	New(w state.ProjectState) (string, error)
	Close(paneID string) error
}

// Shells lists Claude's running Bash-tool shells for a session and reads
// their output files (the sidebar's "Shells" section). Handlers only Tail
// an OutputFile that List returned for the requested session.
type Shells interface {
	List(sessionID string) ([]claude.ActiveShell, error)
	Tail(path string, maxBytes int) (text string, truncated bool, err error)
}

// ClaudePane reads the visible screen of a session's Claude pane, for the
// chat view's copy of Claude Code's spinner line and permission mode, and
// presses shift+tab there to cycle the mode. target is always built from
// the state file, never from the request.
type ClaudePane interface {
	Capture(target string) (string, error)
	CycleMode(target string) error
}

// Subagents lists the agents a session spawned with the Agent tool (see
// claude.SubagentReader). path and sessionID always come from the state
// file's row for the requested window.
type Subagents interface {
	List(path, sessionID string) ([]claude.Subagent, error)
}

// SlashCommands lists what "/" completes to in a session's prompt box (see
// claude.SlashCommands). path and sessionID always come from the state
// file's row for the requested window.
type SlashCommands interface {
	List(path, sessionID string) []claude.SlashCommand
}

type realSlashCommands struct{}

func NewSlashCommands() SlashCommands { return realSlashCommands{} }

func (realSlashCommands) List(path, sessionID string) []claude.SlashCommand {
	return claude.SlashCommands(path, sessionID)
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
	Git       GitFiles
	Review    ChangeAnalyzer
	Scope     ScopeChecker
	Terminals Terminals
	Shells    Shells
	// ClaudePane reads Claude's own pane (spinner line, permission mode)
	// and cycles its permission mode.
	ClaudePane ClaudePane
	Subagents  Subagents
	// Commands feeds the composer's "/" completion.
	Commands  SlashCommands
	Restarter Restarter
	// Attachments holds images uploaded from the composer until the prompt
	// that references them is sent.
	Attachments *AttachmentStore
	// Agents is the configured [[agent]] list. Launches only ever run a
	// command from here — the browser picks an agent by key, never sends a
	// command itself.
	Agents []config.AgentConfig
}

// Restarter asks the running TUI to restart itself, every sidebar and this
// web server (what ctrl+alt+r does). It's the one thing the web sends to the
// hook socket: a write-only control message, never a status read.
type Restarter interface {
	Restart() error
}

type socketRestarter struct{ socketPath string }

// NewRestarter sends notify.NotifyRestart to the TUI's socket.
func NewRestarter(socketPath string) Restarter { return socketRestarter{socketPath: socketPath} }

func (r socketRestarter) Restart() error { return notify.SendRestart(r.socketPath) }

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

func (r realPromptSender) SendPastedText(target, text string) error {
	return r.client.SendPastedText(target, text)
}

func (r realPromptSender) SendPrompt(target, text string, imagePaths []string) error {
	return r.client.SendPrompt(target, text, imagePaths)
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

func (realSessionHistory) ContextTokens(path, sessionID string) int {
	return usage.SessionTokens(filepath.Join(claude.ProjectsDirForPath(path), sessionID+".jsonl"))
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

// realAnalyzer runs review.Analyze against the real git binary.
type realAnalyzer struct{ cmd moexec.Commander }

func NewChangeAnalyzer(cmd moexec.Commander) ChangeAnalyzer { return realAnalyzer{cmd: cmd} }

func (a realAnalyzer) Analyze(o *gitfiles.Overview) (*review.Analysis, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return review.Analyze(ctx, a.cmd, o)
}

// realScopeChecker runs review.CheckScope with the real claude binary.
type realScopeChecker struct{ cmd moexec.Commander }

func NewScopeChecker(cmd moexec.Commander) ScopeChecker { return realScopeChecker{cmd: cmd} }

// scopeTimeout bounds one scope check; claude usually answers in well
// under a minute.
const scopeTimeout = 3 * time.Minute

func (c realScopeChecker) Check(req review.ScopeRequest) (*review.ScopeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scopeTimeout)
	defer cancel()
	return review.CheckScope(ctx, c.cmd, req)
}

// realGitFiles runs gitfiles against the real git binary.
type realGitFiles struct{ cmd moexec.Commander }

func NewGitFiles(cmd moexec.Commander) GitFiles { return realGitFiles{cmd: cmd} }

// gitTimeout bounds one panel refresh so a wedged git (e.g. a lock held by
// another process) can't pile up requests.
const gitTimeout = 10 * time.Second

func (g realGitFiles) Changes(dir string) (*gitfiles.Changes, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetChanges(ctx, g.cmd, dir)
}

func (g realGitFiles) Tree(dir string) (string, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.Tree(ctx, g.cmd, dir)
}

func (g realGitFiles) ReadFile(root, path string) (*gitfiles.Content, error) {
	return gitfiles.ReadFile(root, path)
}

func (g realGitFiles) WriteFile(root, path, text, baseHash string) (*gitfiles.Content, error) {
	return gitfiles.WriteFile(root, path, text, baseHash)
}

func (g realGitFiles) ReadHEAD(root, path string) (*gitfiles.Content, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.ReadHEAD(ctx, g.cmd, root, path)
}

func (g realGitFiles) Log(dir, scope string) (*gitfiles.Log, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetLog(ctx, g.cmd, dir, scope)
}

func (g realGitFiles) Commit(root, hash string) (*gitfiles.CommitDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetCommit(ctx, g.cmd, root, hash)
}

func (g realGitFiles) CommitFile(root, hash, path string) (*gitfiles.CommitFileDiff, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetCommitFile(ctx, g.cmd, root, hash, path)
}

func (g realGitFiles) Overview(dir, mode string) (*gitfiles.Overview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetOverview(ctx, g.cmd, dir, mode)
}

func (g realGitFiles) ReadAt(root, rev, path string) (*gitfiles.Content, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.ReadAt(ctx, g.cmd, root, rev, path)
}

// fetchTimeout bounds the git fetch of a PR branch.
const fetchTimeout = time.Minute

func (g realGitFiles) ResolveBranch(root, branch string, remote bool) (string, error) {
	timeout := gitTimeout
	if remote {
		timeout = fetchTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return gitfiles.ResolveBranch(ctx, g.cmd, root, branch, remote)
}

func (g realGitFiles) ResolvePR(root string, n int, base string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	return gitfiles.ResolvePR(ctx, g.cmd, root, n, base)
}

func (g realGitFiles) OverviewAt(root, branch, head, base string) (*gitfiles.Overview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetOverviewAt(ctx, g.cmd, root, branch, head, base)
}

func (g realGitFiles) TreeAt(root, head string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.TreeAt(ctx, g.cmd, root, head)
}

func (g realGitFiles) Branch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.CurrentBranch(ctx, g.cmd, dir)
}

// realTerminals finds and drives drawer terminals through tmux.
type realTerminals struct{ client *tmux.Client }

func NewTerminals(client *tmux.Client) Terminals { return realTerminals{client: client} }

// terminalScrollback is how many lines of history the output view shows.
const terminalScrollback = 200

// List returns the window's terminals: the one shown in its drawer (named
// by the sidebar's @mo_drawer_pane option, if it's really in the window)
// first, then every pane parked in its mo-terms session.
func (r realTerminals) List(w state.ProjectState) ([]ops.Terminal, error) {
	var out []ops.Terminal
	seen := map[string]bool{}
	if id := r.client.WindowOption(w.WindowID, tmux.DrawerPaneOption); id != "" {
		if panes, err := r.client.ListWindowPanes(w.WindowID); err == nil {
			for _, p := range panes {
				if p.ID != id {
					continue
				}
				if d, err := r.client.PaneDetails(id); err == nil {
					out = append(out, ops.Terminal{ID: d.ID, Command: d.Command, Cwd: d.Cwd, Visible: true})
					seen[id] = true
				}
			}
		}
	}
	session := ops.TermSessionName(w.InstanceID, w.WindowID, w.WindowName)
	if r.client.SessionExistsNamed(session) {
		panes, err := r.client.ListSessionPanes(session)
		if err != nil {
			return nil, err
		}
		for _, p := range panes {
			if !seen[p.ID] {
				out = append(out, ops.Terminal{ID: p.ID, Command: p.Command, Cwd: p.Cwd})
				seen[p.ID] = true
			}
		}
	}
	return out, nil
}

func (r realTerminals) Capture(paneID string) (string, error) {
	return r.client.CapturePane(paneID, terminalScrollback)
}

// SendLine types text literally (no tmux key-name interpretation) and
// presses Enter; an empty line just presses Enter.
func (r realTerminals) SendLine(paneID, text string) error {
	if text == "" {
		return r.client.SendRawKeys(paneID, "Enter")
	}
	return r.client.SendLiteralText(paneID, text)
}

// Close kills the terminal's pane (the sidebar's `x`). The sidebar drops
// it on its next refresh.
func (r realTerminals) Close(paneID string) error {
	return r.client.KillPane(paneID)
}

func (r realTerminals) Interrupt(paneID string) error {
	return r.client.SendRawKeys(paneID, "C-c")
}

// New opens a terminal parked in the window's mo-terms session — never in
// the visible window, so the user's tmux layout doesn't change. The
// sidebar adopts it on its next refresh. Creating the session yields its
// first shell, which is the new terminal.
func (r realTerminals) New(w state.ProjectState) (string, error) {
	session := ops.TermSessionName(w.InstanceID, w.WindowID, w.WindowName)
	ghost, err := ops.EnsureTermSession(r.client, session, w.Path)
	if err != nil {
		return "", err
	}
	if ghost != "" {
		return ghost, nil
	}
	return r.client.NewWindowInSession(session, w.Path)
}

// realClaudePane captures Claude's pane through tmux.
type realClaudePane struct{ client *tmux.Client }

func NewClaudePane(client *tmux.Client) ClaudePane { return realClaudePane{client: client} }

// Capture returns just the visible screen (no scrollback): the spinner is
// always drawn there, right above the prompt box.
func (r realClaudePane) Capture(target string) (string, error) {
	return r.client.CapturePane(target, 0)
}

// CycleMode presses shift+tab, Claude Code's permission-mode cycle key.
func (r realClaudePane) CycleMode(target string) error {
	return r.client.SendRawKeys(target, "BTab")
}

// realShells finds shells via ps/lsof (claude.ActiveShells) for the live
// Claude process running sessionID.
type realShells struct{}

func NewShells() Shells { return realShells{} }

func (realShells) List(sessionID string) ([]claude.ActiveShell, error) {
	sessions, err := claude.LiveSessions()
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return claude.ActiveShells(s.PID), nil
		}
	}
	return nil, nil
}

// Tail returns up to maxBytes from the end of path, starting at a line
// boundary when it had to cut.
func (realShells) Tail(path string, maxBytes int) (string, bool, error) {
	return tailFile(path, maxBytes)
}

func tailFile(path string, maxBytes int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	size := info.Size()
	if size <= int64(maxBytes) {
		data, err := io.ReadAll(f)
		return string(data), false, err
	}
	buf := make([]byte, maxBytes)
	if _, err := f.ReadAt(buf, size-int64(maxBytes)); err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	return string(buf), true, nil
}
