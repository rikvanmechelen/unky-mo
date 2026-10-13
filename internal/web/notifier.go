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
	"github.com/rvanmech/unky-mo/internal/tmux"
)

// doneMinActive is how long a turn must have run before its end notifies:
// shorter ones are you watching it.
const doneMinActive = 15 * time.Second

// notifyForget drops a row that has been missing this long. Missing for
// less (the TUI rewriting rows during a restart), it keeps its state, so a
// restart doesn't notify.
const notifyForget = 2 * time.Minute

// blockedEvery is how often the notifier reads the panes of windows that
// have no session yet, looking for a startup dialog.
const blockedEvery = 2 * time.Second

// maxNotifyBody bounds a notification's body text, in characters.
const maxNotifyBody = 180

// notifyEvent is a confirmed status change worth a notification.
type notifyEvent struct {
	Kind string // "input" or "done"
	Row  state.ProjectState
	// Folder is set for a window stopped on Claude Code's folder trust
	// dialog: it has no session row, so Row only names the window.
	Folder string
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

	blocked       map[string]*blockedSeen // by window id
	blockedPrimed bool
	lastBlocked   time.Time
}

func newNotifyTracker() *notifyTracker {
	return &notifyTracker{rows: map[string]*trackedRow{}, blocked: map[string]*blockedSeen{}}
}

// blockedSeen is a startup dialog a window has shown for some consecutive
// looks.
type blockedSeen struct {
	sig      string
	looks    int
	notified bool
}

// blockedDialog is a session-less window found on a startup dialog.
type blockedDialog struct {
	window tmux.Window
	view   startupView
}

// blockedDue says whether it's time to look at the panes again.
func (t *notifyTracker) blockedDue(now time.Time) bool {
	if !t.lastBlocked.IsZero() && now.Sub(t.lastBlocked) < blockedEvery {
		return false
	}
	t.lastBlocked = now
	return true
}

// observeBlocked takes one look at the windows on a startup dialog. Like
// observe, the first look only primes it and a dialog must show twice in a
// row before it counts; each dialog notifies once, and a window whose dialog
// goes (answered, or the session started) is forgotten.
func (t *notifyTracker) observeBlocked(found []blockedDialog) []notifyEvent {
	var events []notifyEvent
	seen := map[string]bool{}
	for _, b := range found {
		id := b.window.ID
		seen[id] = true
		sig := b.view.sig()
		bs := t.blocked[id]
		if bs == nil || bs.sig != sig {
			bs = &blockedSeen{sig: sig}
			t.blocked[id] = bs
		}
		bs.looks++
		if !t.blockedPrimed {
			bs.notified = true // already up when mo web started
		}
		if bs.looks >= 2 && !bs.notified {
			bs.notified = true
			events = append(events, notifyEvent{
				Kind: "input",
				Row: state.ProjectState{
					Name: b.window.Name, WindowName: b.window.Name, WindowID: id, Path: b.window.CWD, Status: "startup",
				},
				Folder: b.view.folder,
			})
		}
	}
	for id := range t.blocked {
		if !seen[id] {
			delete(t.blocked, id)
		}
	}
	t.blockedPrimed = true
	return events
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
	case ev.Folder != "":
		msg.URL += "&new=1"
		msg.Title = label + " needs you"
		msg.Body = clip("Trust this folder? "+ev.Folder, maxNotifyBody)
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
	if tracker.blockedDue(now) {
		if found, ok := s.blockedDialogs(st); ok {
			events = append(events, tracker.observeBlocked(found)...)
		}
	}
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

// blockedDialogs reads the pane of every window mo launched that has no
// session yet, and returns those showing a startup dialog. ok is false when
// the windows couldn't be listed, so the tracker keeps what it knew.
func (s *Server) blockedDialogs(st *state.StateFile) (found []blockedDialog, ok bool) {
	if s.deps.Sessions == nil || s.deps.ClaudePane == nil {
		return nil, false
	}
	windows, err := s.deps.Sessions.ListWindows()
	if err != nil {
		return nil, false
	}
	hasSession := map[string]bool{}
	for _, p := range st.Projects {
		if p.SessionID != "" {
			hasSession[p.WindowID] = true
		}
	}
	for _, w := range windows {
		if w.InstanceID == "" || hasSession[w.ID] || !windowIDPattern.MatchString(w.ID) {
			continue
		}
		screen, err := s.deps.ClaudePane.Capture(s.claudeTarget(w.ID))
		if err != nil {
			continue
		}
		if v := parseStartupDialog(screen); v.found {
			found = append(found, blockedDialog{window: w, view: v})
		}
	}
	return found, true
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
