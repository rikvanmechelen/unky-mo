package tui

import (
	"strings"

	"github.com/rvanmech/unky-mo/internal/config"
)

// agentTagForKey returns a short display tag for the given agent key,
// e.g. "(gemini)" for key "g". Returns "" for the default agent (Claude)
// since it's the implied default and doesn't need a tag.
func (m Model) agentTagForKey(key string) string {
	if key == "" {
		return ""
	}
	def := m.defaultAgent()
	if key == def.Key {
		return ""
	}
	for _, a := range m.agents {
		if a.Key == key {
			return "(" + strings.ToLower(a.Name) + ")"
		}
	}
	return "(" + key + ")"
}

// agentForBranch returns the agent to use for a project+branch. Resolution
// order: saved choice > default agent.
func (m Model) agentForBranch(projectName, branch string) config.AgentConfig {
	if key := config.LookupAgentChoice(m.agentChoices, projectName, branch); key != "" {
		if a := m.agentByKey(key); a != nil {
			return *a
		}
	}
	return m.defaultAgent()
}

// agentByKey looks up a configured agent by key, or nil.
func (m Model) agentByKey(key string) *config.AgentConfig {
	for i := range m.agents {
		if m.agents[i].Key == key {
			return &m.agents[i]
		}
	}
	return nil
}

// defaultAgent returns the user's default agent config.
func (m Model) defaultAgent() config.AgentConfig {
	for _, a := range m.agents {
		if a.Default {
			return a
		}
	}
	if len(m.agents) > 0 {
		return m.agents[0]
	}
	return config.DefaultAgents()[0]
}
