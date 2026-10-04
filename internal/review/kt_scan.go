package review

import (
	"regexp"
	"strings"
)

// Kotlin/Java scanning: jvmCode blanks comments (Kotlin's nest), strings
// (including Kotlin's """raw""" strings and ${} templates) and char
// literals, keeping line numbers.
func jvmCode(src string) string { return jvmScan(src, false) }

// jvmScan blanks comments, and strings unless keepStrings (annotation
// arguments such as Retrofit paths are strings).
func jvmScan(src string, keepStrings bool) string {
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
		case c == '"' && i+2 < n && b[i+1] == '"' && b[i+2] == '"':
			end := strings.Index(string(b[i+3:]), `"""`)
			j := n
			if end >= 0 {
				j = i + 3 + end + 3
			}
			if !keepStrings {
				blank(b, i, j)
			}
			i = j
		case c == '"' || c == '\'':
			j := i + 1
			for j < n && b[j] != c && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			if !keepStrings {
				blank(b, i, min(n, j+1))
			}
			i = j + 1
		default:
			i++
		}
	}
	return string(b)
}

var (
	jvmPackageRe = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)`)
	jvmImportRe  = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([\w.]+?)(\.\*)?\s*(?:as\s+\w+)?\s*;?\s*$`)
	// A qualified name in code: lowercase package segments, then a type.
	jvmQualifiedRe = regexp.MustCompile(`\b([a-z][\w]*(?:\.[a-z][\w]*)+)\.[A-Z]`)
)

// jvmPackage reads a Kotlin/Java file's package.
func jvmPackage(src string) string {
	if m := jvmPackageRe.FindStringSubmatch(jvmCode(src)); m != nil {
		return m[1]
	}
	return ""
}

// jvmRef is a raw reference: a dotted name and whether it names a package
// (a wildcard import) rather than a type or member.
type jvmRef struct {
	Name    string `json:"name"`
	Package bool   `json:"package,omitempty"`
	Line    int    `json:"line"`
}

// jvmRefs lists a file's imports and qualified names.
func jvmRefs(src string) []jvmRef {
	code := jvmCode(src)
	var out []jvmRef
	lineOf := func(i int) int { return 1 + strings.Count(code[:i], "\n") }
	for _, m := range jvmImportRe.FindAllStringSubmatchIndex(code, -1) {
		out = append(out, jvmRef{Name: code[m[2]:m[3]], Package: m[4] >= 0, Line: lineOf(m[0])})
	}
	for _, m := range jvmQualifiedRe.FindAllStringSubmatchIndex(code, -1) {
		line := strings.TrimSpace(code[strings.LastIndexByte(code[:m[0]], '\n')+1 : m[0]])
		if strings.HasPrefix(line, "import") || strings.HasPrefix(line, "package") {
			continue
		}
		out = append(out, jvmRef{Name: code[m[2]:m[3]], Package: true, Line: lineOf(m[0])})
	}
	return out
}
