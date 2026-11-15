package tui

import (
	"sort"
	"strings"

	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
)

func (m *Model) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}
	state.Write(m.stateFilePath, m.buildStateFile())
}

// buildStateFile is the state the sidebars and mo web read: a row per live
// session (and one per project without any), in the dashboard's order.
func (m Model) buildStateFile() *state.StateFile {
	// Index the session views by the parent they render under.
	// - "projects" (primary + siblings on main checkout): key = projectPath
	// - worktrees:                                          key = projectPath|branch
	// - strays (git-backed): appended after known projects
	// - externals:           appended in their own section
	mainByProject := map[string][]sessionView{}
	worktreeByKey := map[string][]sessionView{}
	var stray, external []sessionView

	for _, v := range m.sessionViews {
		if v.IsStray {
			if v.Section == "external" {
				external = append(external, v)
			} else {
				stray = append(stray, v)
			}
			continue
		}
		if v.IsWorktree {
			key := v.ProjectPath + "|" + strings.TrimPrefix(v.ProjectName, "@")
			worktreeByKey[key] = append(worktreeByKey[key], v)
			continue
		}
		mainByProject[v.ProjectPath] = append(mainByProject[v.ProjectPath], v)
	}

	// Sort siblings within each group by Index (primary=0 first, ordinals next,
	// then custom-title windows). Stable within ties.
	for k := range mainByProject {
		sortViewsForDisplay(mainByProject[k])
	}
	for k := range worktreeByKey {
		sortViewsForDisplay(worktreeByKey[k])
	}

	var projects []state.ProjectState
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}

		// Main-checkout rows: one per live session. If none, emit a
		// placeholder StatusNone row so the sidebar still lists the project.
		views := mainByProject[pi.project.Path]
		if len(views) == 0 {
			projects = append(projects, state.ProjectState{
				Name:       pi.project.Name,
				Path:       pi.project.Path,
				WindowName: pi.project.Name,
				Status:     "none",
				Section:    "projects",
			})
		} else {
			// Main-checkout rows: parent stays "" (only worktree rows get a
			// parent — the sidebar indents them under it).
			for _, v := range views {
				projects = append(projects, viewToProjectState(v, "", pi.project.Name))
			}
		}

		// Worktrees for this project: enumerate distinct branches we saw.
		// Each branch may host multiple sessions (primary + siblings).
		branchesSeen := map[string]bool{}
		var branchOrder []string
		for key := range worktreeByKey {
			if !strings.HasPrefix(key, pi.project.Path+"|") {
				continue
			}
			branch := strings.TrimPrefix(key, pi.project.Path+"|")
			if !branchesSeen[branch] {
				branchesSeen[branch] = true
				branchOrder = append(branchOrder, branch)
			}
		}
		sort.Strings(branchOrder)
		for _, branch := range branchOrder {
			for _, v := range worktreeByKey[pi.project.Path+"|"+branch] {
				rowName := "@" + branch
				projects = append(projects, viewToProjectState(v, pi.project.Name, rowName))
			}
		}
	}

	// Strays: git-backed go into the projects section; externals go in their
	// own section so the sidebar can group them below known projects.
	for _, v := range stray {
		projects = append(projects, viewToProjectState(v, "", v.ProjectName))
	}
	for _, v := range external {
		projects = append(projects, viewToProjectState(v, "", v.ProjectName))
	}

	sessionName := ""
	if m.tmux != nil {
		sessionName = m.tmux.SessionName()
	}

	sf := &state.StateFile{
		TmuxSession: sessionName,
		Projects:    projects,
	}
	if m.usageReady {
		sf.Usage = &state.UsageState{
			FiveHourPct:      usage.PctFromUtil(m.usage.FiveHour.Utilization),
			FiveHourResetsAt: m.usage.FiveHour.ResetsAt,
			SevenDayPct:      usage.PctFromUtil(m.usage.SevenDay.Utilization),
			SevenDayResetsAt: m.usage.SevenDay.ResetsAt,
			FetchedAt:        m.usage.FetchedAt,
			Stale:            m.usage.Source == "stale",
			AuthError:        m.usageAuth,
		}
	}
	return sf
}

// viewToProjectState converts one sessionView into a state.ProjectState.
// rowBaseName + parent control how the Name renders for siblings (e.g.
// "unky-mo [2]", "@feature [debug-oauth]") so the sidebar picks them up
// correctly. Parent is "" for main-checkout rows and strays; the parent
// project name for worktree rows.
func viewToProjectState(v sessionView, parent, rowBaseName string) state.ProjectState {
	name := rowBaseName
	// When the window has a suffix (sibling ordinal or custom title), fold it
	// into the display Name. Primary (Index=0, suffix-less) keeps the base.
	if _, _, suffix, ok := ttmux.ParseWindowName(v.WindowName); ok && suffix != "" {
		name = rowBaseName + " [" + suffix + "]"
	}
	ps := state.ProjectState{
		Name:         name,
		Path:         v.CWD,
		WindowName:   v.WindowName,
		WindowID:     v.WindowID,
		InstanceID:   v.InstanceID,
		AgentKey:     v.AgentKey,
		Status:       statusToString(v.Status),
		Section:      v.Section,
		SessionID:    v.SessionID,
		Index:        v.Index,
		Parent:       parent,
		PendingTool:  v.PendingTool,
		PendingInput: v.PendingInput,
	}
	if v.IsStray {
		ps.Branch = v.Branch
		ps.Dirty = v.Dirty
	}
	if v.TeamName != "" {
		ps.TeamName = v.TeamName
		ps.TeamRole = v.TeamRole
		for _, tm := range v.Teammates {
			ps.Teammates = append(ps.Teammates, state.TeammateState{
				Name:   tm.Name,
				Status: tm.Status,
				PaneID: tm.PaneID,
			})
		}
	}
	return ps
}

// sortViewsForDisplay orders views within a (project, branch) group so the
// primary-ish row renders first, ordinal siblings next, custom-title
// windows last. Index conventions: 0 = bare primary, 2+ = ordinal, -1 =
// custom title (unknown ordering — fall back to WindowName alphabetical).
func sortViewsForDisplay(views []sessionView) {
	sort.SliceStable(views, func(i, j int) bool {
		ai, bi := views[i].Index, views[j].Index
		// Map to a sort rank: bare(0) < ordinal(>=2) < custom(-1)
		rank := func(n int) int {
			switch {
			case n == 0:
				return 0
			case n >= 2:
				return n
			default:
				return 1_000_000
			}
		}
		ri, rj := rank(ai), rank(bi)
		if ri != rj {
			return ri < rj
		}
		return views[i].WindowName < views[j].WindowName
	})
}
