package review

import (
	"strings"
	"testing"
)

func TestLineIndexer(t *testing.T) {
	for _, s := range []string{"", "a", "\n", "a\nb", "a\nb\n", "\n\nx\n\n"} {
		lineAt := lineIndexer(s)
		for off := 0; off <= len(s); off++ {
			if want, got := 1+strings.Count(s[:off], "\n"), lineAt(off); got != want {
				t.Errorf("%q at %d: got %d, want %d", s, off, got, want)
			}
		}
	}
}
