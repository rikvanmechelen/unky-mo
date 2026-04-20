package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
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

// visibleCountFor returns how many tickets from a bucket are currently shown
// (respecting per-bucket cap + user expansion toggle).
func (m Model) visibleCountFor(g tickets.BucketGroup) int {
	if m.ticketsExpanded[g.Bucket] {
		return len(g.Tickets)
	}
	if m.ticketsPerBucket > 0 && len(g.Tickets) > m.ticketsPerBucket {
		return m.ticketsPerBucket
	}
	return len(g.Tickets)
}

// hasOverflowRow reports whether this bucket renders a "+N more" / "show
// less" row that's part of the focusable cursor range.
func (m Model) hasOverflowRow(g tickets.BucketGroup) bool {
	if m.ticketsPerBucket <= 0 {
		return false
	}
	return len(g.Tickets) > m.ticketsPerBucket
}

// ticketsVisibleLen returns the count of focusable rows in the tickets panel
// — tickets + "+N more" / "show less" rows (but NOT bucket headers).
func (m Model) ticketsVisibleLen() int {
	n := 0
	for _, g := range m.ticketsGroups {
		n += m.visibleCountFor(g)
		if m.hasOverflowRow(g) {
			n++ // overflow row is focusable
		}
	}
	return n
}

// ticketAtCursor returns the ticket at the current cursor position. Returns
// nil when the cursor lands on an overflow row (see bucketAtCursor).
func (m Model) ticketAtCursor() *tickets.Ticket {
	idx := m.ticketsCursor
	for _, g := range m.ticketsGroups {
		visible := m.visibleCountFor(g)
		if idx < visible {
			t := g.Tickets[idx]
			return &t
		}
		idx -= visible
		if m.hasOverflowRow(g) {
			if idx == 0 {
				return nil // cursor is on the overflow row
			}
			idx--
		}
	}
	return nil
}

// bucketAtOverflowCursor returns the bucket whose overflow row is currently
// selected, or ("", false) when the cursor isn't on one. Drives the toggle
// behavior of enter on the overflow row.
func (m Model) bucketAtOverflowCursor() (tickets.Bucket, bool) {
	idx := m.ticketsCursor
	for _, g := range m.ticketsGroups {
		visible := m.visibleCountFor(g)
		if idx < visible {
			return "", false
		}
		idx -= visible
		if m.hasOverflowRow(g) {
			if idx == 0 {
				return g.Bucket, true
			}
			idx--
		}
	}
	return "", false
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
		visible := m.visibleCountFor(g)
		for i := 0; i < visible; i++ {
			t := g.Tickets[i]
			selected := focused && m.ticketsCursor == rowIdx
			b.WriteString(renderTicketRow(t, selected, g.Bucket == tickets.BucketUnmapped, width))
			rowIdx++
		}
		if m.hasOverflowRow(g) {
			selected := focused && m.ticketsCursor == rowIdx
			b.WriteString(renderOverflowRow(g.Bucket, len(g.Tickets)-visible, m.ticketsExpanded[g.Bucket], selected))
			rowIdx++
		}
	}
	return b.String()
}

// renderOverflowRow renders the toggle row at the bottom of a capped bucket.
// When collapsed: "… +7 more". When expanded: "… show less". Focusable, so
// it gets a cursor arrow when selected.
func renderOverflowRow(bucket tickets.Bucket, hidden int, expanded, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	var label string
	if expanded {
		label = "… show less"
	} else {
		label = fmt.Sprintf("… +%d more", hidden)
	}
	style := footerDescStyle
	if selected {
		style = selectedItemStyle
	}
	return cursor + style.Render(label) + "\n"
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
