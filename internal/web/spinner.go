package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// spinnerView is Claude Code's own "working" line, scraped from its pane:
//
//	✻ Discombobulating… (1m 9s · ↓ 413 tokens · thought for 4s)
//
// Verb is "Discombobulating…" and Parts the "·"-separated details.
// ElapsedSeconds and Tokens are pulled out of Parts (and left there) so the
// browser can tick them between polls; -1 / "" when absent.
type spinnerView struct {
	Verb           string   `json:"verb"`
	Parts          []string `json:"parts"`
	ElapsedSeconds int      `json:"elapsed_seconds"`
	Tokens         string   `json:"tokens"`
}

// spinnerLine matches the spinner: one of Claude Code's spinner glyphs at
// column 0, a verb ending in "…" (finished turns print "✻ Worked for 3s"
// with no ellipsis), then optional details in parentheses.
var spinnerLine = regexp.MustCompile(`^[·✢✳✶✻✽*]\s+(\S[^()]*…)\s*(?:\((.*)\))?\s*$`)

var (
	durationPart = regexp.MustCompile(`^(?:(\d+)h\s*)?(?:(\d+)m\s*)?(?:(\d+)s)?$`)
	tokensPart   = regexp.MustCompile(`tokens?$`)
)

// spinnerScanLines bounds how far above the prompt box the spinner may sit
// (a todo list can render between them).
const spinnerScanLines = 40

// parseSpinner finds the spinner in a capture of Claude's visible screen.
// It sits above the prompt box, so it scans upward from the box's top
// border (or the bottom, if no box is found), which keeps text typed into
// the prompt from ever matching. On a wide terminal Claude Code draws a side
// panel to the right of the transcript, past a "│", so each line is cut
// there first. Returns nil when Claude isn't showing one.
func parseSpinner(screen string) *spinnerView {
	lines := strings.Split(screen, "\n")
	end := len(lines)
	for i := len(lines) - 1; i > 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "❯") && isRule(lines[i-1]) {
			end = i - 1
			break
		}
	}
	for i := end - 1; i >= 0 && i >= end-spinnerScanLines; i-- {
		line, _, _ := strings.Cut(lines[i], "│")
		m := spinnerLine.FindStringSubmatch(strings.TrimRight(line, " "))
		if m == nil {
			continue
		}
		v := &spinnerView{Verb: strings.TrimSpace(m[1]), Parts: []string{}, ElapsedSeconds: -1}
		for _, p := range strings.Split(m[2], "·") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			v.Parts = append(v.Parts, p)
			if secs, ok := parseElapsed(p); ok && v.ElapsedSeconds < 0 {
				v.ElapsedSeconds = secs
			} else if v.Tokens == "" && tokensPart.MatchString(p) {
				v.Tokens = p
			}
		}
		return v
	}
	return nil
}

func isRule(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && strings.Trim(line, "─") == ""
}

// parseElapsed reads "9s", "1m 9s", "1h 2m" and the like.
func parseElapsed(s string) (int, bool) {
	m := durationPart.FindStringSubmatch(s)
	if m == nil || s == "" {
		return 0, false
	}
	total := 0
	for i, mult := range []int{3600, 60, 1} {
		if m[i+1] != "" {
			n, _ := strconv.Atoi(m[i+1])
			total += n * mult
		}
	}
	return total, true
}

// handleSpinner returns Claude Code's live spinner line for the window's
// session, or {"spinner": null} when none is on screen. Claude's pane is
// always pane 0 of its window (the same target the prompt box types into),
// derived from the state file, never from the request.
func (s *Server) handleSpinner(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	if _, _, _, ok := s.resolveSession(windowID); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	screen, err := s.deps.ClaudePane.Capture(s.tmuxSession + ":" + windowID + ".0")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]*spinnerView{"spinner": parseSpinner(screen)})
}
