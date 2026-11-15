package review

import "strings"

type rtTokKind byte

const (
	rtIdent rtTokKind = iota + 1 // a name, keyword or constant
	rtSym                        // :name
	rtLabel                      // name:
	rtStr                        // "…" (text is the contents)
	rtWords                      // %w[] / %i[] (words in list)
	rtRegex                      // /…/, %r{}
	rtNum
	rtIvar // @x, $x
	rtOp   // punctuation and operators
	rtNL   // end of line
)

type rtTok struct {
	kind  rtTokKind
	text  string
	words []string
	sym   bool // rtWords: %i (symbols)
	line  int
	space bool // whitespace before it
}

// rtTokens splits routes source into tokens. Comments, =begin blocks and
// heredoc bodies are dropped; a string keeps its contents as written
// (interpolation included).
func rtTokens(src string) []rtTok {
	var out []rtTok
	b := []byte(src)
	n := len(b)
	line := 1
	space := true
	var heredocs []string // terminators to skip after the line
	emit := func(k rtTokKind, text string) {
		out = append(out, rtTok{kind: k, text: text, line: line, space: space})
		space = false
	}
	for i := 0; i < n; {
		c := b[i]
		if (i == 0 || b[i-1] == '\n') && strings.HasPrefix(src[i:], "=begin") {
			end := strings.Index(src[i:], "\n=end")
			if end < 0 {
				break
			}
			line += strings.Count(src[i:i+end+1], "\n")
			i += end + len("\n=end")
			for i < n && b[i] != '\n' {
				i++
			}
			continue
		}
		switch {
		case c == '\n':
			emit(rtNL, "")
			line++
			i++
			space = true
			for len(heredocs) > 0 {
				term := heredocs[0]
				heredocs = heredocs[1:]
				for i < n {
					j := strings.IndexByte(src[i:], '\n')
					l := src[i:]
					if j >= 0 {
						l = src[i : i+j]
					}
					i += len(l)
					if i < n {
						i++
						line++
					}
					if strings.TrimSpace(l) == term {
						break
					}
				}
			}
		case c == ' ' || c == '\t' || c == '\r':
			space = true
			i++
		case c == '\\' && i+1 < n && b[i+1] == '\n':
			i += 2 // a line continuation
			line++
			space = true
		case c == '#':
			for i < n && b[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			j := rtStringEnd(b, i+1, c)
			text := src[i+1 : min(j, n)]
			if c == '"' {
				text = strings.ReplaceAll(text, `\"`, `"`)
			} else {
				text = strings.ReplaceAll(text, `\'`, `'`)
			}
			emit(rtStr, text)
			line += strings.Count(src[i:min(j+1, n)], "\n")
			i = j + 1
			// "key": value
			if i < n && b[i] == ':' && (i+1 >= n || b[i+1] != ':') {
				out[len(out)-1].kind = rtLabel
				i++
			}
		case c == '%' && i+2 < n && strings.IndexByte("wWiIqQr", b[i+1]) >= 0 && strings.IndexByte("([{<|!/^", b[i+2]) >= 0:
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
				if depth > 0 {
					j++
				}
			}
			body := src[i+3 : min(j, n)]
			switch b[i+1] {
			case 'w', 'W', 'i', 'I':
				out = append(out, rtTok{kind: rtWords, words: strings.Fields(body), sym: b[i+1] == 'i' || b[i+1] == 'I', line: line, space: space})
				space = false
			case 'r':
				emit(rtRegex, body)
			default:
				emit(rtStr, body)
			}
			line += strings.Count(src[i:min(j+1, n)], "\n")
			i = j + 1
		case c == '/' && regexpStart(b, i):
			j := i + 1
			for j < n && b[j] != '/' && b[j] != '\n' {
				if b[j] == '\\' && j+1 < n && b[j+1] != '\n' {
					j++
				}
				j++
			}
			if j < n && b[j] == '/' {
				emit(rtRegex, src[i+1:j])
				i = j + 1
				for i < n && isWordByte(b[i]) {
					i++ // flags
				}
				continue
			}
			emit(rtOp, "/")
			i++
		case c == ':' && i+1 < n && (b[i+1] == '"' || b[i+1] == '\''):
			j := rtStringEnd(b, i+2, b[i+1])
			emit(rtSym, src[i+2:min(j, n)])
			i = j + 1
		case c == ':' && i+1 < n && (isWordByte(b[i+1]) && !(b[i+1] >= '0' && b[i+1] <= '9')) && (i == 0 || b[i-1] != ':'):
			j := i + 1
			for j < n && isWordByte(b[j]) {
				j++
			}
			if j < n && (b[j] == '?' || b[j] == '!' || b[j] == '=') && (j+1 >= n || b[j+1] != '>' && b[j+1] != '=') {
				j++
			}
			emit(rtSym, src[i+1:j])
			i = j
		case c == '<' && i+2 < n && b[i+1] == '<' && heredocRe.Match(b[i:]):
			m := heredocRe.FindSubmatch(b[i:])
			heredocs = append(heredocs, string(m[3]))
			emit(rtStr, "")
			i += len(m[0])
		case c == '@' || c == '$':
			j := i + 1
			for j < n && (isWordByte(b[j]) || b[j] == '@') {
				j++
			}
			emit(rtIvar, src[i:j])
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < n && (isWordByte(b[j]) || b[j] == '.' && j+1 < n && b[j+1] >= '0' && b[j+1] <= '9') {
				j++
			}
			emit(rtNum, src[i:j])
			i = j
		case isWordByte(c):
			j := i
			for j < n && isWordByte(b[j]) {
				j++
			}
			if j < n && (b[j] == '?' || b[j] == '!') && (j+1 >= n || b[j+1] != '=') {
				j++
			}
			word := src[i:j]
			if j < n && b[j] == ':' && (j+1 >= n || b[j+1] != ':') {
				emit(rtLabel, word)
				i = j + 1
				continue
			}
			emit(rtIdent, word)
			i = j
		default:
			op := string(c)
			for _, o := range []string{"=>", "->", "::", "||", "&&", "==", "!=", "<=", ">=", "**", "&.", "+=", "-=", "||=", "<<", "..."} {
				if strings.HasPrefix(src[i:], o) && len(o) > len(op) {
					op = o
				}
			}
			emit(rtOp, op)
			i += len(op)
		}
	}
	emit(rtNL, "")
	return out
}

// rtStringEnd is the index of the quote closing a string that starts at
// i (just past the opening quote), skipping escapes and, in a "…" string,
// #{…} interpolation.
func rtStringEnd(b []byte, i int, q byte) int {
	for i < len(b) {
		switch {
		case b[i] == '\\':
			i += 2
			continue
		case b[i] == q:
			return i
		case q == '"' && b[i] == '#' && i+1 < len(b) && b[i+1] == '{':
			depth := 0
			for i < len(b) {
				if b[i] == '{' {
					depth++
				} else if b[i] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
				i++
			}
		}
		i++
	}
	return len(b)
}
