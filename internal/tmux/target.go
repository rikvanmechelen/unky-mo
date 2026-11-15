package tmux

import "strings"

// SafeTarget returns a tmux target string for a window that is safe to use
// even when the window name contains dots (which tmux misinterprets as pane
// separators). When the provided window list contains a matching window, its
// stable ID (@N) is used instead. Falls back to session:name when no match
// is found (e.g. the window was already killed), as an exact-name target
// ("=name"): a bare name also matches the start of another window's name
// ("moma" → "moma-apps-rails"), so killing a window that's gone would kill
// a neighbour.
func SafeTarget(session, name string, windows []Window) string {
	for _, w := range windows {
		if w.Name == name {
			return session + ":" + w.ID
		}
	}
	return ExactTarget(session, name)
}

// ExactTarget is a target that matches only a window named exactly name.
func ExactTarget(session, name string) string {
	return session + ":=" + name
}

// NeedsSafeTarget reports whether a window name contains characters that
// tmux would misinterpret in a target string (currently just dots, which
// tmux treats as pane separators).
func NeedsSafeTarget(name string) bool {
	return strings.Contains(name, ".")
}

// EscapeFormat makes s literal where tmux expects a format (a popup's -T
// title): formats expand #{…} and run #(…) as a shell command, and the
// text can be a file name or a command Claude ran.
func EscapeFormat(s string) string {
	return strings.ReplaceAll(s, "#", "##")
}
