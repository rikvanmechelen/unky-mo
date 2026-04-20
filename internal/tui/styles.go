package tui

import "charm.land/lipgloss/v2"

var (
	// Colors — tuned for dark backgrounds (~#14191E)
	colorPrimary   = lipgloss.Color("#A78BFA") // lighter purple for dark bg
	colorSecondary = lipgloss.Color("#9CA3AF") // readable gray (Tailwind gray-400)
	colorSuccess   = lipgloss.Color("#34D399") // brighter green
	colorWarning   = lipgloss.Color("#FBBF24") // brighter yellow
	colorDanger    = lipgloss.Color("#F87171") // brighter red
	colorMuted     = lipgloss.Color("#6B7280") // subtle but legible gray
	colorWhite     = lipgloss.Color("#F3F4F6")
	colorBg        = lipgloss.Color("#14191E")
	colorInfo      = lipgloss.Color("#60A5FA") // blue-400 — external / informational

	// Title bar
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Background(colorPrimary).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true)

	// Footer / help bar
	footerStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(colorMuted)

	footerKeyStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true)

	footerDescStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	// List items
	selectedItemStyle = lipgloss.NewStyle().
				Foreground(colorWhite).
				Bold(true)

	normalItemStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	// Language badges
	langStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// Status indicators
	statusActive = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)

	statusIdle = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	statusPermission = lipgloss.NewStyle().
				Foreground(colorDanger).
				Bold(true)

	statusNone = lipgloss.NewStyle().
			Foreground(colorMuted)

	// External: a live claude running outside mo's tmux session (e.g. started
	// from a VS Code terminal). Distinct color signals the "import me?" UX.
	statusExternal = lipgloss.NewStyle().
			Foreground(colorInfo).
			Bold(true)

	// Notification badge
	notifBadgeStyle = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)

	// Claude usage strip (dashboard footer)
	usageStripStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(colorMuted).
			Padding(0, 1)

	usageLabelStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true)

	barFilledStyle = lipgloss.NewStyle().Foreground(colorPrimary)
	barEmptyStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	barWarnStyle   = lipgloss.NewStyle().Foreground(colorWarning)
	barDangerStyle = lipgloss.NewStyle().Foreground(colorDanger)
)

// pickBarStyle returns the appropriate filled-segment color for a utilization
// percentage: primary below 70%, warning at 70-89%, danger at 90+%.
func pickBarStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 90:
		return barDangerStyle
	case pct >= 70:
		return barWarnStyle
	default:
		return barFilledStyle
	}
}
