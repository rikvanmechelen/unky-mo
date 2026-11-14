package web

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Claude Code's slash commands open menus (/model, /config, /permissions,
// /mcp, …) that all follow the same few patterns, drawn in place of the
// prompt box under a "▔▔▔" edge: a title (or a tab strip whose first label
// is the title), an optional subtitle and search box, rows with a "❯"
// cursor and state glyphs, optional extra content (a slider, a preview, a
// status panel), and a legend of the keys that work on this screen
// ("Enter to select · Esc to cancel"). parseMenu reads that structure from
// a capture of the pane so the browser can draw any of them, and
// docs/plans/slash-menus.md has the survey behind it. Probed on Claude
// Code 2.1.325; captures are in testdata/menus/.

// menuView is a menu as drawn on the screen.
type menuView struct {
	Title    string     `json:"title"`
	Subtitle string     `json:"subtitle,omitempty"`
	Tabs     []menuTab  `json:"tabs,omitempty"`
	Input    *menuInput `json:"input,omitempty"`
	Rows     []menuRow  `json:"rows"`
	Cursor   int        `json:"cursor"` // index into Rows, -1 when none
	Above    int        `json:"above,omitempty"`
	Below    int        `json:"below,omitempty"`
	Extra    []string   `json:"extra,omitempty"`
	Legend   []menuKey  `json:"legend,omitempty"`
	Raw      string     `json:"raw"`
	Sig      string     `json:"sig"`
}

type menuTab struct {
	Label  string `json:"label"`
	Active bool   `json:"active,omitempty"`
}

// menuInput is the text box a menu draws: Kind "search" (has a ⌕) or
// "text". Placeholder is the grey hint shown while Text is empty.
type menuInput struct {
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	Placeholder string `json:"placeholder,omitempty"`
}

type menuRow struct {
	Text      string   `json:"text"`
	Cols      []string `json:"cols"`
	Marker    string   `json:"marker,omitempty"`
	Number    int      `json:"number,omitempty"`
	Group     string   `json:"group,omitempty"`
	Cursor    bool     `json:"cursor,omitempty"`
	MoreAbove bool     `json:"moreAbove,omitempty"`
	MoreBelow bool     `json:"moreBelow,omitempty"`
}

// menuKey is one entry of the legend. Keys are what to press, in the
// vocabulary of menuKeyToTmux ("enter", "esc", "left", "s", "C-t"); Typing
// is "Type to search": any text goes to the menu's input.
type menuKey struct {
	Keys   []string `json:"keys"`
	Label  string   `json:"label"`
	Typing bool     `json:"typing,omitempty"`
}

const menuEdgeMin = 10

var (
	sgrRe       = regexp.MustCompile("\x1b\\[[0-9;]*m")
	tabGapRe    = regexp.MustCompile(`\s{2,}`)
	legendRe    = regexp.MustCompile(`(?i)\besc(?:ape)? to `)
	legendPart  = regexp.MustCompile(`^(\S+) (to|for) (.+)$`)
	moreRe      = regexp.MustCompile(`^(\d+) more\b`)
	rowNumberRe = regexp.MustCompile(`^(\d+)\.\s+(.*)$`)
	ctrlKeyRe   = regexp.MustCompile(`(?i)^ctrl\+([a-z])$`)
)

const menuMarkers = "✔✘✓✗◯○●◉⚠→▸›☐☑"

func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

// isMenuEdge: a menu's top edge runs from column 0 across the pane. The
// transcript can show "▔" too (a tool's output, a quoted capture), but
// indented under its ⎿.
func isMenuEdge(plain string) bool {
	return strings.HasPrefix(plain, strings.Repeat("▔", menuEdgeMin))
}

