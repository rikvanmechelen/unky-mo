package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Subagent is one agent a session spawned with the Agent tool, read from
// the session's subagents directory: <projects dir>/<sessionID>/subagents/
// agent-<id>.jsonl (its transcript) plus agent-<id>.meta.json.
type Subagent struct {
	ID          string
	AgentType   string
	Description string
	// ToolUseID is the parent transcript's Agent tool_use that spawned it.
	ToolUseID string
	// Background is true for an agent that runs while the parent carries on
	// (and may end its turn) — the case the parent's status can't show.
	Background     bool
	TranscriptPath string
	StartedAt      time.Time
	LastActivity   time.Time
	// State is SubagentRunning, SubagentWaiting or SubagentDone, or the
	// status a task-notification reported for an agent that stopped
	// without finishing (e.g. "killed").
	State string
	// Running is true while the agent isn't finished: running, or waiting
	// on background work of its own.
	Running  bool
	ToolUses int
	// LastTool and LastToolDetail are the agent's most recent tool call,
	// e.g. "WebFetch" and its URL.
	LastTool       string
	LastToolDetail string
}

const (
	SubagentRunning = "running"
	// SubagentWaiting is a background agent that ended its turn without
	// delivering its report: it's waiting on background work of its own
	// (e.g. a run_in_background shell) and resumes when that reports.
	SubagentWaiting = "waiting"
	SubagentDone    = "done"
)

// SubagentsDir is where Claude Code keeps sessionID's subagent transcripts
// for a checkout at projectPath.
func SubagentsDir(projectPath, sessionID string) string {
	return filepath.Join(ProjectsDirForPath(projectPath), sessionID, "subagents")
}

// SubagentReader lists a session's subagents. Transcripts only ever grow,
// so it remembers how far it has read each file and parses just the new
// lines on the next call — a dashboard polls this every couple of seconds
// and a research agent's transcript runs to megabytes.
type SubagentReader struct {
	mu      sync.Mutex
	agents  map[string]*subagentScan // transcript path → scan
	parents map[string]*parentScan   // parent transcript path → scan
}

func NewSubagentReader() *SubagentReader {
	return &SubagentReader{agents: map[string]*subagentScan{}, parents: map[string]*parentScan{}}
}

