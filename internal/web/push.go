package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rvanmech/unky-mo/internal/webpush"
)

// maxPushSubscriptions bounds the store: these are your devices, not users.
const maxPushSubscriptions = 32

// presenceTTL is how long a page's heartbeat keeps its device quiet for the
// session it shows. Pages beat every 15 s.
const presenceTTL = 30 * time.Second

// ErrTooManySubscriptions is returned when the store is full.
var ErrTooManySubscriptions = errors.New("too many push subscriptions")

// PushEvents are the kinds of notification a device wants.
type PushEvents struct {
	Input bool `json:"input"` // a session needs input (question or permission)
	Done  bool `json:"done"`  // a session finished its turn
}

// PushEntry is one subscribed device.
type PushEntry struct {
	Subscription webpush.Subscription `json:"subscription"`
	Events       PushEvents           `json:"events"`
	Label        string               `json:"label,omitempty"`
	Created      time.Time            `json:"created"`
}

// PushMessage is a notification's payload, read by sw.js. It's encrypted end
// to end, but kept to what the notification shows.
type PushMessage struct {
	Kind   string `json:"kind"` // "input", "done" or "test"
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Tag    string `json:"tag,omitempty"`    // a newer notification with the same tag replaces it
	URL    string `json:"url,omitempty"`    // opened on tap
	Window string `json:"window,omitempty"` // tmux window id
}

// PushService sends Web Push notifications to subscribed devices: the
// VAPID keys, the subscription store (a JSON file) and, in memory, which
// device currently shows which session (presence). See
// docs/plans/notifications.md.
type PushService struct {
	keys   *webpush.Keys
	sender *webpush.Sender
	path   string
	now    func() time.Time

	mu       sync.Mutex
	entries  []PushEntry
	presence map[string]map[string]time.Time // endpoint → window → last heartbeat
}

// NewPushService loads (or creates) the VAPID key and the subscription store
// in dir. A nil client uses a plain http.Client.
func NewPushService(dir string, client webpush.Doer, now func() time.Time) (*PushService, error) {
	if now == nil {
		now = time.Now
	}
	keys, _, err := webpush.LoadOrCreateKeys(filepath.Join(dir, "vapid.pem"))
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	p := &PushService{
		keys:     keys,
		sender:   &webpush.Sender{Keys: keys, Client: client, Now: now},
		path:     filepath.Join(dir, "subscriptions.json"),
		now:      now,
		presence: map[string]map[string]time.Time{},
	}
	data, err := os.ReadFile(p.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &p.entries); err != nil {
			return nil, fmt.Errorf("%s: %w", p.path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return p, nil
}

// PublicKey is the VAPID public key for PushManager.subscribe.
func (p *PushService) PublicKey() string { return p.keys.PublicKey() }

// Entries returns a copy of the subscribed devices.
func (p *PushService) Entries() []PushEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PushEntry(nil), p.entries...)
}

// Lookup returns the entry for endpoint.
func (p *PushService) Lookup(endpoint string) (PushEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.Subscription.Endpoint == endpoint {
			return e, true
		}
	}
	return PushEntry{}, false
}

// Subscribe adds a device, or replaces the entry with the same endpoint.
func (p *PushService) Subscribe(sub webpush.Subscription, events PushEvents, label string) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := append([]PushEntry(nil), p.entries...)
	entry := PushEntry{Subscription: sub, Events: events, Label: label, Created: p.now().UTC()}
	replaced := false
	for i, e := range entries {
		if e.Subscription.Endpoint == sub.Endpoint {
			entry.Created = e.Created
			entries[i] = entry
			replaced = true
		}
	}
	if !replaced {
		if len(entries) >= maxPushSubscriptions {
			return ErrTooManySubscriptions
		}
		entries = append(entries, entry)
	}
	return p.saveLocked(entries)
}

// Unsubscribe removes the entry for endpoint (a no-op if there is none).
func (p *PushService) Unsubscribe(endpoint string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.presence, endpoint)
	var kept []PushEntry
	for _, e := range p.entries {
		if e.Subscription.Endpoint != endpoint {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(p.entries) {
		return nil
	}
	return p.saveLocked(kept)
}

// saveLocked writes entries atomically (0600) and adopts them.
func (p *PushService) saveLocked(entries []PushEntry) error {
	if entries == nil {
		entries = []PushEntry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".subscriptions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p.path); err != nil {
		return err
	}
	p.entries = entries
	return nil
}

// Heartbeat records that endpoint's page shows window (visible and
// focused). An empty window clears the endpoint's presence. Unknown
// endpoints are ignored.
func (p *PushService) Heartbeat(endpoint, window string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	known := false
	for _, e := range p.entries {
		if e.Subscription.Endpoint == endpoint {
			known = true
		}
	}
	if !known {
		return
	}
	if window == "" {
		delete(p.presence, endpoint)
		return
	}
	if p.presence[endpoint] == nil {
		p.presence[endpoint] = map[string]time.Time{}
	}
	p.presence[endpoint][window] = p.now()
}

// Present reports whether endpoint's page showed window within presenceTTL.
func (p *PushService) Present(endpoint, window string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.presence[endpoint][window]
	return ok && p.now().Sub(at) < presenceTTL
}

// pushOptions are the Web Push headers for a message: needs-input is urgent
// and kept an hour, done is normal and stale after ten minutes. The topic
// makes the push service replace an undelivered message for the same window.
func pushOptions(msg PushMessage) webpush.Options {
	opts := webpush.Options{TTL: 10 * time.Minute, Urgency: "normal"}
	if msg.Kind == "input" || msg.Kind == "test" {
		opts = webpush.Options{TTL: time.Hour, Urgency: "high"}
	}
	if msg.Window != "" {
		opts.Topic = pushTopic(msg.Window)
	}
	return opts
}

// pushTopic turns a tmux window id ("@12") into a Topic header value.
func pushTopic(window string) string {
	out := []byte("w")
	for _, c := range []byte(window) {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			out = append(out, c)
		}
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

// Deliver sends msg to one device, removing it when the push service says
// it's gone (the error is still returned).
func (p *PushService) Deliver(ctx context.Context, e PushEntry, msg PushMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	err = p.sender.Send(ctx, e.Subscription, payload, pushOptions(msg))
	if errors.Is(err, webpush.ErrGone) {
		_ = p.Unsubscribe(e.Subscription.Endpoint)
	}
	return err
}
