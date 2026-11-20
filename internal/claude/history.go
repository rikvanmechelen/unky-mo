package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// PromptHistoryMax is how many prompts PromptHistory returns, as many as
// Claude Code's own up-arrow history goes back.
const PromptHistoryMax = 100

// promptHistoryTail is how much of the end of history.jsonl is read. The
// file holds every prompt from every project and only grows; a project's
// last 100 prompts are near its end.
const promptHistoryTail = 4 << 20

// historyEntry is one line of ~/.claude/history.jsonl, where Claude Code
// records each prompt typed into it (probed on 2.1.325).
type historyEntry struct {
	Display        string                   `json:"display"`
	PastedContents map[string]pastedContent `json:"pastedContents"`
	Timestamp      int64                    `json:"timestamp"`
	Project        string                   `json:"project"`
	SessionID      string                   `json:"sessionId"`
}

// pastedContent is a paste the prompt names as "[Pasted text #N …]". A
// large one only has a contentHash, its text no longer in the file.
type pastedContent struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// pastedRef matches the placeholder Claude Code puts in a prompt for a
// paste: "[Pasted text #1]" or "[Pasted text #1 +70 lines]".
var pastedRef = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)

// HistoryFile is Claude Code's prompt history.
func HistoryFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "history.jsonl")
}

// PromptHistory returns the prompts typed into Claude Code in the checkout
// at cwd, newest first, as its up arrow offers them: the session's own
// first, then the rest of the project's. Pastes are put back in where
// their text was kept; a repeated prompt is listed once, at its newest.
// A missing or unreadable file is no history.
func PromptHistory(file, cwd, sessionID string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	return promptHistory(f, cwd, sessionID)
}

func promptHistory(f io.ReadSeeker, cwd, sessionID string) []string {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil
	}
	start := max(size-promptHistoryTail, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), promptHistoryTail)
	if start > 0 {
		sc.Scan() // the first line is cut short
	}
	var own, other []string
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"project"`)) {
			continue
		}
		var e historyEntry
		if json.Unmarshal(line, &e) != nil || e.Project != cwd {
			continue
		}
		text := expandPastes(e.Display, e.PastedContents)
		if text == "" {
			continue
		}
		if sessionID != "" && e.SessionID == sessionID {
			own = append(own, text)
		} else {
			other = append(other, text)
		}
	}
	out := make([]string, 0, min(len(own)+len(other), PromptHistoryMax))
	seen := map[string]bool{}
	for _, list := range [][]string{own, other} {
		for i := len(list) - 1; i >= 0 && len(out) < PromptHistoryMax; i-- {
			if !seen[list[i]] {
				seen[list[i]] = true
				out = append(out, list[i])
			}
		}
	}
	return out
}

// expandPastes puts each paste's text back in place of its placeholder.
// One whose text wasn't kept stays a placeholder.
func expandPastes(display string, pastes map[string]pastedContent) string {
	if len(pastes) == 0 {
		return display
	}
	return pastedRef.ReplaceAllStringFunc(display, func(ref string) string {
		p, ok := pastes[pastedRef.FindStringSubmatch(ref)[1]]
		if !ok || p.Type != "text" || p.Content == "" {
			return ref
		}
		return p.Content
	})
}
