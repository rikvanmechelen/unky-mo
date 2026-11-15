package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// csrfDo sends method path through a server with no login configured (the
// default), with the given request headers.
func csrfDo(t *testing.T, h http.Handler, method, path string, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(method, "https://127.0.0.1:7890"+path, strings.NewReader(`{}`))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestCrossSiteRefused: an unsafe request a page on another origin sent is
// refused before it reaches a handler, whatever the auth mode; requests
// from the dashboard's own pages and from curl are not.
func TestCrossSiteRefused(t *testing.T) {
	srv := NewServer(Deps{Hub: testHub()}, 0, "test")
	// /api/restart answers 404 (no Restarter) when it's reached, 403 when refused.
	const path = "/api/restart"
	refused := map[string]map[string]string{
		"cross-site fetch":            {"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"},
		"same-site, another port":     {"Sec-Fetch-Site": "same-site", "Origin": "https://127.0.0.1:3000"},
		"foreign Origin, no metadata": {"Origin": "https://evil.example"},
		"null Origin":                 {"Origin": "null"},
	}
	for name, hdr := range refused {
		if got := csrfDo(t, srv, http.MethodPost, path, hdr); got != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", name, got)
		}
	}
	allowed := map[string]map[string]string{
		"same-origin fetch": {"Sec-Fetch-Site": "same-origin", "Origin": "https://127.0.0.1:7890"},
		"typed by the user": {"Sec-Fetch-Site": "none"},
		"own Origin only":   {"Origin": "https://127.0.0.1:7890"},
		"curl (no headers)": {},
	}
	for name, hdr := range allowed {
		if got := csrfDo(t, srv, http.MethodPost, path, hdr); got == http.StatusForbidden {
			t.Errorf("%s: refused", name)
		}
	}
	// Reads are never refused here (their answers aren't readable cross-site).
	if got := csrfDo(t, srv, http.MethodGet, "/api/boot", map[string]string{"Sec-Fetch-Site": "cross-site"}); got == http.StatusForbidden {
		t.Errorf("GET refused")
	}
	// The installed app's shell pairs and subscribes from its own origin:
	// pairing carries the password, everything else its Bearer token.
	cross := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://10.0.0.2:7890"}
	if got := csrfDo(t, srv, http.MethodPost, "/api/hub/pair", cross); got == http.StatusForbidden {
		t.Errorf("cross-origin pairing refused")
	}
	withToken := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://10.0.0.2:7890", "Authorization": "Bearer abc"}
	for _, p := range []string{"/api/push/subscriptions", "/api/push/test", "/api/push/presence"} {
		if got := csrfDo(t, srv, http.MethodPost, p, withToken); got == http.StatusForbidden {
			t.Errorf("shell's %s with a token refused", p)
		}
		// A page without the token gets no exemption, whatever the method.
		for _, m := range []string{http.MethodPost, http.MethodDelete} {
			if got := csrfDo(t, srv, m, p, cross); got != http.StatusForbidden {
				t.Errorf("cross-site %s %s without a token: want 403, got %d", m, p, got)
			}
		}
	}
}

// TestCrossSiteRefusedWithLogin: with a login the cookie rides along on a
// same-site request from another port; the request is still refused.
func TestCrossSiteRefusedWithLogin(t *testing.T) {
	h, _, _ := newLoginServer(t)
	_, cookie := pair(t, h)
	rec := loginDo(h, http.MethodPost, "/api/restart", `{}`, func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.Header.Set("Origin", "https://localhost:3000")
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rec.Code)
	}
}

// TestStaleCookieDoesNotHideBearer: a revoked device's cookie doesn't stop
// a valid Bearer token on the same request.
func TestStaleCookieDoesNotHideBearer(t *testing.T) {
	h, _, _ := newLoginServer(t)
	token, _ := pair(t, h)
	rec := loginDo(h, http.MethodGet, "/api/hub/whoami", "", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: deviceCookie, Value: "revoked-or-expired"})
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}

// TestPlainHTTPHostCheck: over plain HTTP a Host the machine doesn't
// answer to (a rebound DNS name) is refused; the machine's own names pass.
func TestPlainHTTPHostCheck(t *testing.T) {
	srv := NewServer(Deps{Hub: testHub()}, 0, "test")
	srv.SetPlainHTTPHosts(func() []string { return []string{"localhost", "127.0.0.1", "::1", "box.local"} })
	for host, want := range map[string]bool{
		"127.0.0.1:7890":    true,
		"localhost:7890":    true,
		"[::1]:7890":        true,
		"BOX.local:7890":    true,
		"evil.example:7890": false,
		"evil.example":      false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/boot", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if got := rec.Code != http.StatusMisdirectedRequest; got != want {
			t.Errorf("%s: allowed=%v, want %v (status %d)", host, got, want, rec.Code)
		}
	}
}
