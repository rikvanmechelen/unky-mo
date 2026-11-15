package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
)

func (m Model) onProjectsRefresh(msg projectsRefreshMsg) (tea.Model, tea.Cmd) {
	items := mergeRefreshedProjects(m.list.Items(), msg.projects)
	m.list.SetItems(items)
	// Update the flat projects slice so refreshGitStatuses and other
	// paths that iterate m.projects see the new set.
	updated := make([]project.Project, len(items))
	for i, item := range items {
		updated[i] = item.(ProjectItem).project
	}
	m.projects = updated
	return m, nil
}

func (m Model) onNewProjectCreated(msg newProjectCreatedMsg) (tea.Model, tea.Cmd) {
	agent := m.defaultAgent()
	p := ops.LaunchParams{
		WindowName:    msg.name,
		Cwd:           msg.path,
		ShellCmd:      agent.Cmd,
		AgentKey:      agent.Key,
		AttachSidebar: true,
		SwitchFocus:   true,
	}
	opsCtx := m.ops
	return m, tea.Batch(
		func() tea.Msg {
			if _, err := ops.LaunchSession(opsCtx, p); err != nil {
				return statusMsgEvent(fmt.Sprintf("launch session: %v", err))
			}
			return statusMsgEvent(fmt.Sprintf("Created and launched %s", msg.name))
		},
		m.refreshProjects(),
	)
}

func (m Model) onGitStatus(msg gitStatusMsg) (tea.Model, tea.Cmd) {
	m.gitStatuses = map[string]project.GitStatus(msg)
	// Update ProjectItems with git info
	items := m.list.Items()
	for i, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		if gs, ok := m.gitStatuses[pi.project.Path]; ok {
			pi.git = gs
			items[i] = pi
		}
	}
	m.list.SetItems(items)
	return m, nil
}
