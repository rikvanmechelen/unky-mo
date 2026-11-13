package web

import "strings"

// On a wide terminal Claude Code draws a changes side panel beside the
// conversation: a "│" column with the changed-file list ("11 files changed
// +184 -9") and their diffs to its right. Every row of the capture then
// carries the panel's text, which breaks every parser in this package at
// once — a rule is no longer only "─", so parsePermissionDialog and
// parseInputBox find no dialog and no message box; a dialog row's label
// swallows a diff line, so a sig never matches twice; parseMode finds no
// footer, so the mode chip greys out. The session then can't be answered,
// prompted or switched from the browser at all.
//
// cropSidePanel cuts a capture back to the conversation column.
const sidePanelBar = '│'

const (
	// minSidePanelCol keeps a crop from eating the conversation: the panel
	// leaves the transcript far more room than this.
	minSidePanelCol = 20
	// minSidePanelRows is the fewest non-empty rows a screen needs before a
	// column of bars says anything.
	minSidePanelRows = 8
)

// cropSidePanel returns screen without the changes side panel, or
// unchanged when there is none.
//
// The panel is found by column rather than by the first "│" on each line,
// so box drawing inside the transcript (a table in a tool's output, a
// captured diff) doesn't cut lines short. The separator is drawn on every
// row of the panel, so it is the one column that most rows long enough to
// reach it have — rows where the conversation is shorter are trimmed before
// it by tmux and don't count against it. The thresholds leave room for a
// full-width dialog over the bottom of the panel, which is cheap to allow:
// only rows that carry the separator are cut, so a column found by mistake
// trims those rows and leaves everything else whole.
func cropSidePanel(screen string) string {
	lines := strings.Split(screen, "\n")
	rows := make([][]rune, len(lines))
	bars := make(map[int]int)  // column -> rows with a bar there
	reach := make(map[int]int) // column -> rows long enough to have one
	filled := 0
	for i, l := range lines {
		r := []rune(l)
		rows[i] = r
		if len(r) == 0 {
			continue
		}
		filled++
		for c, ch := range r {
			reach[c]++
			if ch == sidePanelBar {
				bars[c]++
			}
		}
	}
	if filled < minSidePanelRows {
		return screen
	}
	cut := -1
	for c, n := range bars {
		if c < minSidePanelCol || n*4 < reach[c]*3 || n*5 < filled*3 {
			continue
		}
		// The leftmost qualifying column: the panel's own text can hold
		// bars of its own, and only the separator bounds the conversation.
		if cut < 0 || c < cut {
			cut = c
		}
	}
	if cut < 0 {
		return screen
	}
	// Only rows that carry the separator are the panel's. Claude Code draws
	// some dialogs ("Teach auto mode about your environment?") full width,
	// over the panel; those rows reach past the column without a bar there
	// and must come through whole.
	for i, r := range rows {
		if len(r) > cut && r[cut] == sidePanelBar {
			lines[i] = strings.TrimRight(string(r[:cut]), " ")
		}
	}
	return strings.Join(lines, "\n")
}
