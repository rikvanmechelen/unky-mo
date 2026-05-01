package ops

import (
	"github.com/rvanmech/unky-mo/internal/claude"
)

// TeamStatus describes a detected agent team and its members' runtime state.
type TeamStatus struct {
	Name        string
	LeadSession string // sessionID of the lead
	LeadWindow  string // tmux window ID of the lead (empty when unresolved)
	Teammates   []TeammateStatus
}

// TeammateStatus describes one teammate within a team.
type TeammateStatus struct {
	Name   string // role name from team config ("architect", "tester")
	Status string // "active" or "idle"
	PaneID string // tmux pane ID if discovered; empty otherwise
}

// ListTeams reads Claude Code agent team configs and resolves runtime state
// (which team leads are alive, which panes are teammate panes). The
// liveSessions + windowMap arguments let callers supply pre-fetched data
// when available (the TUI already has these); pass nil to have ListTeams
// fetch them.
//
// sessionWindows maps sessionID → window ID (stable tmux @N). When nil,
// no pane discovery is attempted.
func ListTeams(tc TmuxClient, sessionWindows map[string]string) ([]TeamStatus, error) {
	configs, err := claude.ReadTeamConfigs()
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return nil, nil
	}

	var results []TeamStatus
	for _, cfg := range configs {
		teammates := cfg.Teammates()
		if len(teammates) == 0 {
			continue
		}

		// Resolve the lead's window.
		lead := cfg.LeadMember()
		if lead == nil || lead.SessionID == "" {
			continue
		}
		windowID := ""
		if sessionWindows != nil {
			windowID = sessionWindows[lead.SessionID]
		}
		if windowID == "" {
			// Lead session not resolved to a window — still report the team
			// with config-only data (no pane info).
			ts := TeamStatus{
				Name:        cfg.Name,
				LeadSession: lead.SessionID,
			}
			for _, tm := range teammates {
				ts.Teammates = append(ts.Teammates, TeammateStatus{
					Name:   tm.Name,
					Status: "active",
				})
			}
			results = append(results, ts)
			continue
		}

		// Discover teammate panes in the lead's window.
		ts := TeamStatus{
			Name:        cfg.Name,
			LeadSession: lead.SessionID,
			LeadWindow:  windowID,
		}
		ts.Teammates = discoverTeammatePanes(tc, windowID, teammates)
		results = append(results, ts)
	}
	return results, nil
}

// discoverTeammatePanes enumerates panes in the lead's window and matches
// them positionally to the teammate list from the team config.
func discoverTeammatePanes(tc TmuxClient, windowID string, teammates []claude.TeamMember) []TeammateStatus {
	var result []TeammateStatus

	if tc != nil && windowID != "" {
		panes, err := tc.ListWindowPanes(windowID)
		if err == nil && len(panes) > 2 {
			// panes[0] = lead Claude, panes[1] = sidebar, panes[2+] = teammates.
			extraPanes := panes[2:]
			for i, tm := range teammates {
				paneID := ""
				if i < len(extraPanes) {
					paneID = extraPanes[i].ID
				}
				result = append(result, TeammateStatus{
					Name:   tm.Name,
					Status: "active",
					PaneID: paneID,
				})
			}
			return result
		}
	}

	// Fallback: no pane discovery possible — populate from config only.
	for _, tm := range teammates {
		result = append(result, TeammateStatus{
			Name:   tm.Name,
			Status: "active",
		})
	}
	return result
}
