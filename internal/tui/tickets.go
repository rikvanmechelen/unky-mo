package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/tickets/jira"
)

// ticketsShouldRender returns true whenever the user has shown intent to use
// the tickets panel — providers were built, instances are declared in
// config, or a token is present (env var or file). This keeps partial
// setups visible so the onboarding message can guide the next step.
// Users can hide the panel entirely with `enabled = false` in config.
func (m Model) ticketsShouldRender() bool {
	// Explicit opt-out wins.
	if m.ticketsDisabled {
		return false
	}
	if len(m.ticketsProviders) > 0 {
		return true
	}
	if len(m.ticketsInstances) > 0 {
		return true
	}
	return jira.HasToken()
}

// ticketsVisibleLen returns the count of focusable rows in the tickets panel
// (tickets only — section headers and "+N more" are not focusable). Used
// for cursor wrap-around.
func (m Model) ticketsVisibleLen() int {
	n := 0
	for _, g := range m.ticketsGroups {
		visible := len(g.Tickets)
		if m.ticketsPerBucket > 0 && visible > m.ticketsPerBucket {
			visible = m.ticketsPerBucket
		}
		n += visible
	}
	return n
}

// ticketAtCursor returns the ticket at the current cursor position, or nil
// if the cursor is out of range (no tickets loaded yet).
func (m Model) ticketAtCursor() *tickets.Ticket {
	idx := m.ticketsCursor
	for _, g := range m.ticketsGroups {
		visible := len(g.Tickets)
		if m.ticketsPerBucket > 0 && visible > m.ticketsPerBucket {
			visible = m.ticketsPerBucket
		}
		if idx < visible {
			t := g.Tickets[idx]
			return &t
		}
		idx -= visible
	}
	return nil
}

// renderTicketsPanel returns the tickets pane contents for the dashboard
// right column, already wrapped to width. Callers compose it below the
// sessions section via JoinVertical.
func (m Model) renderTicketsPanel(width int) string {
	if !m.ticketsShouldRender() {
		return ""
	}

	focused := !m.dashFocusLeft && m.dashRightFocus == dashRightTickets
	focusIndicator := ""
	if focused {
		focusIndicator = " ◀"
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("Tickets"+focusIndicator) + "\n")

	// Onboarding state: no providers constructed yet.
	if len(m.ticketsProviders) == 0 {
		b.WriteString(renderTicketsOnboarding(jira.HasToken(), len(m.ticketsInstances) > 0, width))
		return b.String()
	}

	if !m.ticketsLoaded {
		b.WriteString("  " + footerDescStyle.Render("Loading…") + "\n")
		return b.String()
	}

	// Surface provider errors (auth failure, rate-limit) without hiding
	// other providers' data. Shown once at the top of the panel.
	for _, r := range m.ticketsErrors {
		if r.Err == nil {
			continue
		}
		msg := r.Err.Error()
		if len(msg) > width-4 {
			msg = msg[:width-7] + "..."
		}
		b.WriteString("  " + statusPermission.Render("!") + " " + footerDescStyle.Render(msg) + "\n")
	}

	if len(m.ticketsGroups) == 0 {
		b.WriteString("  " + footerDescStyle.Render("No assigned tickets") + "\n")
		return b.String()
	}

	rowIdx := 0
	for _, g := range m.ticketsGroups {
		b.WriteString("  " + footerDescStyle.Render(tickets.DisplayLabel(g.Bucket)) + "\n")
		limit := len(g.Tickets)
		if m.ticketsPerBucket > 0 && limit > m.ticketsPerBucket {
			limit = m.ticketsPerBucket
		}
		for i := 0; i < limit; i++ {
			t := g.Tickets[i]
			selected := focused && m.ticketsCursor == rowIdx
			b.WriteString(renderTicketRow(t, selected, g.Bucket == tickets.BucketUnmapped, width))
			rowIdx++
		}
		if overflow := len(g.Tickets) - limit; overflow > 0 {
			b.WriteString("  " + footerDescStyle.Render(fmt.Sprintf("… +%d more", overflow)) + "\n")
		}
	}
	return b.String()
}

// renderTicketRow renders a single ticket line. Format:
//
//	▸ ⚡ OP-175  In Progress    fix auth flow
//
// - cursor arrow when selected
// - sprint bolt when in an active sprint
// - raw status in dim (for Unmapped rows, the raw status is the important
//   thing to surface so workflow drift is obvious)
func renderTicketRow(t tickets.Ticket, selected, unmapped bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}

	bolt := "  "
	if t.InSprint {
		bolt = statusActive.Render("⚡") + " "
	}

	id := t.ID
	if id == "" {
		id = "—"
	}
	var idRender string
	if selected {
		idRender = selectedItemStyle.Render(id)
	} else {
		idRender = normalItemStyle.Render(id)
	}

	rawStatus := t.RawStatus
	statusStyle := footerDescStyle
	if unmapped {
		statusStyle = statusPermission
		rawStatus = "[" + rawStatus + "]"
	}
	statusCell := statusStyle.Render(rawStatus)

	// Compose: cursor(2) + bolt(2) + id(var) + space + status(var) + space + title(rest)
	prefix := cursor + bolt + idRender + " " + statusCell + " "
	// Lipgloss styles add escape codes; measure visible width via Width().
	visiblePrefix := lipgloss.Width(prefix)
	remaining := width - visiblePrefix
	title := t.Title
	if remaining > 0 && len(title) > remaining {
		title = title[:remaining-1] + "…"
	}
	if !selected {
		title = footerDescStyle.Render(title)
	}
	return prefix + title + "\n"
}

// renderTicketsOnboarding shows the "not configured" message with lines
// tailored to whichever piece is missing (token vs. config instance). Width
// is currently unused but kept for future wrapping logic.
func renderTicketsOnboarding(hasToken, hasInstances bool, width int) string {
	var lines []string
	lines = append(lines, "  "+footerDescStyle.Render("Not configured."))

	if !hasToken {
		lines = append(lines,
			"  "+footerDescStyle.Render("Add a Jira API token:"),
			"  "+normalItemStyle.Render("~/.config/unky-mo/jira.token"),
			"  "+footerDescStyle.Render("(chmod 600; or set"),
			"  "+footerDescStyle.Render(" UNKY_MO_JIRA_TOKEN env var)"),
		)
	}
	if !hasInstances {
		lines = append(lines,
			"  "+footerDescStyle.Render("Add to config.toml:"),
			"  "+normalItemStyle.Render("[[tickets.jira]]"),
			"  "+normalItemStyle.Render("base_url = \"https://…\""),
			"  "+normalItemStyle.Render("email = \"you@…\""),
		)
	}
	lines = append(lines, "  "+footerDescStyle.Render("Then ctrl+r to refresh."))
	return strings.Join(lines, "\n") + "\n"
}

// openInBrowser shells out to the platform's default opener. Fire-and-forget;
// errors are returned so callers can surface a status message.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
