package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newLoginServer(t *testing.T) (http.Handler, *DeviceStore, *clock) {
	t.Helper()
	creds := testCredentials(t, "mo", "s3cret")
	devices, c, _ := newTestDevices(t)
	srv := NewServer(Deps{Hub: testHub(), Devices: devices, Login: creds}, 0, "test")
	return RequireLogin(srv, creds, devices), devices, c
}

func loginDo(h http.Handler, method, path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:5555"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if edit != nil {
		edit(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func pair(t *testing.T, h http.Handler) (token string, cookie *http.Cookie) {
	t.Helper()
	rec := loginDo(h, http.MethodPost, "/api/hub/pair", `{"username":"mo","password":"s3cret","label":"Pixel"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Token  string
		Device struct{ ID, Label string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mo_device" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != body.Token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 || cookie.Path != "/" {
		t.Fatalf("pair cookie = %+v", cookie)
	}
	return body.Token, cookie
}

func TestLoginCredentialKinds(t *testing.T) {
	h, _, _ := newLoginServer(t)
	token, cookie := pair(t, h)

	ok := map[string]func(*http.Request){
		"cookie": func(r *http.Request) { r.AddCookie(cookie) },
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) },
		"basic":  func(r *http.Request) { r.SetBasicAuth("mo", "s3cret") },
	}
	for name, edit := range ok {
		if rec := loginDo(h, http.MethodGet, "/api/hub/addrs", "", edit); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	bad := map[string]func(*http.Request){
		"bad cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "mo_device", Value: "nope"}) },
		"bad bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		"bad basic":  func(r *http.Request) { r.SetBasicAuth("mo", "wrong") },
	}
	for name, edit := range bad {
		if rec := loginDo(h, http.MethodGet, "/api/hub/addrs", "", edit); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
	}
}

func TestLoginRefusalKinds(t *testing.T) {
	h, _, _ := newLoginServer(t)

	// A browser navigation goes to the launcher's login, no native prompt.
	rec := loginDo(h, http.MethodGet, "/chat?window=@3", "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") })
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/hub?login=1&next=%2Fchat%3Fwindow%3D%403" {
		t.Errorf("navigation: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("navigation got a basic challenge")
	}

	// The page's own fetches: a plain 401.
	rec = loginDo(h, http.MethodGet, "/api/state", "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "cors") })
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("fetch: %d, challenge %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	// curl: the basic challenge, as before.
	rec = loginDo(h, http.MethodGet, "/api/state", "", nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("curl: %d, challenge %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	// Public paths need nothing.
	if rec := loginDo(h, http.MethodGet, "/api/hub/hello", "", nil); rec.Code != http.StatusOK {
		t.Errorf("hello: %d", rec.Code)
	}
}

func TestLoginRevokedAndExpired(t *testing.T) {
	h, devices, c := newLoginServer(t)
	token, cookie := pair(t, h)
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }

	// Expired after 90 idle days.
	c.t = c.t.Add(deviceIdle + 1)
	if rec := loginDo(h, http.MethodGet, "/api/hub/whoami", "", withCookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired: %d", rec.Code)
	}

	token, cookie = pair(t, h)
	id := devices.List()[0].ID
	devices.Revoke(id)
	if rec := loginDo(h, http.MethodGet, "/api/hub/whoami", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked: %d", rec.Code)
	}
}

func TestPairEndpoint(t *testing.T) {
	h, devices, _ := newLoginServer(t)

	rec := loginDo(h, http.MethodPost, "/api/hub/pair", `{"username":"mo","password":"wrong","label":"x"}`, nil)
	if rec.Code != http.StatusUnauthorized || len(devices.List()) != 0 {
		t.Errorf("wrong password: %d, %d devices", rec.Code, len(devices.List()))
	}
	for _, body := range []string{`{`, `{"username":"mo"}`, `{"username":"mo","password":"s3cret","label":"` + strings.Repeat("x", 200) + `"}`} {
		if rec := loginDo(h, http.MethodPost, "/api/hub/pair", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("pair %q: %d, want 400", body[:min(len(body), 30)], rec.Code)
		}
	}

	// Another site may not pair (yet: the shell comes in step 4).
	rec = loginDo(h, http.MethodPost, "/api/hub/pair", `{"username":"mo","password":"s3cret","label":"x"}`, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin pair: %d", rec.Code)
	}
	// Its own origin may.
	rec = loginDo(h, http.MethodPost, "/api/hub/pair", `{"username":"mo","password":"s3cret","label":"x"}`, func(r *http.Request) {
		r.Header.Set("Origin", "http://example.com")
	})
	if rec.Code != http.StatusOK {
		t.Errorf("same-origin pair: %d %s", rec.Code, rec.Body)
	}
}

func TestPairRateLimit(t *testing.T) {
	h, _, c := newLoginServer(t)
	wrong := `{"username":"mo","password":"wrong","label":"x"}`
	for i := 0; i < pairFailuresPerMinute; i++ {
		if rec := loginDo(h, http.MethodPost, "/api/hub/pair", wrong, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	right := `{"username":"mo","password":"s3cret","label":"x"}`
	if rec := loginDo(h, http.MethodPost, "/api/hub/pair", right, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("past the limit: %d, want 429 even with the right password", rec.Code)
	}
	// Another client isn't affected.
	if rec := loginDo(h, http.MethodPost, "/api/hub/pair", right, func(r *http.Request) { r.RemoteAddr = "10.0.0.10:1" }); rec.Code != http.StatusOK {
		t.Errorf("other client: %d", rec.Code)
	}
	c.t = c.t.Add(61e9)
	if rec := loginDo(h, http.MethodPost, "/api/hub/pair", right, nil); rec.Code != http.StatusOK {
		t.Errorf("a minute later: %d", rec.Code)
	}
}

func TestDevicesEndpoints(t *testing.T) {
	h, devices, _ := newLoginServer(t)
	_, mine := pair(t, h)
	_, _ = pair(t, h)
	withCookie := func(r *http.Request) { r.AddCookie(mine) }

	rec := loginDo(h, http.MethodGet, "/api/hub/whoami", "", withCookie)
	var who struct{ Device *struct{ ID, Label string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != http.StatusOK || who.Device == nil || who.Device.Label != "Pixel" {
		t.Fatalf("whoami: %d %s", rec.Code, rec.Body)
	}
	myID := who.Device.ID

	rec = loginDo(h, http.MethodGet, "/api/hub/devices", "", withCookie)
	var list struct {
		Devices []struct {
			ID      string
			Current bool
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Devices) != 2 {
		t.Fatalf("devices: %s", rec.Body)
	}
	var other string
	for _, d := range list.Devices {
		if d.Current != (d.ID == myID) {
			t.Errorf("current flag wrong on %+v", d)
		}
		if d.ID != myID {
			other = d.ID
		}
	}
	if strings.Contains(rec.Body.String(), "hash") {
		t.Error("the listing exposes hashes")
	}

	if rec := loginDo(h, http.MethodDelete, "/api/hub/devices/"+other, "", withCookie); rec.Code != http.StatusNoContent {
		t.Errorf("revoke other: %d", rec.Code)
	}
	if rec := loginDo(h, http.MethodDelete, "/api/hub/devices/"+other, "", withCookie); rec.Code != http.StatusNotFound {
		t.Errorf("revoke again: %d", rec.Code)
	}

	rec = loginDo(h, http.MethodPost, "/api/hub/logout", "", withCookie)
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == "mo_device" && c.MaxAge < 0)
	}
	if !cleared || len(devices.List()) != 0 {
		t.Errorf("logout: cookie cleared %v, %d devices left", cleared, len(devices.List()))
	}
}

func TestDeviceEndpointsWithoutLogin(t *testing.T) {
	srv := NewServer(Deps{Hub: testHub()}, 0, "test")
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/hub/pair"}, {http.MethodGet, "/api/hub/whoami"}, {http.MethodGet, "/api/hub/devices"},
		{http.MethodDelete, "/api/hub/devices/abc"}, {http.MethodPost, "/api/hub/logout"},
	} {
		if rec := loginDo(srv, r.method, r.path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s without a login: %d", r.method, r.path, rec.Code)
		}
	}
}
