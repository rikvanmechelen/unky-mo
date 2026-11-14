package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
)

// toolUseIDRe is what a tool_use id may look like here (Claude Code's are
// "toolu_" + base62); it keeps the id a plain token inside the search below.
var toolUseIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// maxToolImages bounds ?i=, the image's index within one tool result.
const maxToolImages = 32

var errNoToolImage = errors.New("no such tool image")

// handleToolImage serves an image a tool result carries, e.g. a Read of a
// screenshot. The transcript stream leaves these out (slimLine: ~600 KB each,
// and most are never looked at), so the chat view fetches one when its card
// opens (plan: docs/plans/transcript-load.md). The session comes from the
// state row, never the request, and only the four attachable image types are
// served, by sniffed content.
func (s *Server) handleToolImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("toolUseID")
	i, err := strconv.Atoi(r.URL.Query().Get("i"))
	if !toolUseIDRe.MatchString(id) || err != nil || i < 0 || i >= maxToolImages {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad tool use id or image index"))
		return
	}
	path, _, _, ok := s.resolveSession(r.PathValue("windowID"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", r.PathValue("windowID")))
		return
	}
	data, err := readToolImage(path, id, i)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	typ := http.DetectContentType(data)
	if _, ok := attachmentExts[typ]; !ok {
		writeError(w, http.StatusNotFound, errNoToolImage)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A tool result never changes.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = w.Write(data)
}

// readToolImage finds the user line in the JSONL at path whose tool_result
// answers tool use id, and decodes the i'th base64 image in that result. The
// quoted id is only a quick filter: each line it appears on is parsed, so an
// id quoted in some other text doesn't count.
func readToolImage(path, id string, i int) ([]byte, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, errNoToolImage
	}
	needle := []byte(`"` + id + `"`)
	for off := 0; ; {
		at := bytes.Index(file[off:], needle)
		if at < 0 {
			return nil, errNoToolImage
		}
		at += off
		start := bytes.LastIndexByte(file[:at], '\n') + 1
		end := bytes.IndexByte(file[at:], '\n')
		if end < 0 {
			end = len(file)
		} else {
			end += at
		}
		if img, ok := toolImageInLine(file[start:end], id, i); ok {
			return img, nil
		}
		off = end
	}
}

func toolImageInLine(line []byte, id string, i int) ([]byte, bool) {
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &msg) != nil || msg.Type != "user" {
		return nil, false
	}
	for _, block := range msg.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID != id {
			continue
		}
		var inner []struct {
			Type   string `json:"type"`
			Source struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"source"`
		}
		if json.Unmarshal(block.Content, &inner) != nil {
			return nil, false
		}
		n := 0
		for _, b := range inner {
			if b.Type != "image" || b.Source.Type != "base64" {
				continue
			}
			if n == i {
				data, err := base64.StdEncoding.DecodeString(b.Source.Data)
				return data, err == nil
			}
			n++
		}
		return nil, false
	}
	return nil, false
}