// parseMenu reads the menu on screen, or returns nil when there is none.
// screen may carry SGR colours (capture-pane -e), which is the only way to
// tell which tab is active; without them no tab is marked.
func parseMenu(screen string) *menuView {
	styledLines := strings.Split(screen, "\n")
	plain := cropSidePanel(stripSGR(screen))
	lines := strings.Split(plain, "\n")
	if len(styledLines) != len(lines) {
		styledLines = nil
	}

	edge := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isMenuEdge(lines[i]) {
			edge = i
			break
		}
	}
	if edge < 0 {
		return nil
	}
	// A menu replaces the prompt box. With the box below the edge, the
	// edge is something in the transcript, and keys sent to this "menu"
	// would go to the prompt.
	for _, l := range lines[edge+1:] {
		if isRule(l) {
			return nil
		}
	}
	width := len([]rune(strings.TrimRight(lines[edge], " ")))
	end := len(lines)
	for end > edge+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	first := edge + 1
	for first < end && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	if first >= end {
		return nil
	}
	block := make([]string, 0, end-first)
	for _, l := range lines[first:end] {
		block = append(block, strings.TrimRight(l, " \t"))
	}
	base := indentOf(block[0])

	v := &menuView{Cursor: -1}
	v.Raw = strings.TrimRight(dedent(block, base), "\n")

	// The legend can sit on any line (/skills ends on a note).
	skip := map[int]bool{}
	for i := len(block) - 1; i >= 0; i-- {
		if legendRe.MatchString(block[i]) {
			v.Legend = parseLegend(block[i])
			skip[i] = true
			break
		}
	}

	// Find where the list starts (a cursor, a scroll marker or a numbered
	// row) and where an input box starts: lines at the base indent before
	// either are the subtitle, and a block with neither is plain content.
	hasList := false
	stop := len(block)
	listStart := len(block)
	for i, l := range block {
		if skip[i] {
			continue
		}
		t := strings.TrimSpace(l)
		if isCursorLine(l, base) || isScrollLine(t) || (indentOf(l) >= base+2 && rowNumberRe.MatchString(t)) {
			hasList = true
			listStart = i
			if i < stop {
				stop = i
			}
			break
		}
	}
	for i, l := range block {
		if strings.HasPrefix(strings.TrimSpace(l), "╭") {
			if i < stop {
				stop = i
			}
			break
		}
	}
	tabLineFound := false
	listEnded := false
	var subtitle []string
	var extra []string
	var rows []menuRow
	var rowIndent []int
	inBox := false

	for i := 0; i < len(block); i++ {
		if skip[i] {
			continue
		}
		l := block[i]
		t := strings.TrimSpace(l)
		if t == "" {
			if len(extra) > 0 && extra[len(extra)-1] != "" {
				extra = append(extra, "")
			}
			continue
		}
		if inBox {
			if strings.HasPrefix(t, "╰") {
				inBox = false
			} else if strings.HasPrefix(t, "│") {
				v.Input = parseInputLine(t)
			}
			continue
		}
		if strings.HasPrefix(t, "╭") {
			inBox = true
			continue
		}
		ind := indentOf(l)

		if !tabLineFound && v.Title == "" && ind == base {
			if segs := tabGapRe.Split(t, -1); len(segs) >= 3 {
				tabLineFound = true
				v.Title = segs[0]
				activeLabel := ""
				if styledLines != nil {
					activeLabel = activeSegment(styledLines[first+i])
				}
				for _, s := range segs[1:] {
					v.Tabs = append(v.Tabs, menuTab{Label: s, Active: activeLabel != "" && s == activeLabel})
				}
				continue
			}
		}

		cursorLine := isCursorLine(l, base)
		scrollLine := isScrollLine(t)
		if scrollLine {
			if m := moreRe.FindStringSubmatch(strings.TrimSpace(t[len("↓"):])); m != nil {
				n, _ := strconv.Atoi(m[1])
				if strings.HasPrefix(t, "↓") {
					v.Below = n
				} else {
					v.Above = n
				}
				continue
			}
		}

		// Above the first row, unmarked text that hangs deep or runs long is
		// a paragraph's continuation (a detail page's "Issue:" line), not a
		// row; shorter lines there are rows (/rewind and /theme list some
		// above the cursor).
		prose := hasList && i < listStart && !cursorLine && !scrollLine && !startsWithMarker(t) &&
			!rowNumberRe.MatchString(t) && (ind >= base+8 || len([]rune(t)) > 60) &&
			!(ind == base+2 && nextLineIsMarked(block, skip, i))
		inList := hasList && !listEnded && !prose && (cursorLine || scrollLine || ind >= base+2)
		switch {
		case inList:
			body := t
			row := menuRow{}
			switch {
			case cursorLine:
				row.Cursor = true
				body = strings.TrimSpace(strings.TrimPrefix(t, "❯"))
			case scrollLine:
				row.MoreBelow = strings.HasPrefix(t, "↓")
				row.MoreAbove = strings.HasPrefix(t, "↑")
				body = strings.TrimSpace(t[len("↓"):])
			}
			if m := rowNumberRe.FindStringSubmatch(body); m != nil {
				row.Number, _ = strconv.Atoi(strings.TrimSuffix(strings.Fields(body)[0], "."))
				body = m[2]
			}
			if r, size := firstRune(body); strings.ContainsRune(menuMarkers, r) && strings.HasPrefix(body[size:], " ") {
				row.Marker = string(r)
				body = strings.TrimSpace(body[size:])
			}
			row.Cols = tabGapRe.Split(body, -1)
			row.Text = strings.Join(row.Cols, "  ")
			rows = append(rows, row)
			rowIndent = append(rowIndent, ind)
		case ind == base && v.Title == "":
			v.Title = t
		case (ind <= base+1 && i < stop && stop < len(block)) || prose:
			// A line the terminal wrapped continues the one before.
			if n := len(subtitle); n > 0 && i > 0 && wrappedBefore(block[i-1], t, width) {
				subtitle[n-1] += " " + t
			} else {
				subtitle = append(subtitle, t)
			}
		default:
			if hasList && len(rows) > 0 {
				listEnded = true
			}
			if ind >= base {
				extra = append(extra, l[base:])
			} else {
				extra = append(extra, t)
			}
		}
	}

	// A heading is an unmarked line at the list's indent that introduces
	// rows with a glyph or number (a bare list can't be told from rows); it
	// names the rows after it instead of being one.
	group := ""
	for i, r := range rows {
		isPlain := !r.Cursor && r.Marker == "" && r.Number == 0 && !r.MoreBelow && !r.MoreAbove
		if isPlain && rowIndent[i] == base+2 && nextRowIsMarked(rows, i) {
			group = r.Text
			continue
		}
		r.Group = group
		v.Rows = append(v.Rows, r)
	}
	if v.Rows == nil {
		v.Rows = []menuRow{}
	}
	for i, r := range v.Rows {
		if r.Cursor {
			v.Cursor = i
			break
		}
	}
	v.Subtitle = strings.Join(subtitle, "\n")
	v.Extra = trimBlank(extra)

	if v.Title == "" && len(v.Rows) == 0 && len(v.Extra) == 0 {
		return nil
	}
	v.Sig = menuSig(v)
	return v
}

