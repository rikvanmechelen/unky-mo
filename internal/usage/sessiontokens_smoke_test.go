package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionTokensSmoke dumps the token total for every JSONL in a chosen
// project dir. Skipped by default. Usage:
//
//	SESSION_TOKENS_DIR=~/.claude/projects/-home-rvanmech-workspace-unky-mo \
//	  go test -run TestSessionTokensSmoke -v ./internal/usage/...
func TestSessionTokensSmoke(t *testing.T) {
	dir := os.Getenv("SESSION_TOKENS_DIR")
	if dir == "" {
		t.Skip("set SESSION_TOKENS_DIR to a ~/.claude/projects/* directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		n := SessionTokens(path)
		fmt.Printf("%s  tokens=%d  fmt=%s\n", e.Name(), n, FormatTokensShort(n))
	}
}
