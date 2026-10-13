package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	"github.com/rvanmech/unky-mo/internal/tmux"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func nrow(window, session, status string) state.ProjectState {
	return state.ProjectState{Name: "unky-mo", WindowName: "unky-mo", WindowID: window, SessionID: session, Status: status}
}

func withPending(r state.ProjectState, tool, input string) state.ProjectState {
	r.PendingTool, r.PendingInput = tool, json.RawMessage(input)
	return r
}

// trackerRun feeds reads one second apart and returns the events per read.
type trackerRun struct {
	t   *testing.T
	tr  *notifyTracker
	now time.Time
}

func newTrackerRun(t *testing.T) *trackerRun {
	return &trackerRun{t: t, tr: newNotifyTracker(), now: time.Unix(1_800_000_000, 0)}
}

func (r *trackerRun) read(rows ...state.ProjectState) []notifyEvent {
	r.now = r.now.Add(time.Second)
	return r.tr.observe(rows, r.now)
}

func (r *trackerRun) wait(d time.Duration) { r.now = r.now.Add(d) }

func kinds(evs []notifyEvent) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Kind+":"+e.Row.WindowID)
	}
	return strings.Join(out, ",")
}

func TestNotifyTrackerPrimesSilently(t *testing.T) {
	r := newTrackerRun(t)
	if evs := r.read(nrow("@1", "s1", "question"), nrow("@2", "s2", "permission")); len(evs) != 0 {
		t.Fatalf("priming read notified: %s", kinds(evs))
	}
	if evs := r.read(nrow("@1", "s1", "question"), nrow("@2", "s2", "permission")); len(evs) != 0 {
		t.Fatalf("unchanged read notified: %s", kinds(evs))
	}
}

func TestNotifyTrackerInputNeedsTwoReads(t *testing.T) {
	r := newTrackerRun(t)
	r.read(nrow("@1", "s1", "active"))
	if evs := r.read(nrow("@1", "s1", "question")); len(evs) != 0 {
		t.Fatalf("notified on the first read of a change: %s", kinds(evs))
	}
	if got := kinds(r.read(nrow("@1", "s1", "question"))); got != "input:@1" {
		t.Fatalf("second read = %q, want input:@1", got)
	}
	if evs := r.read(nrow("@1", "s1", "question")); len(evs) != 0 {
		t.Fatal("notified again for the same question")
	}

	// From idle too, and for a permission.
	r.read(nrow("@1", "s1", "idle"))
	r.read(nrow("@1", "s1", "idle"))
	r.read(nrow("@1", "s1", "permission"))
	if got := kinds(r.read(nrow("@1", "s1", "permission"))); got != "input:@1" {
		t.Errorf("idle → permission = %q", got)
	}
}

func TestNotifyTrackerFlickerIsSilent(t *testing.T) {
	r := newTrackerRun(t)
	r.read(nrow("@1", "s1", "active"))
	r.read(nrow("@1", "s1", "question"))
	if evs := r.read(nrow("@1", "s1", "active")); len(evs) != 0 {
		t.Fatal("flicker notified")
	}
	// A different candidate in between restarts the count.
	r.read(nrow("@1", "s1", "permission"))
	r.read(nrow("@1", "s1", "question"))
	if evs := r.read(nrow("@1", "s1", "permission")); len(evs) != 0 {
		t.Fatalf("alternating candidates notified: %s", kinds(evs))
	}
}

func TestNotifyTrackerNewPromptSameStatus(t *testing.T) {
	r := newTrackerRun(t)
	first := withPending(nrow("@1", "s1", "permission"), "Bash", `{"command":"make test"}`)
	second := withPending(nrow("@1", "s1", "permission"), "Bash", `{"command":"make install"}`)
	r.read(first)
	r.read(second)
	if got := kinds(r.read(second)); got != "input:@1" {
		t.Errorf("next prompt = %q, want input:@1", got)
	}
}

