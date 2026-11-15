package tui

import (
	"fmt"
	"sort"

	gh "github.com/rvanmech/unky-mo/internal/github"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/project"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// detailRow represents one navigable item in the project detail view. Rows
// are organized as: one "branch" header per local branch, followed by
// "br-session" rows for each session discovered at that branch's checkout
// location. Branches that are neither the main checkout nor a worktree show
// a single "br-empty" row, or — when a synced session exists for the branch
// on another machine — a "br-remote" row.
type detailRow struct {
	kind       string                // "branch", "br-session", "br-empty", "br-remote"
	session    *claude.RecentSession // non-nil for br-session
	branch     *project.Branch       // non-nil for all branch-scoped rows
	path       string                // cwd to launch/resume in ("" if branch has no local checkout)
	remoteMeta *moSync.SessionMeta   // non-nil for br-remote
	// tmuxWindow is the real tmux window name currently running this row's
	// session, if any. Populated by buildDetailRows from a live windows ×
	// sessions cross-reference. Empty when the session has no live window
	// (historical resume). Lets the `enter` handler switch directly to the
	// correct sibling instead of recomputing project@branch.
	tmuxWindow string
}

// buildDetailRows constructs the flat list of navigable rows for the project
// detail view. Main-project sessions live under the main branch's row.
func (m *Model) buildDetailRows() {
	p := m.detailProject
	if p == nil {
		m.detailRows = nil
		return
	}

	// Build sessionID → tmux window name so each br-session row points at
	// the correct live window (primary or sibling). An empty result means
	// the session is historical-only and `enter` will launch a fresh window.
	windowBySession := m.sessionToWindowMap()

	var rows []detailRow
	for i := range m.detailBranches {
		b := &m.detailBranches[i]
		launchPath := ""
		switch {
		case b.WorktreePath != "":
			launchPath = b.WorktreePath
		case b.IsMain:
			launchPath = p.Path
		}
		rows = append(rows, detailRow{
			kind:   "branch",
			branch: b,
			path:   launchPath,
		})

		if launchPath == "" {
			if meta, ok := m.remoteSynced[b.Name]; ok {
				metaCopy := meta
				rows = append(rows, detailRow{
					kind:       "br-remote",
					branch:     b,
					remoteMeta: &metaCopy,
				})
			} else {
				rows = append(rows, detailRow{
					kind:   "br-empty",
					branch: b,
				})
			}
			continue
		}

		sessions := m.claude.RecentSessions(launchPath, 5)
		if len(sessions) == 0 {
			rows = append(rows, detailRow{
				kind:   "br-empty",
				branch: b,
				path:   launchPath,
			})
			continue
		}
		for j := range sessions {
			rows = append(rows, detailRow{
				kind:       "br-session",
				session:    &sessions[j],
				branch:     b,
				path:       launchPath,
				tmuxWindow: windowBySession[sessions[j].SessionID],
			})
		}
	}

	// Surface remote-only entries whose branch isn't in the local branch
	// list at all (e.g. the branch was never fetched). Group them under a
	// synthetic "Remote branches" header so the user can still pull them.
	known := map[string]bool{}
	for _, b := range m.detailBranches {
		known[b.Name] = true
	}
	var orphanBranches []string
	for branch := range m.remoteSynced {
		if branch == "" || known[branch] {
			continue
		}
		orphanBranches = append(orphanBranches, branch)
	}
	if len(orphanBranches) > 0 {
		sort.Strings(orphanBranches)
		for _, branch := range orphanBranches {
			meta := m.remoteSynced[branch]
			metaCopy := meta
			b := &project.Branch{Name: branch}
			rows = append(rows, detailRow{
				kind:   "branch",
				branch: b,
			})
			rows = append(rows, detailRow{
				kind:       "br-remote",
				branch:     b,
				remoteMeta: &metaCopy,
			})
		}
	}

	m.detailRows = rows
}

// detailCombinedLen returns the number of navigable rows.
func (m Model) detailCombinedLen() int {
	return len(m.detailRows)
}

// loadRecap loads the last few messages for the session at the current cursor.
func (m *Model) loadRecap() {
	m.detailRecap = nil
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return
	}
	row := m.detailRows[m.detailCursor]
	if row.session == nil {
		return
	}
	m.detailRecap = m.claude.LastMessages(row.path, row.session.SessionID, 6)
}

func (m Model) fetchPRs(projectPath string) tea.Cmd {
	return func() tea.Msg {
		prs, err := gh.ListPRs(projectPath)
		return prFetchMsg{prs: prs, err: err}
	}
}

func (m Model) fetchPRDetail(number int) tea.Cmd {
	return func() tea.Msg {
		if m.detailProject == nil {
			return prDetailMsg{err: fmt.Errorf("no project")}
		}
		detail, err := gh.GetPRDetail(m.detailProject.Path, number)
		return prDetailMsg{detail: detail, err: err}
	}
}

// currentProject returns the project for the current context —
// detailProject on the project detail screen, list selection on the dashboard.
func (m Model) currentProject() *project.Project {
	if m.detailProject != nil && m.screen == ScreenProject {
		return m.detailProject
	}
	item, ok := m.list.SelectedItem().(ProjectItem)
	if !ok {
		return nil
	}
	return &item.project
}

// detailContext resolves the tmux window name and cwd for the row the detail
// cursor is currently on. When the cursor is on a branch row whose checkout
// is a worktree, returns that worktree's window (<project>@<branch>) and
// path; otherwise returns the main project's. Returns (nil) if no project is
// in context.
func (m Model) detailContext() (windowName, cwd string, ok bool) {
	project, branch, cwd, ok := m.detailLaunchTarget()
	if !ok {
		return "", "", false
	}
	return ttmux.ComposeWindowName(project, branch, ""), cwd, true
}

// detailLaunchTarget returns the (project, branch, cwd) tuple for the row
// the detail cursor is currently on. Like detailContext but without
// pre-composing the window name, so callers that need to build sibling
// window names (e.g. "project [2]") can call ComposeWindowName themselves.
// branch is "" when the target is the main checkout.
func (m Model) detailLaunchTarget() (project, branch, cwd string, ok bool) {
	p := m.currentProject()
	if p == nil {
		return "", "", "", false
	}
	if m.screen == ScreenProject && m.detailCursor >= 0 && m.detailCursor < len(m.detailRows) {
		row := m.detailRows[m.detailCursor]
		if row.branch != nil && !row.branch.IsMain && row.branch.WorktreePath != "" {
			return p.Name, row.branch.Name, row.branch.WorktreePath, true
		}
	}
	return p.Name, "", p.Path, true
}

// currentBranchRow returns the Branch for the row under the detail cursor,
// or nil if the cursor isn't on a branch-scoped row.
func (m Model) currentBranchRow() *project.Branch {
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return nil
	}
	return m.detailRows[m.detailCursor].branch
}

// currentDetailRow returns the full detail row under the cursor (branch,
// session, kind, tmuxWindow) or nil if out of range. Used by `w` to tell a
// session row apart from a plain branch row so they can route to different
// flows.
func (m Model) currentDetailRow() *detailRow {
	if m.detailCursor < 0 || m.detailCursor >= len(m.detailRows) {
		return nil
	}
	return &m.detailRows[m.detailCursor]
}
