package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// JavaScript/TypeScript scanning for the Node analyzer. jsCode blanks
// comments, template literals and regex literals, but keeps plain strings
// (import specifiers are strings), recording where each one starts so an
// "import" written inside a string isn't read as one.

// jsScanned is a source with non-code blanked, plus the spans of its
// plain string literals.
type jsScanned struct {
	code    string
	strings [][2]int // [start, end) of each '…'/"…" literal, in order
}

// inString reports whether offset i falls inside a string literal.
func (s *jsScanned) inString(i int) bool {
	k := sort.Search(len(s.strings), func(k int) bool { return s.strings[k][1] > i })
	return k < len(s.strings) && s.strings[k][0] <= i
}

// regexAfterWords are keywords after which a "/" starts a regex literal.
var regexAfterWords = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true,
}

func jsCode(src string) *jsScanned {
	b := []byte(src)
	n := len(b)
	out := &jsScanned{}
	// prev is the last significant byte, word the last identifier: they
	// decide whether "/" is division or a regex.
	var prev byte
	word := ""
	for i := 0; i < n; {
		c := b[i]
		switch {
		case c == '/' && i+1 < n && b[i+1] == '/':
			j := i
			for j < n && b[j] != '\n' {
				j++
			}
			blank(b, i, j)
			i = j
		case c == '/' && i+1 < n && b[i+1] == '*':
			end := strings.Index(string(b[i+2:]), "*/")
			j := n
			if end >= 0 {
				j = i + 2 + end + 2
			}
			blank(b, i, j)
			i = j
		case c == '\'' || c == '"':
			j := i + 1
			for j < n && b[j] != c && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			out.strings = append(out.strings, [2]int{i, min(n, j+1)})
			i = j + 1
			prev, word = 'a', ""
		case c == '`':
			j := skipTemplate(b, i+1)
			blank(b, i, j)
			i = j
			prev, word = 'a', ""
		case c == '/' && startsRegex(prev, word):
			j := i + 1
			inClass := false
			for j < n && b[j] != '\n' {
				if b[j] == '\\' {
					j += 2
					continue
				}
				if b[j] == '[' {
					inClass = true
				} else if b[j] == ']' {
					inClass = false
				} else if b[j] == '/' && !inClass {
					break
				}
				j++
			}
			j++
			for j < n && (b[j] >= 'a' && b[j] <= 'z') { // flags
				j++
			}
			blank(b, i, min(n, j))
			i = j
			prev, word = 'a', ""
		case isIdentByte(c):
			j := i
			for j < n && isIdentByte(b[j]) {
				j++
			}
			word = string(b[i:j])
			prev = 'a'
			i = j
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prev, word = c, ""
			}
			i++
		}
	}
	out.code = string(b)
	return out
}

// skipTemplate returns the end of a template literal starting after its
// opening backtick, skipping ${…} substitutions (which may nest templates).
func skipTemplate(b []byte, i int) int {
	n := len(b)
	for i < n {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '`':
			return i + 1
		case '$':
			if i+1 < n && b[i+1] == '{' {
				depth := 1
				i += 2
				for i < n && depth > 0 {
					switch b[i] {
					case '{':
						depth++
					case '}':
						depth--
					case '`':
						i = skipTemplate(b, i+1) - 1
					case '\'', '"':
						q := b[i]
						i++
						for i < n && b[i] != q {
							if b[i] == '\\' {
								i++
							}
							i++
						}
					}
					i++
				}
				continue
			}
		}
		i++
	}
	return n
}

func startsRegex(prev byte, word string) bool {
	if word != "" {
		return regexAfterWords[word]
	}
	return prev == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", prev) >= 0
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

var jsImportRes = []*regexp.Regexp{
	// import x from '…', import {a, b} from '…', import type X from '…', import '…'
	regexp.MustCompile(`\bimport\s+(?:type\s+)?(?:[\w$*{}\s,]+?\s+from\s+)?(['"])([^'"\n]+)['"]`),
	// export {a} from '…', export * from '…', export * as ns from '…'
	regexp.MustCompile(`\bexport\s+(?:type\s+)?(?:\*(?:\s+as\s+[\w$]+)?|\{[^}]*\})\s+from\s+(['"])([^'"\n]+)['"]`),
	// import('…'), require('…')
	regexp.MustCompile(`\b(?:import|require)\s*\(\s*(['"])([^'"\n]+)['"]\s*\)`),
}

// jsImports lists the module specifiers a scanned source imports, with
// their lines. A match that starts inside a string literal isn't one.
func jsImports(s *jsScanned) []ref {
	var out []ref
	for _, re := range jsImportRes {
		for _, m := range re.FindAllStringSubmatchIndex(s.code, -1) {
			if s.inString(m[0]) {
				continue
			}
			out = append(out, ref{to: s.code[m[4]:m[5]], line: 1 + strings.Count(s.code[:m[0]], "\n")})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

var scriptBlockRe = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

// componentScript keeps the script parts of a .vue, .svelte or .astro file
// (Astro's --- frontmatter too), blanking the markup so lines stay right.
func componentScript(p, src string) string {
	b := []byte(src)
	keep := make([]bool, len(b))
	for _, m := range scriptBlockRe.FindAllStringSubmatchIndex(src, -1) {
		for i := m[2]; i < m[3]; i++ {
			keep[i] = true
		}
	}
	if path.Ext(p) == ".astro" && strings.HasPrefix(src, "---") {
		if end := strings.Index(src[3:], "\n---"); end >= 0 {
			for i := 3; i < 3+end; i++ {
				keep[i] = true
			}
		}
	}
	for i := range b {
		if !keep[i] && b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}