func TestNotifyTrackerDone(t *testing.T) {
	r := newTrackerRun(t)
	r.read(nrow("@1", "s1", "idle"))
	r.read(nrow("@1", "s1", "active"))
	r.read(nrow("@1", "s1", "active")) // confirmed active
	r.wait(20 * time.Second)
	r.read(nrow("@1", "s1", "idle"))
	if got := kinds(r.read(nrow("@1", "s1", "idle"))); got != "done:@1" {
		t.Fatalf("long turn = %q, want done:@1", got)
	}

	// A short turn is silent.
	r.read(nrow("@1", "s1", "active"))
	r.read(nrow("@1", "s1", "active"))
	r.read(nrow("@1", "s1", "idle"))
	if evs := r.read(nrow("@1", "s1", "idle")); len(evs) != 0 {
		t.Errorf("short turn notified: %s", kinds(evs))
	}

	// A denied permission isn't "done".
	r.read(nrow("@1", "s1", "permission"))
	r.read(nrow("@1", "s1", "permission"))
	r.wait(time.Minute)
	r.read(nrow("@1", "s1", "idle"))
	if evs := r.read(nrow("@1", "s1", "idle")); len(evs) != 0 {
		t.Errorf("permission → idle notified: %s", kinds(evs))
	}
}

func TestNotifyTrackerNewSessionAndMissingRows(t *testing.T) {
	r := newTrackerRun(t)
	r.read(nrow("@1", "s1", "idle"))
	// A session that appears later, already asking, doesn't notify.
	r.read(nrow("@1", "s1", "idle"), nrow("@2", "s2", "question"))
	if evs := r.read(nrow("@1", "s1", "idle"), nrow("@2", "s2", "question")); len(evs) != 0 {
		t.Fatalf("new session notified: %s", kinds(evs))
	}
	// A new session in the same window starts fresh too.
	r.read(nrow("@1", "s9", "question"), nrow("@2", "s2", "question"))
	if evs := r.read(nrow("@1", "s9", "question"), nrow("@2", "s2", "question")); len(evs) != 0 {
		t.Fatalf("new session in a known window notified: %s", kinds(evs))
	}

	// Missing for 30 s (a TUI restart) keeps the confirmed state: no event.
	r.read()
	r.wait(30 * time.Second)
	r.read(nrow("@2", "s2", "question"))
	if evs := r.read(nrow("@2", "s2", "question")); len(evs) != 0 {
		t.Errorf("row back after 30 s notified: %s", kinds(evs))
	}
	// Missing for 3 minutes, it's forgotten and comes back as a new session.
	r.read()
	r.wait(3 * time.Minute)
	r.read()
	r.read(nrow("@2", "s2", "question"))
	if evs := r.read(nrow("@2", "s2", "question")); len(evs) != 0 {
		t.Errorf("forgotten row notified on return: %s", kinds(evs))
	}
	if len(r.tr.rows) != 1 {
		t.Errorf("%d tracked rows, want the forgotten ones dropped", len(r.tr.rows))
	}

	// Rows without a session or window are ignored.
	if evs := r.read(state.ProjectState{Status: "question"}, nrow("", "s3", "question")); len(evs) != 0 {
		t.Error("row without ids tracked")
	}
}

