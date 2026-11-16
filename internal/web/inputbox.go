package web

import (
	"strings"
	"unicode/utf8"
)

// inputPlaceholders are the greyed hints Claude Code shows in an empty
// message box. While messages are queued the box says
// "Press up to edit queued messages"; a fresh session suggests a question.
var inputPlaceholders = []string{
	"Press up to edit queued messages",
	"Try \"",
}

// parseInputBox reads the text in Claude Code's message box: the lines
// between the last two horizontal rules, minus the "❯" marker. ok is false
// when the screen has no such box (e.g. a dialog replaced it). A
// placeholder hint counts as empty.
//
// screen may carry SGR colours (capture-pane -e). Faint text in the box is
// then a hint, not typed text: an idle session suggests its next prompt
// there ("❯ yes, go ahead" drawn dim), and only the colours tell it apart.
func parseInputBox(screen string) (text string, ok bool) {
	lines := strings.Split(cropSidePanel(stripSGR(screen)), "\n")
	typed := strings.Split(blankFaint(screen), "\n")
	bottom, top := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isBoxRule(lines[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		top = i
		break
	}
	if top < 0 {
		return "", false
	}
	var parts []string
	for i := top + 1; i < bottom; i++ {
		// blankFaint keeps every column, so the crop's width applies.
		l := []rune(typed[i])
		if n := len([]rune(lines[i])); len(l) > n {
			l = l[:n]
		}
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(l)), "❯"))
		if s != "" {
			parts = append(parts, s)
		}
	}
	text = strings.Join(parts, "\n")
	for _, p := range inputPlaceholders {
		if strings.HasPrefix(text, p) {
			return "", true
		}
	}
	return text, true
}

// blankFaint strips screen's SGR sequences and turns faint (SGR 2) text
// into spaces, column for column. The attribute carries across lines, as
// the terminal keeps it.
func blankFaint(screen string) string {
	var b strings.Builder
	faint := false
	for i := 0; i < len(screen); {
		if screen[i] == '\x1b' {
			if loc := sgrRe.FindStringIndex(screen[i:]); loc != nil && loc[0] == 0 {
				faint = sgrFaint(screen[i+2:i+loc[1]-1], faint)
				i += loc[1]
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(screen[i:])
		if faint && r != '\n' {
			b.WriteByte(' ')
		} else {
			b.WriteString(screen[i : i+size])
		}
		i += size
	}
	return b.String()
}

// sgrFaint applies one SGR parameter list to the faint attribute: 2 sets
// it, 0 (or none) and 22 clear it. Extended colours' sub-parameters
// (38;5;n, 38;2;r;g;b) are skipped so their numbers aren't read as
// attributes.
func sgrFaint(params string, faint bool) bool {
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "", "0", "22":
			faint = false
		case "2":
			faint = true
		case "38", "48", "58":
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return faint
}

// isBoxRule is isRule for the message box's borders, which Claude Code can
// label with the session name ("──────── app ─").
func isBoxRule(line string) bool {
	if isRule(line) {
		return true
	}
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "───") && strings.HasSuffix(line, "─")
}
