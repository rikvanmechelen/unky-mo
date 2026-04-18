package usage

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	barFilledRune = "▓"
	barEmptyRune  = "░"
)

// RenderBar returns a width-char bar like "▓▓▓▓▓░░░░░" for util in [0,100].
// The /api/oauth/usage endpoint returns utilization as a percentage, so all
// render helpers work in percent — no /100 at the call site.
// No color — callers apply lipgloss.Style.Render to the result.
func RenderBar(util float64, width int) string {
	filled, empty := SplitBar(util, width)
	return filled + empty
}

// SplitBar returns the filled and empty halves separately so callers can
// color them independently. util is in percent (0..100).
func SplitBar(util float64, width int) (filled, empty string) {
	if width <= 0 {
		return "", ""
	}
	if math.IsNaN(util) || util < 0 {
		util = 0
	}
	if util > 100 {
		util = 100
	}
	n := int(math.Round(util / 100 * float64(width)))
	if n > width {
		n = width
	}
	if n < 0 {
		n = 0
	}
	return strings.Repeat(barFilledRune, n), strings.Repeat(barEmptyRune, width-n)
}

// FormatResetIn returns a compact countdown like "2h15m", "45m", "12s".
// Returns "now" once we've passed the reset time, and "—" for a zero time.
func FormatResetIn(now, resetsAt time.Time) string {
	if resetsAt.IsZero() {
		return "—"
	}
	d := resetsAt.Sub(now)
	if d <= 0 {
		return "now"
	}
	switch {
	case d >= time.Hour:
		h := int(d / time.Hour)
		mins := int(d%time.Hour) / int(time.Minute)
		return fmt.Sprintf("%dh%02dm", h, mins)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// FormatTokensShort formats a token count compactly: 1234567 -> "1.2M",
// 12345 -> "12.3k", 567 -> "567". Negative values are clamped to 0.
func FormatTokensShort(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

// PctFromUtil rounds a 0..100 utilization to a 0..100 integer percentage.
// Values outside the range are clamped.
func PctFromUtil(util float64) int {
	if math.IsNaN(util) || util < 0 {
		return 0
	}
	if util > 100 {
		return 100
	}
	return int(math.Round(util))
}
