package tickets

import (
	"strings"
	"unicode"
)

// BranchNameForTicket returns a git-safe branch name composed of the ticket
// ID and a slug of its title — e.g. "OP-175-fix-auth-flow". When the title
// is empty, returns just the ID.
//
// Rules:
//   - lowercase
//   - non-alphanumeric runs collapse to a single '-'
//   - leading/trailing '-' trimmed
//   - slug capped at maxSlugLen characters, trimmed at the nearest '-'
func BranchNameForTicket(id, title string) string {
	id = strings.TrimSpace(id)
	slug := Slugify(title)
	if id == "" {
		return slug
	}
	if slug == "" {
		return id
	}
	return id + "-" + slug
}

const maxSlugLen = 50

// Slugify lowercases, replaces non-alphanumeric with '-', collapses runs,
// trims, and caps length. Exported because the `mo jira issue` diagnostic
// wants to preview the branch name too.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > maxSlugLen {
		out = out[:maxSlugLen]
		// Don't leave a trailing partial word fragment when possible.
		if i := strings.LastIndexByte(out, '-'); i > maxSlugLen/2 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	return out
}
