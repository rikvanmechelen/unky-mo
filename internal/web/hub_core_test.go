package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// coreReleases records each launcher release's file hash. When a core file
// changes, raise CORE_RELEASE in static/hub-model.js and add its hash here:
// installed apps only take a core whose release is higher than theirs, so a
// change under the same release would never reach them.
var coreReleases = map[int]string{
	1: "e4210beeac92f961c84e32bca8a87ffd",
	2: "d8e2c4d2810618f7056a442b66ebccc0",
}

func TestCoreReleaseTable(t *testing.T) {
	c, err := loadCore()
	if err != nil {
		t.Fatal(err)
	}
	want, ok := coreReleases[c.Release]
	if !ok || want != c.Hash {
		t.Fatalf("core files hash to %s under CORE_RELEASE %d (table: %q). If a core file changed, raise CORE_RELEASE in static/hub-model.js and add {%d: %q} to coreReleases.",
			c.Hash, c.Release, want, c.Release+1, c.Hash)
	}
}

func TestHubCore(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hub/core", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the core must be readable from another machine's origin")
	}
	var c struct {
		Release  int    `json:"release"`
		Hash     string `json:"hash"`
		SW       int    `json:"sw"`
		Manifest int    `json:"manifest"`
		Protocol int    `json:"protocol"`
		Entry    string `json:"entry"`
		Files    map[string]struct {
			Type string `json:"type"`
			Body string `json:"body"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Release < 1 || c.SW < 1 || c.Manifest < 1 || c.Protocol != HubProtocol || c.Entry != "/hub" || c.Hash == "" {
		t.Errorf("core header = %+v", c)
	}
	for _, p := range []string{"/hub", "/hub/hub.js", "/hub/hub-model.js", "/hub/hub.css", "/hub/theme.js", "/hub/idle.svg"} {
		f, ok := c.Files[p]
		if !ok || f.Body == "" || f.Type == "" {
			t.Errorf("core file %s missing or empty", p)
		}
		if p != "/hub" && !strings.HasPrefix(p, "/hub/") {
			t.Errorf("core file %s outside /hub", p)
		}
	}
	if !strings.Contains(c.Files["/hub"].Type, "text/html") {
		t.Errorf("/hub type %q", c.Files["/hub"].Type)
	}
	if et := rec.Header().Get("ETag"); et != `"`+c.Hash+`"` {
		t.Errorf("ETag %q, want the hash", et)
	}

	// The same files are served at their paths.
	for p, f := range c.Files {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != f.Body {
			t.Errorf("GET %s: status %d, body differs: %v", p, rec.Code, rec.Body.String() != f.Body)
		}
	}
}

func TestPublicPaths(t *testing.T) {
	public := []string{
		"/manifest.json", "/sw.js", "/icons/icon-192.png", "/favicon/idle.svg",
		"/hub", "/hub/hub.js", "/hub/hub-model.js", "/hub/hub.css", "/hub/theme.js", "/hub/idle.svg",
		"/api/hub/hello", "/api/hub/core",
	}
	private := []string{
		"/", "/chat", "/branch", "/index.html", "/chat.html", "/app.js", "/common.js", "/style.css", "/theme.js",
		"/api/hub/addrs", "/api/state", "/api/hub", "/hubx", "/hub.js", "/ca.crt", "/api/tls",
		"/icons", "/favicon", "/api/hub/hello/x", "/api/transcript/@1",
	}
	for _, p := range public {
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			if !isPublicPath(httptest.NewRequest(m, p, nil)) {
				t.Errorf("%s %s should be public", m, p)
			}
		}
		if isPublicPath(httptest.NewRequest(http.MethodPost, p, nil)) {
			t.Errorf("POST %s must not be public", p)
		}
	}
	for _, p := range private {
		if isPublicPath(httptest.NewRequest(http.MethodGet, p, nil)) {
			t.Errorf("GET %s must not be public", p)
		}
	}
}

func TestBasicAuthLetsPublicPathsThrough(t *testing.T) {
	old := hashIterations
	hashIterations = 1
	t.Cleanup(func() { hashIterations = old })
	creds, err := NewCredentials("rik", "pw")
	if err != nil {
		t.Fatal(err)
	}
	h := RequireLogin(NewServer(Deps{Hub: testHub()}, 0, "test"), creds, nil)
	for path, want := range map[string]int{
		"/api/hub/hello": http.StatusOK,
		"/api/hub/core":  http.StatusOK,
		"/hub":           http.StatusOK,
		"/sw.js":         http.StatusOK,
		"/api/hub/addrs": http.StatusUnauthorized,
		"/":              http.StatusUnauthorized,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s without a login: %d, want %d", path, rec.Code, want)
		}
	}
}

func TestHubProtocolMatchesLauncher(t *testing.T) {
	h := embeddedStatic()
	m := regexp.MustCompile(`(?m)^const HUB_PROTOCOL = (\d+);`).FindSubmatch(h.assets["hub-model.js"].raw)
	if m == nil || string(m[1]) != strconv.Itoa(HubProtocol) {
		t.Fatalf("hub-model.js HUB_PROTOCOL = %q, HubProtocol = %d: they must agree", m, HubProtocol)
	}
}