func TestPushMessageFor(t *testing.T) {
	q := withPending(nrow("@3", "s", "question"), "AskUserQuestion",
		`{"questions":[{"question":"Which   storage\nbackend?"},{"question":"second"}]}`)
	cases := []struct {
		name        string
		ev          notifyEvent
		title, body string
	}{
		{"question", notifyEvent{Kind: "input", Row: q}, "unky-mo needs you", "Which storage backend?"},
		{"bash", notifyEvent{Kind: "input", Row: withPending(nrow("@3", "s", "permission"), "Bash", `{"command":"rm -rf build","description":"clean"}`)}, "unky-mo needs permission", "rm -rf build"},
		{"edit", notifyEvent{Kind: "input", Row: withPending(nrow("@3", "s", "permission"), "Edit", `{"file_path":"/repo/internal/web/notifier.go"}`)}, "unky-mo needs permission", "Edit notifier.go"},
		{"plan", notifyEvent{Kind: "input", Row: withPending(nrow("@3", "s", "permission"), "ExitPlanMode", `{"plan":"# x"}`)}, "unky-mo needs permission", "Approve the plan"},
		{"fetch", notifyEvent{Kind: "input", Row: withPending(nrow("@3", "s", "permission"), "WebFetch", `{"url":"https://go.dev/doc"}`)}, "unky-mo needs permission", "Fetch go.dev"},
		{"mcp", notifyEvent{Kind: "input", Row: withPending(nrow("@3", "s", "permission"), "mcp__x__y", `{}`)}, "unky-mo needs permission", "mcp__x__y"},
		{"unknown question", notifyEvent{Kind: "input", Row: nrow("@3", "s", "question")}, "unky-mo needs you", "Waiting for your answer"},
		{"unknown permission", notifyEvent{Kind: "input", Row: nrow("@3", "s", "permission")}, "unky-mo needs permission", "Waiting for permission"},
	}
	for _, c := range cases {
		msg := pushMessageFor(c.ev)
		if msg.Title != c.title || msg.Body != c.body {
			t.Errorf("%s: %q / %q, want %q / %q", c.name, msg.Title, msg.Body, c.title, c.body)
		}
		if msg.Tag != "mo-@3" || msg.URL != "/chat?window=%403" || msg.Window != "@3" || msg.Kind != "input" {
			t.Errorf("%s: %+v", c.name, msg)
		}
	}

	wt := nrow("@4", "s", "idle")
	wt.Name, wt.Parent, wt.WindowName, wt.Branch = "@small-tasks", "unky-mo", "unky-mo@small-tasks", "small-tasks"
	if msg := pushMessageFor(notifyEvent{Kind: "done", Row: wt}); msg.Title != "unky-mo @small-tasks is done" || msg.Body != "unky-mo@small-tasks · small-tasks" {
		t.Errorf("worktree done = %q / %q", msg.Title, msg.Body)
	}

	long := withPending(nrow("@3", "s", "permission"), "Bash", `{"command":"`+strings.Repeat("é", 300)+`"}`)
	if body := pushMessageFor(notifyEvent{Kind: "input", Row: long}).Body; len([]rune(body)) != maxNotifyBody || !strings.HasSuffix(body, "…") {
		t.Errorf("long body: %d chars", len([]rune(body)))
	}
}

type fixedState struct{ st *state.StateFile }

func (f *fixedState) Read() (*state.StateFile, error) {
	cp := *f.st
	cp.Projects = append([]state.ProjectState(nil), f.st.Projects...)
	return &cp, nil
}

