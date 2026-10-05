package gitfiles

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// Row is one line of an annotated file: Sign is " " (in both versions),
// "+" (new version only, Ln set) or "-" (old version only, OldLn set).
type Row struct {
	Ln    int    `json:"ln,omitempty"`
	OldLn int    `json:"oldLn,omitempty"`
	Sign  string `json:"sign"`
	Text  string `json:"text"`
}

// Annotated is a whole file as its change shows it: every line of both
// versions in order, the removed ones marked "-" and the added ones "+".
// Binary and TooLarge files have no rows.
type Annotated struct {
	Path     string `json:"path"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"tooLarge,omitempty"`
	Rows     []Row  `json:"rows"`
}

// Excerpt is a cut of an Annotated file around some lines.
type Excerpt struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	Binary    bool   `json:"binary,omitempty"`
	TooLarge  bool   `json:"tooLarge,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Rows      []Row  `json:"rows"`
}

// Excerpt sides: which version's line numbers a request names.
const (
	SideNew = "new"
	SideOld = "old"
)

// MaxExcerptRows caps the rows one excerpt returns.
const MaxExcerptRows = 400

// maxDiffBytes caps a file's diff output: both versions, each at most
// MaxContentBytes, plus the per-line markers.
const maxDiffBytes = 3 * MaxContentBytes

// fullContext is the -U that makes git print the whole file as one hunk.
const fullContext = 1 << 30

// Annotate reads path as o's change shows it: from o.Rev to o.Head, or to
// the working tree when o.Head is empty. path is a file o lists (by its
// path, or a rename's old path), or else an unchanged file, read from the
// new side as plain rows. Both go through Resolve first, like every other
// read of a browser-supplied path.
func Annotate(ctx context.Context, cmd moexec.Commander, o *Overview, path string) (*Annotated, error) {
	if _, err := Resolve(o.Root, path); err != nil {
		return nil, err
	}
	for _, rev := range []string{o.Rev, o.Head} {
		if rev != "" && !hashRe.MatchString(rev) {
			return nil, ErrUnknownCommit
		}
	}
	var file *OverviewFile
	for i := range o.Files {
		if o.Files[i].Path == path || o.Files[i].OldPath == path {
			file = &o.Files[i]
			break
		}
	}
	a := &Annotated{Path: path}
	switch {
	case file == nil:
		return a, a.fromContent(newSide(ctx, cmd, o, path), " ")
	case file.Status == "?" || o.Rev == "":
		return a, a.fromContent(newSide(ctx, cmd, o, file.Path), "+")
	case file.Status == "D":
		return a, a.fromContent(func() (*Content, error) { return ReadAt(ctx, cmd, o.Root, o.Rev, file.Path) }, "-")
	}

	args := []string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U" + strconv.Itoa(fullContext), o.Rev}
	if o.Head != "" {
		args = append(args, o.Head)
	}
	args = append(args, "--")
	if file.OldPath != "" {
		if _, err := Resolve(o.Root, file.OldPath); err != nil {
			return nil, err
		}
		args = append(args, file.OldPath)
	}
	out, err := git(ctx, cmd, o.Root, append(args, file.Path)...)
	if err != nil {
		return nil, err
	}
	if len(out) > maxDiffBytes {
		a.TooLarge = true
		return a, nil
	}
	rows, binary := parseFullDiff(out)
	switch {
	case binary:
		a.Binary = true
	case rows == nil:
		// No hunk: a pure rename or a mode change. The lines are the same
		// on both sides.
		return a, a.fromContent(newSide(ctx, cmd, o, file.Path), " ")
	default:
		a.Rows = rows
	}
	return a, nil
}

// newSide reads path as it is in the change's new version.
func newSide(ctx context.Context, cmd moexec.Commander, o *Overview, path string) func() (*Content, error) {
	return func() (*Content, error) {
		if o.Head != "" {
			return ReadAt(ctx, cmd, o.Root, o.Head, path)
		}
		return ReadFile(o.Root, path)
	}
}

// fromContent fills a's rows with every line of the version read gives,
// all marked sign.
func (a *Annotated) fromContent(read func() (*Content, error), sign string) error {
	c, err := read()
	if err != nil {
		return err
	}
	if c.TooLarge {
		a.TooLarge = true
		return nil
	}
	if c.Binary {
		a.Binary = true
		return nil
	}
	a.Rows = []Row{}
	for i, line := range splitLines(c.Text) {
		r := Row{Sign: sign, Text: line}
		if sign != "-" {
			r.Ln = i + 1
		}
		if sign != "+" {
			r.OldLn = i + 1
		}
		a.Rows = append(a.Rows, r)
	}
	return nil
}

// splitLines splits text into lines, without a last empty one for a
// trailing newline.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// parseFullDiff reads one file's `git diff` output into rows. binary is
// set for git's "Binary files … differ". Rows is nil when there's no hunk.
func parseFullDiff(out []byte) (rows []Row, binary bool) {
	inHunk := false
	oldLn, ln := 0, 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		if !inHunk || bytes.HasPrefix(line, []byte("@@ ")) {
			switch {
			case bytes.HasPrefix(line, []byte("Binary files ")) || bytes.HasPrefix(line, []byte("GIT binary patch")):
				return nil, true
			case bytes.HasPrefix(line, []byte("@@ ")):
				o, n, ok := parseHunkHeader(string(line))
				if !ok {
					continue
				}
				inHunk, oldLn, ln = true, o, n
				if rows == nil {
					rows = []Row{}
				}
			}
			continue
		}
		if len(line) == 0 {
			// The split's last element, or a stray blank: real blank
			// lines carry their " " marker.
			continue
		}
		text := string(line[1:])
		switch line[0] {
		case ' ':
			rows = append(rows, Row{Ln: ln, OldLn: oldLn, Sign: " ", Text: text})
			ln++
			oldLn++
		case '+':
			rows = append(rows, Row{Ln: ln, Sign: "+", Text: text})
			ln++
		case '-':
			rows = append(rows, Row{OldLn: oldLn, Sign: "-", Text: text})
			oldLn++
		case '\\':
			// "\ No newline at end of file"
		default:
			// The next file's header (never with a single path).
			inHunk = false
		}
	}
	return rows, false
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@" into each side's first line
// number. An empty side ("-0,0") starts at 1 for the lines that follow.
func parseHunkHeader(h string) (oldStart, newStart int, ok bool) {
	f := strings.Fields(h)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, false
	}
	start := func(s string) (int, bool) {
		s, _, _ = strings.Cut(s[1:], ",")
		n, err := strconv.Atoi(s)
		return max(n, 1), err == nil
	}
	o, ok1 := start(f[1])
	n, ok2 := start(f[2])
	return o, n, ok1 && ok2
}

// Cut returns the rows from line through end (0 means line) on side,
// widened by ctx rows each way and capped at MaxExcerptRows. On SideNew
// line numbers are the new version's (Ln), on SideOld the old one's
// (OldLn). A line that isn't there gives no rows.
func (a *Annotated) Cut(line, end int, side string, ctx int) *Excerpt {
	e := &Excerpt{Path: a.Path, Side: side, Binary: a.Binary, TooLarge: a.TooLarge, Rows: []Row{}}
	if end < line {
		end = line
	}
	num := func(r Row) int {
		if side == SideOld {
			return r.OldLn
		}
		return r.Ln
	}
	first, last := -1, -1
	for i, r := range a.Rows {
		n := num(r)
		if n >= line && n <= end {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return e
	}
	from, to := max(first-ctx, 0), min(last+ctx, len(a.Rows)-1)
	if to-from+1 > MaxExcerptRows {
		to, e.Truncated = from+MaxExcerptRows-1, true
	}
	e.Rows = append(e.Rows, a.Rows[from:to+1]...)
	return e
}

// Stamp identifies the working-tree version of rel under root without
// reading it: its size and modification time, or "missing". A cached
// annotation of a working-tree file is keyed by it, so an edit is never
// served stale.
func Stamp(root, rel string) string {
	full, err := Resolve(root, rel)
	if err != nil {
		return "unresolved"
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}