// List returns sessionID's subagents, oldest first; none (and no error)
// when it has never spawned one.
//
// An agent is done once it delivers its report: the hand-back tool call
// that ends its turn, or — for agents not told to hand back (older Claude
// Code, foreground agents) — a final end_turn reply. An agent that was told
// to hand back but ends a turn without doing so is waiting on background
// work of its own. A task-notification after its last activity with a
// status other than "completed" (it was stopped) ends it too. Sending a
// finished agent another message starts it again.
func (r *SubagentReader) List(projectPath, sessionID string) ([]Subagent, error) {
	dir := SubagentsDir(projectPath, sessionID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	parentPath := filepath.Join(ProjectsDirForPath(projectPath), sessionID+".jsonl")
	ps := r.parents[parentPath]
	if ps == nil {
		ps = &parentScan{notified: map[string]notification{}}
		r.parents[parentPath] = ps
	}
	if err := ps.update(parentPath); err != nil {
		return nil, err
	}

	var out []Subagent
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(dir, name)
		sc := r.agents[path]
		if sc == nil {
			sc = &subagentScan{}
			r.agents[path] = sc
		}
		if err := sc.update(path); err != nil {
			continue // vanished between ReadDir and open — skip it
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		meta := sc.meta
		if meta == nil {
			meta = readSubagentMeta(filepath.Join(dir, "agent-"+id+".meta.json"))
			sc.meta = meta // nil again on failure, so the next call retries
		}
		a := Subagent{
			ID:             id,
			TranscriptPath: path,
			StartedAt:      sc.first,
			LastActivity:   sc.last,
			ToolUses:       sc.toolUses,
			LastTool:       sc.lastTool,
			LastToolDetail: sc.lastDetail,
		}
		if meta != nil {
			a.AgentType, a.Description, a.ToolUseID = meta.AgentType, meta.Description, meta.ToolUseID
			a.Background = meta.RequestShape == "background"
		}
		a.State = sc.state()
		if n, ok := ps.notified[id]; ok && !n.at.Before(sc.last) && a.State != SubagentDone {
			switch {
			case n.status != "completed" && n.status != "":
				a.State = n.status
			case a.State == SubagentRunning:
				a.State = SubagentDone // reported finished without a closing line
			}
		}
		a.Running = a.State == SubagentRunning || a.State == SubagentWaiting
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

type subagentMeta struct {
	AgentType    string `json:"agentType"`
	Description  string `json:"description"`
	ToolUseID    string `json:"toolUseId"`
	RequestShape string `json:"requestShape"`
}

func readSubagentMeta(path string) *subagentMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m subagentMeta
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

// lineTail hands back the complete lines appended to a file since the
// previous call, holding an unterminated trailing line for the next one.
type lineTail struct {
	offset  int64
	partial []byte
}

// next returns the new complete lines; reset is true when the file shrank
// and is being re-read from the start, so the caller must drop what it
// accumulated.
func (t *lineTail) next(path string) (lines [][]byte, reset bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.Size() < t.offset {
		t.offset, t.partial, reset = 0, nil, true
	}
	if info.Size() == t.offset {
		return nil, reset, nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, reset, err
	}
	chunk, err := io.ReadAll(f)
	if err != nil {
		return nil, reset, err
	}
	t.offset += int64(len(chunk))
	data := append(t.partial, chunk...)
	parts := bytes.Split(data, []byte("\n"))
	t.partial = append([]byte(nil), parts[len(parts)-1]...)
	return parts[:len(parts)-1], reset, nil
}

// subagentScan accumulates what List reports about one subagent transcript.
type subagentScan struct {
	tail  lineTail
	meta  *subagentMeta
	first time.Time
	last  time.Time
	// handsBack: the agent was told to deliver its report via a hand-back
	// tool call, so a plain end_turn doesn't finish it.
	handsBack  bool
	endTurn    bool // the latest line ended a turn with a reply
	handedBack bool // the latest line was the hand-back ending the turn
	toolUses   int
	lastTool   string
	lastDetail string
}

func (s *subagentScan) update(path string) error {
	lines, reset, err := s.tail.next(path)
	if err != nil {
		return err
	}
	if reset {
		*s = subagentScan{tail: s.tail, meta: s.meta}
	}
	for _, line := range lines {
		if bytes.Contains(line, []byte(handbackTool)) {
			s.handsBack = true
		}
		var l struct {
			Type         string    `json:"type"`
			Timestamp    time.Time `json:"timestamp"`
			ToolEndsTurn bool      `json:"toolEndsTurn"`
			Message      struct {
				StopReason string          `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		if !l.Timestamp.IsZero() {
			if s.first.IsZero() {
				s.first = l.Timestamp
			}
			s.last = l.Timestamp
		}
		// A turn ends with a final reply, or with a tool call that ends it
		// (the hand-back delivering the agent's report). Anything after
		// that means the agent was resumed.
		s.handedBack = l.ToolEndsTurn
		s.endTurn = l.Type == "assistant" && l.Message.StopReason == "end_turn"
		if l.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type  string                     `json:"type"`
			Name  string                     `json:"name"`
			Input map[string]json.RawMessage `json:"input"`
		}
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			s.toolUses++
			s.lastTool = b.Name
			s.lastDetail = toolDetail(b.Input)
		}
	}
	return nil
}

// handbackTool is the tool a background agent delivers its report with;
// its system prompt names it, which is how the scan learns the agent uses
// it.
const handbackTool = "SubagentHandback"

func (s *subagentScan) state() string {
	switch {
	case s.handedBack:
		return SubagentDone
	case s.endTurn && s.handsBack:
		return SubagentWaiting
	case s.endTurn:
		return SubagentDone
	default:
		return SubagentRunning
	}
}

// toolDetail picks the input field that best says what a tool call is
// doing, the way Claude Code's own tool rows do.
func toolDetail(input map[string]json.RawMessage) string {
	for _, key := range []string{"description", "file_path", "command", "url", "query", "pattern", "path"} {
		var v string
		if json.Unmarshal(input[key], &v) == nil && strings.TrimSpace(v) != "" {
			v = strings.TrimSpace(strings.SplitN(strings.TrimSpace(v), "\n", 2)[0])
			if r := []rune(v); len(r) > 120 {
				v = string(r[:119]) + "…"
			}
			return v
		}
	}
	return ""
}

// parentScan collects, per task ID, the latest task-notification the
// parent transcript got for it.
type parentScan struct {
	tail     lineTail
	notified map[string]notification
}

type notification struct {
	at     time.Time
	status string
}

var (
	taskNotificationRe = regexp.MustCompile(`<task-notification>.*?</task-notification>`)
	taskIDRe           = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
	taskStatusRe       = regexp.MustCompile(`<status>([^<]+)</status>`)
)

func (p *parentScan) update(path string) error {
	lines, reset, err := p.tail.next(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if reset {
		p.notified = map[string]notification{}
	}
	for _, line := range lines {
		if !bytes.Contains(line, []byte("<task-notification>")) {
			continue
		}
		// The notification is queued (a queue-operation line), then
		// delivered as a user line or attachment; keep the latest copy,
		// since a resumed agent gets a fresh notification each time it
		// stops.
		var l struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(line, &l) != nil || l.Timestamp.IsZero() {
			continue
		}
		for _, block := range taskNotificationRe.FindAll(line, -1) {
			id := taskIDRe.FindSubmatch(block)
			if id == nil {
				continue
			}
			n := notification{at: l.Timestamp}
			if st := taskStatusRe.FindSubmatch(block); st != nil {
				n.status = string(st[1])
			}
			if prev, ok := p.notified[string(id[1])]; !ok || n.at.After(prev.at) {
				p.notified[string(id[1])] = n
			}
		}
	}
	return nil
}
