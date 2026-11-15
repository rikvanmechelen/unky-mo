package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedStaticWithoutLogin(t *testing.T) {
	h, _, _ := newLoginServer(t)
	dest := func(d, mode string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Dest", d)
			r.Header.Set("Sec-Fetch-Mode", mode)
		}
	}
	cases := []struct {
		path, dest, mode string
		want             int
	}{
		{"/chat?window=@1", "iframe", "navigate", http.StatusOK},
		{"/", "iframe", "navigate", http.StatusOK},
		{"/common.js", "script", "no-cors", http.StatusOK},
		{"/style.css", "style", "no-cors", http.StatusOK},
		{"/chat?window=@1", "document", "navigate", http.StatusSeeOther},
		{"/api/state", "iframe", "navigate", http.StatusSeeOther}, // not served: to the login
		{"/api/state", "script", "no-cors", http.StatusUnauthorized},
		{"/ca.crt", "iframe", "navigate", http.StatusSeeOther},
		{"/api/state", "empty", "cors", http.StatusUnauthorized},
	}
	for _, c := range cases {
		rec := loginDo(h, http.MethodGet, c.path, "", dest(c.dest, c.mode))
		if rec.Code != c.want {
			t.Errorf("%s as %s: %d, want %d", c.path, c.dest, rec.Code, c.want)
		}
	}
	// Without fetch metadata (curl), a page still needs the login.
	if rec := loginDo(h, http.MethodGet, "/common.js", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("/common.js from curl: %d", rec.Code)
	}
}

func issueTicket(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	rec := loginDo(h, http.MethodPost, "/api/hub/ticket", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
	if rec.Code != http.StatusOK {
		t.Fatalf("ticket: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Ticket  string
		Expires string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Ticket == "" || body.Expires == "" {
		t.Fatalf("ticket body %s", rec.Body)
	}
	return body.Ticket
}

func TestTickets(t *testing.T) {
	h, devices, c := newLoginServer(t)
	token, _ := pair(t, h)
	ticket := issueTicket(t, h, token)
	q := "?mo_ticket=" + url.QueryEscape(ticket)
	// The checks go through the same login and devices, in front of a stub,
	// so a passed login shows as a 418 rather than reaching real handlers.
	h = RequireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		testCredentials(t, "mo", "s3cret"), devices)

	// Accepted on the listed GETs (the handler may still 404: what matters
	// is that the login passed).
	for _, p := range []string{"/api/transcript/@1", "/api/sessions/@1/subagents/a1/transcript", "/api/sessions/@1/attachments/x.png", "/api/sessions/@1/tool-image/toolu_1", "/ca.crt"} {
		if rec := loginDo(h, http.MethodGet, p+q, "", nil); rec.Code != http.StatusTeapot {
			t.Errorf("%s with a ticket: %d", p, rec.Code)
		}
	}
	// Refused elsewhere, and for anything but GET.
	for _, p := range []string{"/api/state", "/api/sessions/@1/prompt", "/api/hub/devices", "/api/transcript/@1/x"} {
		if rec := loginDo(h, http.MethodGet, p+q, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a ticket: %d, want 401", p, rec.Code)
		}
	}
	if rec := loginDo(h, http.MethodDelete, "/api/sessions/@1/attachments/x.png"+q, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE with a ticket: %d", rec.Code)
	}
	// Tampered.
	bad := ticket[:len(ticket)-2] + "AA"
	if rec := loginDo(h, http.MethodGet, "/api/transcript/@1?mo_ticket="+url.QueryEscape(bad), "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered ticket: %d", rec.Code)
	}
	// Expired.
	c.t = c.t.Add(ticketTTL + time.Minute)
	if rec := loginDo(h, http.MethodGet, "/api/transcript/@1"+q, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired ticket: %d", rec.Code)
	}
	// A revoked device's ticket.
	srv, _, _ := newLoginServerWith(t, devices)
	token, _ = pair(t, srv)
	ticket = issueTicket(t, srv, token)
	for _, d := range devices.List() {
		devices.Revoke(d.ID)
	}
	if rec := loginDo(h, http.MethodGet, "/api/transcript/@1?mo_ticket="+url.QueryEscape(ticket), "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked device's ticket: %d", rec.Code)
	}
	// Basic auth gets no ticket: a ticket names a device.
	if rec := loginDo(srv, http.MethodPost, "/api/hub/ticket", "", func(r *http.Request) { r.SetBasicAuth("mo", "s3cret") }); rec.Code != http.StatusForbidden {
		t.Errorf("ticket for basic auth: %d", rec.Code)
	}
}

