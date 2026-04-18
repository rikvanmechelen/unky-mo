package tmux

import (
	"strconv"
	"strings"
)

// ComposeWindowName builds the tmux window name for a Claude session.
//
// Examples:
//   - project="foo", branch="",     suffix=""    → "foo"
//   - project="foo", branch="feat", suffix=""    → "foo@feat"
//   - project="foo", branch="",     suffix="2"   → "foo [2]"
//   - project="foo", branch="feat", suffix="2"   → "foo@feat [2]"
//   - project="foo", branch="feat", suffix="dbg" → "foo@feat [dbg]"
//
// Primary (suffix == "") windows keep their bare name so existing code that
// looks windows up by `project` or `project@branch` keeps working.
func ComposeWindowName(project, branch, suffix string) string {
	base := project
	if branch != "" {
		base = project + "@" + branch
	}
	if suffix == "" {
		return base
	}
	return base + " [" + suffix + "]"
}

// ParseWindowName is the inverse of ComposeWindowName. Returns ok=false if
// the name cannot be parsed. Suffix is "" for primary windows.
//
// Assumes project names contain no '@' or " [" (directory basenames
// typically don't). Branches may contain any character git allows except
// the trailing " [".
func ParseWindowName(name string) (project, branch, suffix string, ok bool) {
	if name == "" {
		return "", "", "", false
	}
	rest := name
	if strings.HasSuffix(rest, "]") {
		if i := strings.LastIndex(rest, " ["); i >= 0 {
			suffix = rest[i+2 : len(rest)-1]
			rest = rest[:i]
		}
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		project = rest[:i]
		branch = rest[i+1:]
	} else {
		project = rest
	}
	if project == "" {
		return "", "", "", false
	}
	return project, branch, suffix, true
}

// NextAvailableOrdinal returns the lowest numeric ordinal ≥ 2 as a string
// such that ComposeWindowName(project, branch, <ordinal>) is not in
// existingNames. Used when launching a concurrent sibling session.
//
// Non-numeric sibling names (e.g. custom titles) do not occupy ordinal
// slots — a window named "foo [debug]" does not prevent "foo [2]" from
// being picked.
func NextAvailableOrdinal(existingNames []string, project, branch string) string {
	taken := make(map[string]bool, len(existingNames))
	for _, n := range existingNames {
		taken[n] = true
	}
	for n := 2; ; n++ {
		candidate := ComposeWindowName(project, branch, strconv.Itoa(n))
		if !taken[candidate] {
			return strconv.Itoa(n)
		}
	}
}
