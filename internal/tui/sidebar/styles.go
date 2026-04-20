package sidebar

import "charm.land/lipgloss/v2"

var (
	// Colors — same palette as main TUI, tuned for dark backgrounds (~#14191E)
	colorPrimary = lipgloss.Color("#A78BFA")
	colorSuccess = lipgloss.Color("#34D399")
	colorWarning = lipgloss.Color("#FBBF24")
	colorDanger  = lipgloss.Color("#F87171")
	colorMuted   = lipgloss.Color("#6B7280")
	colorWhite   = lipgloss.Color("#F3F4F6")
	colorDim     = lipgloss.Color("#9CA3AF")
	colorInfo    = lipgloss.Color("#60A5FA")

	headerStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true)

	normalStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	currentStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true).
			Underline(true)

	homeStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	dotActive = lipgloss.NewStyle().
			Foreground(colorWarning)

	dotIdle = lipgloss.NewStyle().
		Foreground(colorSuccess)

	dotPermission = lipgloss.NewStyle().
			Foreground(colorDanger)

	dotNone = lipgloss.NewStyle().
		Foreground(colorMuted)

	dotExternal = lipgloss.NewStyle().
			Foreground(colorInfo).
			Bold(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	termActiveStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	// Claude usage line (above footer)
	usageLineStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// White horizontal rules bracketing the usage line
	usageRuleStyle = lipgloss.NewStyle().
			Foreground(colorWhite)

	usageBarFilled = lipgloss.NewStyle().Foreground(colorPrimary)
	usageBarEmpty  = lipgloss.NewStyle().Foreground(colorMuted)
	usageBarWarn   = lipgloss.NewStyle().Foreground(colorWarning)
	usageBarDanger = lipgloss.NewStyle().Foreground(colorDanger)
)

// pickBarStyle returns the appropriate filled-segment color for a utilization
// percentage: primary below 70%, warning at 70-89%, danger at 90+%.
func pickBarStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 90:
		return usageBarDanger
	case pct >= 70:
		return usageBarWarn
	default:
		return usageBarFilled
	}
}
