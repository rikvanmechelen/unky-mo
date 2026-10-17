package web

import "strings"

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
func parseInputBox(screen string) (text string, ok bool) {
	lines := strings.Split(screen, "\n")
	bottom, top := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRule(lines[i]) {
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
	for _, l := range lines[top+1 : bottom] {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "❯"))
		if l != "" {
			parts = append(parts, l)
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
