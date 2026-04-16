package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary   = lipgloss.Color("#7C3AED") // purple
	colorSecondary = lipgloss.Color("#6B7280") // gray
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorWarning   = lipgloss.Color("#F59E0B") // yellow
	colorDanger    = lipgloss.Color("#EF4444") // red
	colorMuted     = lipgloss.Color("#4B5563") // dim gray
	colorWhite     = lipgloss.Color("#F9FAFB")
	colorBg        = lipgloss.Color("#111827") // dark bg

	// Title bar
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Background(colorPrimary).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
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
			Foreground(colorSuccess).
			Bold(true)

	statusIdle = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)

	statusPermission = lipgloss.NewStyle().
				Foreground(colorDanger).
				Bold(true)

	statusNone = lipgloss.NewStyle().
			Foreground(colorMuted)

	// Notification badge
	notifBadgeStyle = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)
)
