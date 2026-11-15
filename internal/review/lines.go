package review

import (
	"sort"
	"strings"
)

// lineIndexer returns lineAt(off): the 1-based line of byte offset off in
// s. It indexes the newlines once, so a scan that asks for the line of
// every match stays linear; counting the newlines before each match was
// quadratic on a file full of matches.
func lineIndexer(s string) func(off int) int {
	var nl []int
	for i := 0; ; {
		j := strings.IndexByte(s[i:], '\n')
		if j < 0 {
			break
		}
		nl = append(nl, i+j)
		i += j + 1
	}
	return func(off int) int {
		// Newlines before off, plus one.
		return 1 + sort.SearchInts(nl, off)
	}
}
