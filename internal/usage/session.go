package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// sessionCacheEntry is keyed by full JSONL path. size is the file size at
// parse time; if it hasn't changed since the last parse, we skip re-parsing
// (JSONL is append-only, so size is a sufficient invalidation signal).
type sessionCacheEntry struct {
	size   int64
	tokens int
}

var (
	sessionCacheMu sync.Mutex
	sessionCache   = map[string]sessionCacheEntry{}
)

// SessionTokens returns the current context footprint of a Claude session
// JSONL: input + cache_read + cache_creation of the last assistant turn —
// i.e. the size of what would be sent on the next request. Returns 0 for
// missing files or files with no usage data yet.
//
// The JSONL is append-only across /compact, so a naive sum would grow
// monotonically and double-count cached prefixes. Using the last turn's
// input side tracks what Claude Code itself considers "context used" and
// drops correctly after compaction. If the most recent sizing event in
// the file is a compact_boundary (compact just ran, no new turn yet), we
// use its postTokens as the footprint.
//
// Safe to call every tick — the size-keyed cache skips re-parsing when the
// file hasn't grown.
func SessionTokens(jsonlPath string) int {
	st, err := os.Stat(jsonlPath)
	if err != nil {
		return 0
	}

	sessionCacheMu.Lock()
	e, ok := sessionCache[jsonlPath]
	sessionCacheMu.Unlock()
	if ok && e.size == st.Size() {
		return e.tokens
	}

	total := parseSessionTokens(jsonlPath)

	sessionCacheMu.Lock()
	sessionCache[jsonlPath] = sessionCacheEntry{size: st.Size(), tokens: total}
	sessionCacheMu.Unlock()
	return total
}

type sessionTurn struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	CompactMetadata struct {
		PostTokens int `json:"postTokens"`
	} `json:"compactMetadata"`
}

func parseSessionTokens(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	// JSONL lines can hold whole tool results; bump the buffer generously.
	s.Buffer(make([]byte, 64*1024), 16*1024*1024)

	current := 0
	for s.Scan() {
		var t sessionTurn
		if err := json.Unmarshal(s.Bytes(), &t); err != nil {
			continue
		}
		switch {
		case t.Type == "assistant":
			u := t.Message.Usage
			current = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		case t.Type == "system" && t.Subtype == "compact_boundary":
			current = t.CompactMetadata.PostTokens
		}
	}
	return current
}
