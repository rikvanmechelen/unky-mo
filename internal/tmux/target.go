package tmux

import "strings"

// SafeTarget returns a tmux target string for a window that is safe to use
// even when the window name contains dots (which tmux misinterprets as pane
// separators). When the provided window list contains a matching window, its
// stable ID (@N) is used instead. Falls back to session:name when no match
// is found (e.g. the window was already killed).
func SafeTarget(session, name string, windows []Window) string {
	for _, w := range windows {
		if w.Name == name {
			return session + ":" + w.ID
		}
	}
	return session + ":" + name
}

// NeedsSafeTarget reports whether a window name contains characters that
// tmux would misinterpret in a target string (currently just dots, which
// tmux treats as pane separators).
func NeedsSafeTarget(name string) bool {
	return strings.Contains(name, ".")
}
