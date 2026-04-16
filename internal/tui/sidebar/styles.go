package sidebar

import "github.com/charmbracelet/lipgloss"

var (
	// Colors — same palette as main TUI, tuned for dark backgrounds (~#14191E)
	colorPrimary = lipgloss.Color("#A78BFA")
	colorSuccess = lipgloss.Color("#34D399")
	colorWarning = lipgloss.Color("#FBBF24")
	colorDanger  = lipgloss.Color("#F87171")
	colorMuted   = lipgloss.Color("#6B7280")
	colorWhite   = lipgloss.Color("#F3F4F6")
	colorDim     = lipgloss.Color("#9CA3AF")

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
			Foreground(colorSuccess)

	dotIdle = lipgloss.NewStyle().
		Foreground(colorWarning)

	dotPermission = lipgloss.NewStyle().
			Foreground(colorDanger)

	dotNone = lipgloss.NewStyle().
		Foreground(colorMuted)

	footerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
)
