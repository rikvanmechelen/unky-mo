package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rvanmech/unky-mo/internal/state"
)

// doneMinActive is how long a turn must have run before its end notifies:
// shorter ones are you watching it.
const doneMinActive = 15 * time.Second

// notifyForget drops a row that has been missing this long. Missing for
// less (the TUI rewriting rows during a restart), it keeps its state, so a
// restart doesn't notify.
const notifyForget = 2 * time.Minute

// maxNotifyBody bounds a notification's body text, in characters.
const maxNotifyBody = 180

// notifyEvent is a confirmed status change worth a notification.
type notifyEvent struct {
	Kind string // "input" or "done"
	Row  state.ProjectState
}

// observation is what the tracker compares between reads.
type observation struct {
	status string
	sig    string // hash of the pending tool and input
}

type trackedRow struct {
	confirmed    observation
	candidate    observation
	hasCandidate bool
	activeSince  time.Time // when confirmed became "active"
	lastSeen     time.Time
}

// notifyTracker turns successive state-file reads into notification events.
// The first read only primes it; a change must be seen on two consecutive
// reads before it counts, so a one-read flicker never notifies.
type notifyTracker struct {
	primed bool
	rows   map[string]*trackedRow
}

func newNotifyTracker() *notifyTracker {
	return &notifyTracker{rows: map[string]*trackedRow{}}
}

func observe(row state.ProjectState) observation {
	o := observation{status: row.Status}
	if row.PendingTool != "" || len(row.PendingInput) > 0 {
		sum := sha256.Sum256(append([]byte(row.PendingTool+"\x00"), row.PendingInput...))
		o.sig = string(sum[:8])
	}
	return o
}

func needsInput(status string) bool { return status == "question" || status == "permission" }

// observe takes one read of the state file's rows.
func (t *notifyTracker) observe(rows []state.ProjectState, now time.Time) []notifyEvent {
	var events []notifyEvent
	for _, row := range rows {
		if row.WindowID == "" || row.SessionID == "" {
			continue
		}
		key := row.WindowID + "|" + row.SessionID
		o := observe(row)
		tr, ok := t.rows[key]
		if !ok {
			// A new session (or the priming read): record, don't notify.
			tr = &trackedRow{confirmed: o}
			if o.status == "active" {
				tr.activeSince = now
			}
			t.rows[key] = tr
		}
		tr.lastSeen = now

		switch {
		case o == tr.confirmed:
			tr.hasCandidate = false
		case tr.hasCandidate && o == tr.candidate:
			from := tr.confirmed
			tr.confirmed, tr.hasCandidate = o, false
			if kind := transitionKind(from, o, now.Sub(tr.activeSince)); kind != "" {
				events = append(events, notifyEvent{Kind: kind, Row: row})
			}
			if o.status == "active" && from.status != "active" {
				tr.activeSince = now
			}
		default:
			tr.candidate, tr.hasCandidate = o, true
		}
	}
	for key, tr := range t.rows {
		if now.Sub(tr.lastSeen) > notifyForget {
			delete(t.rows, key)
		}
	}
	if !t.primed {
		t.primed = true
		return nil
	}
	return events
}

// transitionKind judges a confirmed change: "input" when it now needs you
// (or asks something new), "done" when a long enough turn ended.
func transitionKind(from, to observation, activeFor time.Duration) string {
	switch {
	case needsInput(to.status) && (from.status != to.status || from.sig != to.sig):
		return "input"
	case from.status == "active" && to.status == "idle" && activeFor >= doneMinActive:
		return "done"
	}
	return ""
}

// projectLabel names a row in a notification: the project, or the parent
// project and the worktree ("unky-mo @small-tasks").
func projectLabel(row state.ProjectState) string {
	if row.Parent != "" {
		return row.Parent + " " + row.Name
	}
	return row.Name
}