func TestNotifyTickSendsToWantingDevices(t *testing.T) {
	_, push, fake, clock, _ := newPushTestServer(t)
	st := &fixedState{st: &state.StateFile{Projects: []state.ProjectState{nrow("@1", "s1", "active")}}}
	srv := NewServer(Deps{Push: push, State: st}, 0, "test")

	inputOnly := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/phone")
	both := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/desk")
	watching := newTestDevice(t, "https://web.push.apple.com/watching")
	_ = push.Subscribe(inputOnly.sub, PushEvents{Input: true}, "phone")
	_ = push.Subscribe(both.sub, PushEvents{Input: true, Done: true}, "desk")
	_ = push.Subscribe(watching.sub, PushEvents{Input: true, Done: true}, "ipad")
	push.Heartbeat(watching.sub.Endpoint, "@1") // shows this session right now

	tr := newNotifyTracker()
	tick := func() {
		clock.t = clock.t.Add(time.Second)
		<-srv.notifyTick(context.Background(), tr, clock.t)
	}
	tick()
	st.st.Projects = []state.ProjectState{withPending(nrow("@1", "s1", "permission"), "Bash", `{"command":"go test ./..."}`)}
	tick()
	tick()

	if fake.count() != 2 {
		t.Fatalf("input sent %d times, want 2 (phone and desk, not the watching iPad)", fake.count())
	}
	got := map[string]PushMessage{}
	for i, r := range fake.reqs {
		switch r.URL.String() {
		case inputOnly.sub.Endpoint:
			got["phone"] = inputOnly.open(t, fake, i)
		case both.sub.Endpoint:
			got["desk"] = both.open(t, fake, i)
		default:
			t.Errorf("sent to %s", r.URL)
		}
	}
	if m := got["phone"]; m.Title != "unky-mo needs permission" || m.Body != "go test ./..." || m.URL != "/chat?window=%401" {
		t.Errorf("phone got %+v", m)
	}
	if got["desk"].Title == "" {
		t.Error("desk got nothing")
	}

	// A long turn ending goes only to devices that want "done".
	st.st.Projects = []state.ProjectState{nrow("@1", "s1", "active")}
	tick()
	tick()
	clock.t = clock.t.Add(time.Minute)
	push.Heartbeat(watching.sub.Endpoint, "@1") // still watching
	st.st.Projects = []state.ProjectState{nrow("@1", "s1", "idle")}
	tick()
	tick()
	if fake.count() != 3 || fake.reqs[2].URL.String() != both.sub.Endpoint {
		t.Fatalf("done went to %d requests; last to %s", fake.count(), fake.reqs[fake.count()-1].URL)
	}
	if m := both.open(t, fake, 2); m.Kind != "done" || m.Title != "unky-mo is done" {
		t.Errorf("done = %+v", m)
	}

	// Once the iPad stops showing the session (heartbeat stale), it's told too.
	clock.t = clock.t.Add(time.Minute)
	st.st.Projects = []state.ProjectState{nrow("@1", "s1", "question")}
	tick()
	tick()
	sentTo := map[string]bool{}
	for _, r := range fake.reqs[3:] {
		sentTo[r.URL.String()] = true
	}
	if len(sentTo) != 3 || !sentTo[watching.sub.Endpoint] {
		t.Errorf("question after the heartbeat expired went to %v", sentTo)
	}
}

func blockedAt(id, name string) blockedDialog {
	return blockedDialog{
		window: tmux.Window{ID: id, Name: name, InstanceID: "x", CWD: "/ws/" + name},
		view:   parseStartupDialog(startupFixtureText("trust-plain.txt")),
	}
}