// wrappedBefore reports whether prev ended where it did only because next's
// first word didn't fit in width columns: word wrap, not a line break.
func wrappedBefore(prev, next string, width int) bool {
	prev = strings.TrimRight(prev, " ")
	if strings.TrimSpace(prev) == "" || width <= 0 {
		return false
	}
	word := strings.Fields(next)[0]
	return len([]rune(prev))+1+len([]rune(word)) > width
}

// nextLineIsMarked reports whether the next line after block[i] starts a
// row with a glyph or a number: what makes an unmarked line above it a
// group heading.
func nextLineIsMarked(block []string, skip map[int]bool, i int) bool {
	for j := i + 1; j < len(block); j++ {
		if skip[j] || strings.TrimSpace(block[j]) == "" {
			continue
		}
		t := strings.TrimSpace(block[j])
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "❯"), "↓"))
		return startsWithMarker(t) || rowNumberRe.MatchString(t)
	}
	return false
}

func startsWithMarker(t string) bool {
	r, size := firstRune(t)
	return strings.ContainsRune(menuMarkers, r) && strings.HasPrefix(t[size:], " ")
}

func nextRowIsMarked(rows []menuRow, i int) bool {
	if i+1 >= len(rows) {
		return false
	}
	n := rows[i+1]
	return n.Marker != "" || n.Number != 0
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func isCursorLine(l string, base int) bool {
	return indentOf(l) == base && strings.HasPrefix(strings.TrimSpace(l), "❯ ")
}

func isScrollLine(t string) bool {
	return strings.HasPrefix(t, "↓ ") || strings.HasPrefix(t, "↑ ")
}

func dedent(lines []string, n int) string {
	var out []string
	for _, l := range lines {
		if len(l) >= n && strings.TrimSpace(l[:n]) == "" {
			out = append(out, l[n:])
		} else {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return strings.Join(out, "\n")
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// parseInputLine reads "│ ⌕ Search settings…   │" (the right border may be
// cropped away).
func parseInputLine(t string) *menuInput {
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│"))
	in := &menuInput{Kind: "text"}
	if strings.HasPrefix(t, "⌕") {
		in.Kind = "search"
		t = strings.TrimSpace(strings.TrimPrefix(t, "⌕"))
	}
	if strings.HasSuffix(t, "…") {
		in.Placeholder = t
	} else {
		in.Text = t
	}
	return in
}

// parseLegend splits a footer such as "Enter/Space to change · / to search ·
// Esc to close" (or the comma-separated form /skills uses) into keys.
func parseLegend(line string) []menuKey {
	var out []menuKey
	for _, part := range strings.Split(strings.TrimSpace(line), " · ") {
		for _, p := range strings.Split(part, ", ") {
			m := legendPart.FindStringSubmatch(strings.TrimSpace(p))
			if m == nil {
				continue
			}
			k := menuKey{Label: m[3]}
			if m[2] == "for" {
				k.Label = "for " + m[3]
			}
			if strings.EqualFold(m[1], "type") {
				k.Typing = true
				k.Keys = []string{}
			} else {
				k.Keys = legendKeys(m[1])
			}
			out = append(out, k)
		}
	}
	return out
}

// legendKeys turns "Enter/↓", "←/→", "ctrl+t", "s" into key names.
func legendKeys(s string) []string {
	var toks []string
	if s == "/" {
		toks = []string{"/"}
	} else {
		toks = strings.Split(s, "/")
	}
	var keys []string
	for _, t := range toks {
		if t == "" {
			continue
		}
		switch strings.ToLower(t) {
		case "enter", "return":
			keys = append(keys, "enter")
		case "esc", "escape":
			keys = append(keys, "esc")
		case "tab":
			keys = append(keys, "tab")
		case "space":
			keys = append(keys, "space")
		case "↑":
			keys = append(keys, "up")
		case "↓":
			keys = append(keys, "down")
		case "←":
			keys = append(keys, "left")
		case "→":
			keys = append(keys, "right")
		default:
			if m := ctrlKeyRe.FindStringSubmatch(t); m != nil {
				keys = append(keys, "C-"+strings.ToLower(m[1]))
			} else {
				keys = append(keys, t)
			}
		}
	}
	return keys
}

// activeSegment returns the text drawn with a background (or reverse
// video) in a styled line: how Claude Code marks the active tab.
func activeSegment(styled string) string {
	hl := false
	var cur strings.Builder
	i := 0
	for i < len(styled) {
		if loc := sgrRe.FindStringIndex(styled[i:]); loc != nil && loc[0] == 0 {
			hl = applySGR(styled[i+2:i+loc[1]-1], hl)
			if !hl && strings.TrimSpace(cur.String()) != "" {
				return strings.TrimSpace(cur.String())
			}
			i += loc[1]
			continue
		}
		r, size := firstRune(styled[i:])
		if hl {
			cur.WriteRune(r)
		}
		i += size
	}
	return strings.TrimSpace(cur.String())
}

// applySGR folds one escape's parameters into the highlighted state.
func applySGR(params string, hl bool) bool {
	if params == "" {
		return false
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0 || n == 27 || n == 49:
			hl = false
		case n == 7 || (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
			hl = true
		case n == 48 || n == 38:
			// 5;n or 2;r;g;b follow; only a background counts.
			skip := 0
			if i+1 < len(ps) {
				switch ps[i+1] {
				case "5":
					skip = 2
				case "2":
					skip = 4
				}
			}
			if n == 48 {
				hl = true
			}
			i += skip
		}
	}
	return hl
}

func menuSig(v *menuView) string {
	c := *v
	c.Sig, c.Raw = "", ""
	b, _ := json.Marshal(c)
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:8])
}
