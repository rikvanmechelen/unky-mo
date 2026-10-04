package review

import (
	"regexp"
	"strings"
)

// Python scanning for the Python analyzer: pyCode blanks comments and
// string literals (keeping newlines), then pyImports reads the import
// statements of each logical line, skipping the block under
// `if TYPE_CHECKING:` (type-only imports aren't runtime dependencies).

// pyCode returns src with comments and strings (single, triple-quoted,
// with r/b/f/u prefixes) blanked.
func pyCode(src string) string {
	b := []byte(src)
	n := len(b)
	for i := 0; i < n; {
		c := b[i]
		switch {
		case c == '#':
			for i < n && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		case c == '\'' || c == '"':
			q := c
			triple := i+2 < n && b[i+1] == q && b[i+2] == q
			j := i + 1
			if triple {
				j = i + 3
				for j < n && !(b[j] == q && j+2 < n && b[j+1] == q && b[j+2] == q) {
					if b[j] == '\\' {
						j++
					}
					j++
				}
				j += 3
			} else {
				for j < n && b[j] != q && b[j] != '\n' {
					if b[j] == '\\' {
						j++
					}
					j++
				}
				j++
			}
			blank(b, i, min(n, j))
			i = j
		default:
			i++
		}
	}
	return string(b)
}

// pyImport is one imported module: dots for a relative import's level,
// the dotted module path, the names a from-import takes from it, and the
// statement's line.
type pyImport struct {
	Level  int      `json:"level"`
	Module string   `json:"module"`
	Names  []string `json:"names,omitempty"`
	Line   int      `json:"line"`
}

var (
	pyImportRe = regexp.MustCompile(`^\s*import\s+(.+)$`)
	pyFromRe   = regexp.MustCompile(`^\s*from\s+(\.*)([\w.]*)\s+import\s+(.+)$`)
	pyTypeCk   = regexp.MustCompile(`^(\s*)if\s+(?:typing\.)?TYPE_CHECKING\s*:\s*$`)
)

// pyImports lists the imports of a Python source.
func pyImports(src string) []pyImport {
	code := pyCode(src)
	lines := strings.Split(code, "\n")
	var out []pyImport
	skipIndent := -1 // inside an `if TYPE_CHECKING:` block indented deeper than this
	for i := 0; i < len(lines); i++ {
		start := i
		line := lines[i]
		// Join a logical line: open brackets or a trailing backslash.
		for depth(line) > 0 || strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") {
			if i+1 >= len(lines) {
				break
			}
			line = strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\") + " " + lines[i+1]
			i++
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if skipIndent >= 0 {
			if indent > skipIndent {
				continue
			}
			skipIndent = -1
		}
		if m := pyTypeCk.FindStringSubmatch(line); m != nil {
			skipIndent = len(m[1])
			continue
		}
		if m := pyFromRe.FindStringSubmatch(line); m != nil {
			names := strings.Split(strings.Trim(strings.TrimSpace(m[3]), "()"), ",")
			var clean []string
			for _, n := range names {
				n = strings.TrimSpace(n)
				if f := strings.Fields(n); len(f) > 0 && f[0] != "*" {
					clean = append(clean, f[0])
				}
			}
			out = append(out, pyImport{Level: len(m[1]), Module: m[2], Names: clean, Line: start + 1})
			continue
		}
		if m := pyImportRe.FindStringSubmatch(line); m != nil {
			for _, part := range strings.Split(m[1], ",") {
				if f := strings.Fields(part); len(f) > 0 {
					out = append(out, pyImport{Module: f[0], Line: start + 1})
				}
			}
		}
	}
	return out
}

// depth is how many brackets a line leaves open.
func depth(s string) int {
	d := 0
	for _, c := range s {
		switch c {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
		}
	}
	return d
}
