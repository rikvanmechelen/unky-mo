package web

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/webpush"
)

// fakePushService stands in for FCM/Apple: it records each request and
// answers with status.
type fakePushService struct {
	mu     sync.Mutex
	status int
	reqs   []*http.Request
	bodies [][]byte
}

func (f *fakePushService) Do(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	f.bodies = append(f.bodies, body)
	status := f.status
	if status == 0 {
		status = http.StatusCreated
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (f *fakePushService) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// testDevice is a browser's side of a subscription.
type testDevice struct {
	sub  webpush.Subscription
	priv *ecdh.PrivateKey
	auth []byte
}

func newTestDevice(t *testing.T, endpoint string) testDevice {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var sub webpush.Subscription
	sub.Endpoint = endpoint
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)
	return testDevice{sub: sub, priv: priv, auth: auth}
}

// open decrypts the n-th message the fake push service received.
func (d testDevice) open(t *testing.T, f *fakePushService, n int) PushMessage {
	t.Helper()
	f.mu.Lock()
	body := f.bodies[n]
	f.mu.Unlock()
	plain, err := webpush.Decrypt(body, d.priv, d.auth)
	if err != nil {
		t.Fatal(err)
	}
	var msg PushMessage
	if err := json.Unmarshal(plain, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

type pushClock struct{ t time.Time }

func (c *pushClock) now() time.Time { return c.t }

func newPushTestServer(t *testing.T) (*Server, *PushService, *fakePushService, *pushClock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "push")
	fake := &fakePushService{}
	clock := &pushClock{t: time.Unix(1_800_000_000, 0)}
	push, err := NewPushService(dir, fake, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(Deps{Push: push}, 0, "test"), push, fake, clock, dir
}

func doJSON(t *testing.T, srv http.Handler, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if s, ok := body.(string); ok {
		r = strings.NewReader(s)
	} else if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, url, r))
	return rec
}

func TestPushSubscribeLookupUnsubscribe(t *testing.T) {
	srv, push, _, _, dir := newPushTestServer(t)
	dev := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/phone")

	rec := doJSON(t, srv, http.MethodGet, "/api/push", nil)
	var info struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"publicKey"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if !info.Enabled || info.PublicKey != push.PublicKey() || info.PublicKey == "" {
		t.Fatalf("/api/push = %s", rec.Body)
	}

	lookup := "/api/push/subscriptions?endpoint=" + dev.sub.Endpoint
	if rec := doJSON(t, srv, http.MethodGet, lookup, nil); !strings.Contains(rec.Body.String(), `"subscribed":false`) {
		t.Fatalf("lookup before subscribing = %s", rec.Body)
	}

	rec = doJSON(t, srv, http.MethodPost, "/api/push/subscriptions", map[string]any{
		"subscription": dev.sub, "events": PushEvents{Input: true}, "label": "Chrome\x1b[31m on Android",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	e, ok := push.Lookup(dev.sub.Endpoint)
	if !ok || !e.Events.Input || e.Events.Done || e.Label != "Chrome[31m on Android" {
		t.Fatalf("entry = %+v, %v", e, ok)
	}

	// Re-subscribing replaces the entry (new choices), keeping one per endpoint.
	doJSON(t, srv, http.MethodPost, "/api/push/subscriptions", map[string]any{
		"subscription": dev.sub, "events": PushEvents{Input: true, Done: true}, "label": strings.Repeat("é", 100),
	})
	if n := len(push.Entries()); n != 1 {
		t.Fatalf("%d entries after re-subscribing", n)
	}
	e, _ = push.Lookup(dev.sub.Endpoint)
	if !e.Events.Done || len(e.Label) > maxPushLabel || !strings.HasPrefix(e.Label, "éé") {
		t.Errorf("replaced entry = %+v", e)
	}
	rec = doJSON(t, srv, http.MethodGet, lookup, nil)
	if !strings.Contains(rec.Body.String(), `"subscribed":true`) || !strings.Contains(rec.Body.String(), `"done":true`) {
		t.Errorf("lookup = %s", rec.Body)
	}

	// The store survives a restart and is private.
	if fi, err := os.Stat(filepath.Join(dir, "subscriptions.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file: %v %v", fi, err)
	}
	reloaded, err := NewPushService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Lookup(dev.sub.Endpoint); !ok || reloaded.PublicKey() != push.PublicKey() {
		t.Error("store or key didn't survive a reload")
	}

	rec = doJSON(t, srv, http.MethodDelete, "/api/push/subscriptions", map[string]string{"endpoint": dev.sub.Endpoint})
	if rec.Code != http.StatusNoContent || len(push.Entries()) != 0 {
		t.Errorf("unsubscribe: %d, %d entries", rec.Code, len(push.Entries()))
	}
	if rec := doJSON(t, srv, http.MethodDelete, "/api/push/subscriptions", map[string]string{"endpoint": "https://fcm.googleapis.com/x"}); rec.Code != http.StatusNoContent {
		t.Errorf("unsubscribing an unknown endpoint: %d", rec.Code)
	}
}

func TestPushSubscribeRefusals(t *testing.T) {
	srv, push, _, _, _ := newPushTestServer(t)

	internal := newTestDevice(t, "https://intranet.example/hook")
	badKey := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/x")
	badKey.sub.Keys.Auth = "c2hvcnQ"
	for name, body := range map[string]any{
		"unknown host": map[string]any{"subscription": internal.sub, "events": PushEvents{Input: true}},
		"bad auth":     map[string]any{"subscription": badKey.sub},
		"not json":     "{",
	} {
		if rec := doJSON(t, srv, http.MethodPost, "/api/push/subscriptions", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	huge := `{"label":"` + strings.Repeat("x", maxPushBody) + `"}`
	if rec := doJSON(t, srv, http.MethodPost, "/api/push/subscriptions", huge); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
	if len(push.Entries()) != 0 {
		t.Fatal("a refused subscription was stored")
	}

	for i := 0; i < maxPushSubscriptions; i++ {
		dev := newTestDevice(t, fmt.Sprintf("https://fcm.googleapis.com/fcm/send/%d", i))
		if err := push.Subscribe(dev.sub, PushEvents{Input: true}, ""); err != nil {
			t.Fatal(err)
		}
	}
	extra := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/extra")
	if rec := doJSON(t, srv, http.MethodPost, "/api/push/subscriptions", map[string]any{"subscription": extra.sub}); rec.Code != http.StatusConflict {
		t.Errorf("past the cap: %d", rec.Code)
	}
}

func TestPushTestNotification(t *testing.T) {
	srv, push, fake, _, _ := newPushTestServer(t)
	dev := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/phone")
	other := newTestDevice(t, "https://web.push.apple.com/other")
	_ = push.Subscribe(dev.sub, PushEvents{Input: true}, "")
	_ = push.Subscribe(other.sub, PushEvents{Input: true}, "")

	rec := doJSON(t, srv, http.MethodPost, "/api/push/test", map[string]string{"endpoint": dev.sub.Endpoint})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("test push: %d %s", rec.Code, rec.Body)
	}
	if fake.count() != 1 || fake.reqs[0].URL.String() != dev.sub.Endpoint {
		t.Fatalf("test push went to %d endpoints", fake.count())
	}
	if msg := dev.open(t, fake, 0); msg.Kind != "test" || msg.Title != "Notifications work" || msg.URL != "/" {
		t.Errorf("message = %+v", msg)
	}
	if got := fake.reqs[0].Header.Get("Urgency"); got != "high" {
		t.Errorf("Urgency = %q", got)
	}

	if rec := doJSON(t, srv, http.MethodPost, "/api/push/test", map[string]string{"endpoint": "https://fcm.googleapis.com/nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown endpoint: %d", rec.Code)
	}

	fake.status = http.StatusInternalServerError
	if rec := doJSON(t, srv, http.MethodPost, "/api/push/test", map[string]string{"endpoint": dev.sub.Endpoint}); rec.Code != http.StatusBadGateway {
		t.Errorf("push service error: %d", rec.Code)
	}
	if _, ok := push.Lookup(dev.sub.Endpoint); !ok {
		t.Error("a failed (not gone) push removed the device")
	}

	fake.status = http.StatusGone
	if rec := doJSON(t, srv, http.MethodPost, "/api/push/test", map[string]string{"endpoint": dev.sub.Endpoint}); rec.Code != http.StatusGone {
		t.Errorf("gone: %d", rec.Code)
	}
	if _, ok := push.Lookup(dev.sub.Endpoint); ok {
		t.Error("a gone device wasn't removed")
	}
	if _, ok := push.Lookup(other.sub.Endpoint); !ok {
		t.Error("removing one device removed another")
	}
}

func TestPushPresence(t *testing.T) {
	srv, push, _, clock, _ := newPushTestServer(t)
	dev := newTestDevice(t, "https://fcm.googleapis.com/fcm/send/phone")
	_ = push.Subscribe(dev.sub, PushEvents{Input: true}, "")

	beat := func(window string) int {
		return doJSON(t, srv, http.MethodPost, "/api/push/presence", map[string]string{"endpoint": dev.sub.Endpoint, "window": window}).Code
	}
	if code := beat("@5"); code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", code)
	}
	if !push.Present(dev.sub.Endpoint, "@5") || push.Present(dev.sub.Endpoint, "@6") {
		t.Error("presence after a heartbeat")
	}
	clock.t = clock.t.Add(presenceTTL + time.Second)
	if push.Present(dev.sub.Endpoint, "@5") {
		t.Error("presence didn't expire")
	}

	beat("@5")
	beat("")
	if push.Present(dev.sub.Endpoint, "@5") {
		t.Error("an empty window didn't clear presence")
	}
	if code := beat("../etc"); code != http.StatusBadRequest {
		t.Errorf("bad window id: %d", code)
	}

	push.Heartbeat("https://fcm.googleapis.com/stranger", "@5")
	if push.Present("https://fcm.googleapis.com/stranger", "@5") {
		t.Error("an unknown endpoint's heartbeat was kept")
	}
}

func TestPushDisabled(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	if rec := doJSON(t, srv, http.MethodGet, "/api/push", nil); strings.TrimSpace(rec.Body.String()) != `{"enabled":false}` {
		t.Errorf("/api/push = %s", rec.Body)
	}
	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, "/api/push/subscriptions?endpoint=x"},
		{http.MethodPost, "/api/push/subscriptions"},
		{http.MethodDelete, "/api/push/subscriptions"},
		{http.MethodPost, "/api/push/test"},
		{http.MethodPost, "/api/push/presence"},
	} {
		if rec := doJSON(t, srv, tc.method, tc.url, "{}"); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", tc.method, tc.url, rec.Code)
		}
	}
}

func TestPushTopic(t *testing.T) {
	for in, want := range map[string]string{"@12": "w12", "": "w", "@" + strings.Repeat("9", 40): "w" + strings.Repeat("9", 31)} {
		if got := pushTopic(in); got != want {
			t.Errorf("pushTopic(%q) = %q, want %q", in, got, want)
		}
	}
}