func startupFixtureText(name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", "startup", name))
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestNotifyTrackerBlockedWindow(t *testing.T) {
	tr := newNotifyTracker()
	other := blockedAt("@5", "other")

	// Dialogs already up on the first look were there before mo web started.
	if evs := tr.observeBlocked([]blockedDialog{other}); len(evs) != 0 {
		t.Fatalf("priming look notified: %s", kinds(evs))
	}
	if evs := tr.observeBlocked([]blockedDialog{other}); len(evs) != 0 {
		t.Fatalf("a primed dialog notified later: %s", kinds(evs))
	}

	// A new one needs two looks in a row, then notifies once.
	b := blockedAt("@9", "shop-api")
	if evs := tr.observeBlocked([]blockedDialog{other, b}); len(evs) != 0 {
		t.Fatalf("notified on the first look: %s", kinds(evs))
	}
	evs := tr.observeBlocked([]blockedDialog{other, b})
	if kinds(evs) != "input:@9" || evs[0].Folder != "/home/dev/work/scratch" || evs[0].Row.WindowName != "shop-api" {
		t.Fatalf("second look = %+v", evs)
	}
	if evs := tr.observeBlocked([]blockedDialog{other, b}); len(evs) != 0 {
		t.Fatalf("notified again for the same dialog: %s", kinds(evs))
	}

	// Answered (gone), then asking again is a new notification.
	tr.observeBlocked([]blockedDialog{other})
	tr.observeBlocked([]blockedDialog{other, b})
	if got := kinds(tr.observeBlocked([]blockedDialog{other, b})); got != "input:@9" {
		t.Errorf("a dialog that came back = %q", got)
	}

	// A different folder in the same window is a different dialog.
	b2 := b
	b2.view.folder = "/home/dev/work/elsewhere"
	tr.observeBlocked([]blockedDialog{other, b2})
	if got := kinds(tr.observeBlocked([]blockedDialog{other, b2})); got != "input:@9" {
		t.Errorf("a new dialog in the same window = %q", got)
	}
}

func TestBlockedDue(t *testing.T) {
	tr := newNotifyTracker()
	now := time.Unix(1_800_000_000, 0)
	if !tr.blockedDue(now) || tr.blockedDue(now.Add(time.Second)) || !tr.blockedDue(now.Add(blockedEvery)) {
		t.Error("the panes should be read once per blockedEvery")
	}
}

func TestPushMessageForBlockedWindow(t *testing.T) {
	row := state.ProjectState{Name: "shop-api", WindowName: "shop-api", WindowID: "@9", Status: "startup"}
	msg := pushMessageFor(notifyEvent{Kind: "input", Row: row, Folder: "/home/dev/work/shop-api"})
	if msg.Title != "shop-api needs you" || msg.Body != "Trust this folder? /home/dev/work/shop-api" ||
		msg.URL != "/chat?window=%409&new=1" || msg.Tag != "mo-@9" || msg.Window != "@9" || msg.Kind != "input" {
		t.Errorf("%+v", msg)
	}
}

func TestNotifyTickBlockedWindow(t *testing.T) {
	_, push, fake, clock, _ := newPushTestServer(t)
	ctrl := gomock.NewController(t)
	sess := mock_web.NewMockSessionOps(ctrl)
	sess.EXPECT().ListWindows().Return([]tmux.Window{
		{ID: "@0", Name: "fish"},                      // the TUI: not a mo launch
		{ID: "@2", Name: "alpha", InstanceID: "a"},    // has a session
		{ID: "@9", Name: "shop-api", InstanceID: "b"}, // launched, no session
	}, nil).AnyTimes()
	screen := "\n\n"
	pane := mock_web.NewMockClaudePane(ctrl)
	// Only the session-less window's pane is ever read.
	pane.EXPECT().Capture("test:@9.0").DoAndReturn(func(string) (string, error) { return screen, nil }).AnyTimes()
	st := &fixedState{st: &state.StateFile{Projects: []state.ProjectState{nrow("@2", "s2", "idle")}}}
	srv := NewServer(Deps{Push: push, State: st, Sessions: sess, ClaudePane: pane}, 0, "test")

	phone := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/phone")
	_ = push.Subscribe(phone.sub, PushEvents{Input: true}, "phone")
	doneOnly := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/other")
	_ = push.Subscribe(doneOnly.sub, PushEvents{Done: true}, "other")

	tr := newNotifyTracker()
	tick := func() {
		clock.t = clock.t.Add(blockedEvery)
		<-srv.notifyTick(context.Background(), tr, clock.t)
	}
	tick() // nothing yet: primes
	screen = startupFixtureText("trust-preapproved.txt")
	tick()
	if fake.count() != 0 {
		t.Fatalf("sent on the first look of the dialog")
	}
	tick()
	if fake.count() != 1 || fake.reqs[0].URL.String() != phone.sub.Endpoint {
		t.Fatalf("sent %d, want 1 to the phone (not the done-only device)", fake.count())
	}
	m := phone.open(t, fake, 0)
	if m.Title != "shop-api needs you" || !strings.Contains(m.Body, "Trust this folder?") || m.URL != "/chat?window=%409&new=1" {
		t.Errorf("phone got %+v", m)
	}
	tick()
	tick()
	if fake.count() != 1 {
		t.Errorf("the same dialog notified again (%d sends)", fake.count())
	}
}