// clip collapses whitespace and cuts s to max characters.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(stripControl(s)), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// pendingSummary says in a line what a pending question or permission asks.
func pendingSummary(status, tool string, input json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Notebook string `json:"notebook_path"`
		URL      string `json:"url"`
		Query    string `json:"query"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "AskUserQuestion":
		if len(in.Questions) > 0 && in.Questions[0].Question != "" {
			return in.Questions[0].Question
		}
	case "Bash":
		if in.Command != "" {
			return in.Command
		}
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		path := in.FilePath
		if path == "" {
			path = in.Notebook
		}
		if path != "" {
			return tool + " " + filepath.Base(path)
		}
	case "ExitPlanMode":
		return "Approve the plan"
	case "WebFetch":
		if u, err := url.Parse(in.URL); err == nil && u.Host != "" {
			return "Fetch " + u.Host
		}
	case "WebSearch":
		if in.Query != "" {
			return "Search: " + in.Query
		}
	}
	if tool != "" {
		return tool
	}
	if status == "question" {
		return "Waiting for your answer"
	}
	return "Waiting for permission"
}

// pushMessageFor formats an event as a notification.
func pushMessageFor(ev notifyEvent) PushMessage {
	row := ev.Row
	label := projectLabel(row)
	msg := PushMessage{
		Kind:   ev.Kind,
		Tag:    "mo-" + row.WindowID,
		URL:    "/chat?window=" + url.QueryEscape(row.WindowID),
		Window: row.WindowID,
	}
	switch {
	case ev.Kind == "done":
		msg.Title = label + " is done"
		parts := []string{row.WindowName}
		if row.Branch != "" {
			parts = append(parts, row.Branch)
		}
		msg.Body = clip(strings.Join(parts, " · "), maxNotifyBody)
	case row.Status == "question":
		msg.Title = label + " needs you"
		msg.Body = clip(pendingSummary(row.Status, row.PendingTool, row.PendingInput), maxNotifyBody)
	default:
		msg.Title = label + " needs permission"
		msg.Body = clip(pendingSummary(row.Status, row.PendingTool, row.PendingInput), maxNotifyBody)
	}
	return msg
}

// RunNotifier watches the state file and sends a push to each subscribed
// device when a session needs input or is done. It blocks until ctx ends;
// mo web runs it in a goroutine when notifications are on.
func (s *Server) RunNotifier(ctx context.Context, interval time.Duration) {
	if s.deps.Push == nil || s.deps.State == nil {
		return
	}
	tracker := newNotifyTracker()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.notifyTick(ctx, tracker, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// notifyTick is one read of the state file. Sends run in their own
// goroutines; the returned channel closes when they're all done (tests wait
// on it, the loop doesn't).
func (s *Server) notifyTick(ctx context.Context, tracker *notifyTracker, now time.Time) <-chan struct{} {
	done := make(chan struct{})
	st, err := s.deps.State.Read()
	if err != nil || st == nil {
		close(done)
		return done
	}
	events := tracker.observe(st.Projects, now)
	if len(events) == 0 {
		close(done)
		return done
	}
	s.fillBranches(st)
	branches := map[string]string{}
	for _, p := range st.Projects {
		branches[p.WindowID] = p.Branch
	}

	pending := 0
	finished := make(chan struct{})
	for _, ev := range events {
		if ev.Row.Branch == "" {
			ev.Row.Branch = branches[ev.Row.WindowID]
		}
		msg := pushMessageFor(ev)
		for _, e := range s.deps.Push.Entries() {
			if (ev.Kind == "input" && !e.Events.Input) || (ev.Kind == "done" && !e.Events.Done) {
				continue
			}
			if s.deps.Push.Present(e.Subscription.Endpoint, ev.Row.WindowID) {
				continue
			}
			pending++
			go func(e PushEntry) {
				defer func() { finished <- struct{}{} }()
				sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				if err := s.deps.Push.Deliver(sendCtx, e, msg); err != nil {
					fmt.Fprintf(os.Stderr, "push to %s: %v\n", deviceName(e), err)
				}
			}(e)
		}
	}
	go func() {
		for i := 0; i < pending; i++ {
			<-finished
		}
		close(done)
	}()
	return done
}

// deviceName names a device in logs: its label, else its push host.
func deviceName(e PushEntry) string {
	if e.Label != "" {
		return e.Label
	}
	if u, err := url.Parse(e.Subscription.Endpoint); err == nil {
		return u.Host
	}
	return "device"
}