func TestTicketKeySurvivesRestart(t *testing.T) {
	s, c, path := newTestDevices(t)
	_, d, _ := s.Pair("phone", "")
	ticket, _, err := s.IssueTicket(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	again := NewDeviceStore(path, c.now)
	if _, ok := again.CheckTicket(ticket); !ok {
		t.Error("a ticket from before a restart was refused")
	}
}

func TestHubCORS(t *testing.T) {
	h, _, _ := newLoginServer(t)
	shell := "https://192.168.1.20:7890"
	withOrigin := func(r *http.Request) { r.Header.Set("Origin", shell) }

	for _, p := range []string{"/api/hub/pair", "/api/hub/whoami", "/api/hub/addrs"} {
		rec := loginDo(h, http.MethodOptions, p, "", func(r *http.Request) {
			withOrigin(r)
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		})
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != shell ||
			!strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "authorization") {
			t.Errorf("preflight %s: %d %v", p, rec.Code, rec.Header())
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("preflight %s allows credentials", p)
		}
	}

	token, _ := pair(t, h)
	rec := loginDo(h, http.MethodGet, "/api/hub/whoami", "", func(r *http.Request) {
		withOrigin(r)
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != shell {
		t.Errorf("whoami cross-origin: %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	// Not for the rest of the API.
	rec = loginDo(h, http.MethodGet, "/api/hub/devices", "", func(r *http.Request) {
		withOrigin(r)
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("/api/hub/devices: %d, readable cross-origin: %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec := loginDo(h, http.MethodOptions, "/api/hub/devices", "", withOrigin); rec.Code == http.StatusNoContent {
		t.Error("/api/hub/devices answered a preflight")
	}
}

func TestCrossOriginPair(t *testing.T) {
	h, devices, _ := newLoginServer(t)
	shell := "https://192.168.1.20:7890"
	rec := loginDo(h, http.MethodPost, "/api/hub/pair", `{"username":"mo","password":"s3cret","label":"shell"}`, func(r *http.Request) {
		r.Header.Set("Origin", shell)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-origin pair: %d %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a cross-origin pair set a cookie")
	}
	if !strings.Contains(rec.Body.String(), `"token"`) {
		t.Error("no token in the body")
	}
	if d := devices.List(); len(d) != 1 || d[0].Origin != shell {
		t.Errorf("devices = %+v", d)
	}
}

func TestFrameAncestors(t *testing.T) {
	h, devices, _ := newLoginServer(t)
	if got := loginDo(h, http.MethodGet, "/api/hub/hello", "", nil).Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("no devices: %q", got)
	}
	devices.Pair("shell", "https://192.168.1.20:7890")
	devices.Pair("same", "")
	devices.Pair("bad", "https://x.example; script-src *")
	devices.Pair("dup", "https://192.168.1.20:7890")
	got := loginDo(h, http.MethodGet, "/api/hub/hello", "", nil).Header().Get("Content-Security-Policy")
	if got != "frame-ancestors 'self' https://192.168.1.20:7890" {
		t.Errorf("CSP = %q", got)
	}
}

func TestPushCORS(t *testing.T) {
	h, _, _ := newLoginServer(t)
	shell := "https://192.168.1.20:7890"
	for _, p := range []string{"/api/push", "/api/push/subscriptions", "/api/push/test", "/api/push/presence"} {
		rec := loginDo(h, http.MethodOptions, p, "", func(r *http.Request) {
			r.Header.Set("Origin", shell)
			r.Header.Set("Access-Control-Request-Method", "DELETE")
		})
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != shell ||
			!strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
			t.Errorf("preflight %s: %d %v", p, rec.Code, rec.Header())
		}
	}
	token, _ := pair(t, h)
	rec := loginDo(h, http.MethodGet, "/api/push", "", func(r *http.Request) {
		r.Header.Set("Origin", shell)
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != shell {
		t.Errorf("GET /api/push cross-origin: %d %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
