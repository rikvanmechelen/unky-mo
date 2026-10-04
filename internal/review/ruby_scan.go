package review

import (
	"regexp"
	"strings"
)

// Ruby source scanning for the Rails analyzer: rubyCode blanks everything
// that isn't code (comments, strings, heredocs), so constants mentioned in
// them aren't read as references. Blanking keeps every newline, so line
// numbers stay right.

// blank replaces b[i:j] with spaces, keeping newlines.
func blank(b []byte, i, j int) {
	for ; i < j && i < len(b); i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
}

var heredocRe = regexp.MustCompile(`^<<([~-]?)(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// rubyCode returns src with comments, =begin/=end blocks, string literals
// ('…', "…", `…`, %w[] %i[] %q() %Q{} and the like) and heredoc bodies
// blanked out.
func rubyCode(src string) string {
	b := []byte(src)
	n := len(b)
	type heredoc struct {
		id     string
		indent bool // <<~ / <<- : the terminator may be indented
	}
	var pending []heredoc
	lineStart := true
	for i := 0; i < n; {
		c := b[i]
		if lineStart {
			lineStart = false
			// =begin … =end comment blocks start at column 0.
			if strings.HasPrefix(string(b[i:min(n, i+6)]), "=begin") {
				end := strings.Index(string(b[i:]), "\n=end")
				if end < 0 {
					blank(b, i, n)
					break
				}
				stop := i + end + len("\n=end")
				for stop < n && b[stop] != '\n' {
					stop++
				}
				blank(b, i, stop)
				i = stop
				continue
			}
		}
		switch {
		case c == '\n':
			i++
			lineStart = true
			// Heredoc bodies start on the line after their opener.
			for len(pending) > 0 {
				h := pending[0]
				pending = pending[1:]
				for i < n {
					end := strings.IndexByte(string(b[i:]), '\n')
					line := ""
					if end < 0 {
						line, end = string(b[i:]), n-i
					} else {
						line = string(b[i : i+end])
					}
					term := line == h.id || (h.indent && strings.TrimSpace(line) == h.id)
					blank(b, i, i+end) // the body, and the terminator (an identifier)
					i += end
					if i < n {
						i++ // the newline
					}
					if term {
						break
					}
				}
			}
		case c == '#':
			for i < n && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < n && b[j] != c {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(b, i, min(n, j+1))
			i = j + 1
		case c == '%' && i+2 < n && strings.IndexByte("qQwWiIrsx", b[i+1]) >= 0 && strings.IndexByte("([{<|!/^", b[i+2]) >= 0:
			open, closer := b[i+2], closingDelim(b[i+2])
			depth, j := 1, i+3
			for j < n && depth > 0 {
				switch {
				case b[j] == '\\':
					j++
				case b[j] == closer && open != closer:
					depth--
				case b[j] == open && open != closer:
					depth++
				case b[j] == closer:
					depth = 0
				}
				j++
			}
			blank(b, i, j)
			i = j
		case c == '<' && i+1 < n && b[i+1] == '<':
			if m := heredocRe.FindSubmatch(b[i:]); m != nil {
				pending = append(pending, heredoc{id: string(m[3]), indent: len(m[1]) > 0})
				blank(b, i, i+len(m[0]))
				i += len(m[0])
				continue
			}
			i += 2
		default:
			i++
		}
	}
	return string(b)
}

func closingDelim(open byte) byte {
	switch open {
	case '(':
		return ')'
	case '[':
		return ']'
	case '{':
		return '}'
	case '<':
		return '>'
	}
	return open
}

// erbCode keeps only the Ruby inside <% %> and <%= %> tags (not <%# %>
// comments) of an ERB template, as Ruby code.
func erbCode(src string) string {
	b := []byte(src)
	out := make([]byte, len(b))
	for i := range b {
		out[i] = ' '
		if b[i] == '\n' {
			out[i] = '\n'
		}
	}
	for i := 0; i < len(b); {
		start := strings.Index(string(b[i:]), "<%")
		if start < 0 {
			break
		}
		start += i + 2
		end := strings.Index(string(b[start:]), "%>")
		if end < 0 {
			end = len(b) - start
		}
		if start < len(b) && b[start] != '#' {
			copy(out[start:start+end], b[start:start+end])
		}
		i = start + end + 2
	}
	return rubyCode(string(out))
}

// hamlCode keeps a HAML or Slim template's code lines: the Ruby after a
// leading "-" or "=" (also after a tag, as in "%p= x" or "p = x").
var hamlCodeRe = regexp.MustCompile(`^\s*(?:[%.#]?[\w.#-]*\s*)?[-=]\s`)

func hamlCode(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if m := hamlCodeRe.FindStringIndex(l); m != nil {
			lines[i] = strings.Repeat(" ", m[1]) + l[m[1]:]
		} else {
			lines[i] = strings.Repeat(" ", len(l))
		}
	}
	return rubyCode(strings.Join(lines, "\n"))
}

// constantRe finds Ruby constant references (Foo, Foo::Bar, ::Foo) in
// scanned code. The leading context rules out method calls (x.Foo),
// instance/global variables and symbols (:Foo).
var constantRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_:.@$])((?:::)?[A-Z][A-Za-z0-9_]*(?:::[A-Z][A-Za-z0-9_]*)*)`)

// rubyConstants lists the constants code references, with their lines.
func rubyConstants(code string) []ref {
	var out []ref
	for _, m := range constantRe.FindAllStringSubmatchIndex(code, -1) {
		out = append(out, ref{to: code[m[2]:m[3]], line: 1 + strings.Count(code[:m[2]], "\n")})
	}
	return out
}
