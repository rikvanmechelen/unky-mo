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

// SessionTokens returns the cumulative token count (input + output + cache
// reads + cache creation) across every assistant turn in a Claude session
// JSONL file. Returns 0 for missing files or files with no usage data.
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
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
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

	total := 0
	for s.Scan() {
		var t sessionTurn
		if err := json.Unmarshal(s.Bytes(), &t); err != nil {
			continue
		}
		if t.Type != "assistant" {
			continue
		}
		u := t.Message.Usage
		total += u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	return total
}
