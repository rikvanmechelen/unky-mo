package review

import (
	"regexp"
	"strings"
)

// Swift scanning: swiftCode blanks comments (they nest), strings
// ("…" with \( ) interpolation, """multi-line""", #"raw"#) and keeps line
// numbers.
func swiftCode(src string) string {
	b := []byte(src)
	n := len(b)
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
			depth, j := 1, i+2
			for j < n && depth > 0 {
				if b[j] == '/' && j+1 < n && b[j+1] == '*' {
					depth++
					j++
				} else if b[j] == '*' && j+1 < n && b[j+1] == '/' {
					depth--
					j++
				}
				j++
			}
			blank(b, i, j)
			i = j
		case c == '#' && i+1 < n && (b[i+1] == '"' || b[i+1] == '#'):
			// Raw string: #"…"#, ##"…"##, with optional """.
			k := i
			for k < n && b[k] == '#' {
				k++
			}
			hashes := k - i
			if k >= n || b[k] != '"' {
				i = k
				continue
			}
			closer := "\"" + strings.Repeat("#", hashes)
			if strings.HasPrefix(string(b[k:]), `"""`) {
				closer = `"""` + strings.Repeat("#", hashes)
			}
			end := strings.Index(string(b[k+1:]), closer)
			j := n
			if end >= 0 {
				j = k + 1 + end + len(closer)
			}
			blank(b, i, j)
			i = j
		case c == '"' && i+2 < n && b[i+1] == '"' && b[i+2] == '"':
			end := strings.Index(string(b[i+3:]), `"""`)
			j := n
			if end >= 0 {
				j = i + 3 + end + 3
			}
			blank(b, i, j)
			i = j
		case c == '"':
			j := i + 1
			for j < n && b[j] != '"' && b[j] != '\n' {
				if b[j] == '\\' {
					j++
					if j < n && b[j] == '(' { // \( … ) interpolation: skip to its close
						depth := 1
						for j++; j < n && depth > 0 && b[j] != '\n'; j++ {
							if b[j] == '(' {
								depth++
							} else if b[j] == ')' {
								depth--
							}
						}
						continue
					}
				}
				j++
			}
			blank(b, i, min(n, j+1))
			i = j + 1
		default:
			i++
		}
	}
	return string(b)
}

var (
	swiftDeclRe   = regexp.MustCompile(`(?m)\b(?:class|struct|enum|protocol|actor|typealias)\s+([A-Z][A-Za-z0-9_]*)`)
	swiftImportRe = regexp.MustCompile(`(?m)^\s*(@testable\s+)?import\s+(?:(?:class|struct|enum|protocol|func|var|let|typealias)\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	swiftTypeRe   = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)
)

// swiftDecls lists the type names a Swift file declares (classes,
// structs, enums, protocols, actors, typealiases, nested ones too).
func swiftDecls(src string) []string {
	var out []string
	for _, m := range swiftDeclRe.FindAllStringSubmatch(swiftCode(src), -1) {
		out = append(out, m[1])
	}
	return out
}

// swiftScanned is a file's raw references: module imports and the
// capitalized names it uses.
type swiftScanned struct {
	Imports []ref `json:"imports"`
	Types   []ref `json:"types"`
}

func swiftRefs(src string) swiftScanned {
	code := swiftCode(src)
	var s swiftScanned
	lineOf := func(i int) int { return 1 + strings.Count(code[:i], "\n") }
	for _, m := range swiftImportRe.FindAllStringSubmatchIndex(code, -1) {
		if m[2] >= 0 {
			continue // @testable import: tests only
		}
		s.Imports = append(s.Imports, ref{to: code[m[4]:m[5]], line: lineOf(m[0])})
	}
	seen := map[string]bool{}
	for _, m := range swiftTypeRe.FindAllStringIndex(code, -1) {
		name := code[m[0]:m[1]]
		if seen[name] {
			continue // one reference per name per file is enough
		}
		seen[name] = true
		s.Types = append(s.Types, ref{to: name, line: lineOf(m[0])})
	}
	return s
}
