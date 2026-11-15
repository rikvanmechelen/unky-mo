package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The launcher ("core", docs/plans/pwa.md) is the part of the app that
// works offline: the installed app's service worker keeps a copy and
// answers with it when the address doesn't. Its files live under /hub so
// they never shadow a dashboard file, and they're public: static code that
// has to load before anyone has logged in.

// coreFiles maps each core URL to its embedded file.
var coreFiles = map[string]string{
	"/hub":              "hub.html",
	"/hub/hub.css":      "hub.css",
	"/hub/hub.js":       "hub.js",
	"/hub/hub-model.js": "hub-model.js",
	"/hub/theme.js":     "theme.js",
	"/hub/idle.svg":     "favicon/idle.svg",
}

type coreFile struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

// hubCore is GET /api/hub/core: everything a service worker needs to
// install this machine's launcher, in one document.
type hubCore struct {
	Release  int                 `json:"release"` // CORE_RELEASE in hub-model.js
	Hash     string              `json:"hash"`
	SW       int                 `json:"sw"`       // SW_VERSION in sw.js
	Manifest int                 `json:"manifest"` // mo_version in manifest.json
	Protocol int                 `json:"protocol"`
	Entry    string              `json:"entry"`
	Files    map[string]coreFile `json:"files"`
	body     []byte
}

var (
	coreReleaseRE = regexp.MustCompile(`(?m)^const CORE_RELEASE = (\d+);`)
	swVersionRE   = regexp.MustCompile(`(?m)^const SW_VERSION = (\d+);`)
)

var loadCore = sync.OnceValues(func() (*hubCore, error) {
	h := embeddedStatic()
	if h == nil {
		return nil, errors.New("no embedded files")
	}
	asset := func(name string) ([]byte, error) {
		a, ok := h.assets[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		return a.raw, nil
	}
	c := &hubCore{Protocol: HubProtocol, Entry: "/hub", Files: map[string]coreFile{}}
	sum := sha256.New()
	urls := make([]string, 0, len(coreFiles))
	for u := range coreFiles {
		urls = append(urls, u)
	}
	slices.Sort(urls)
	for _, u := range urls {
		raw, err := asset(coreFiles[u])
		if err != nil {
			return nil, err
		}
		typ := mime.TypeByExtension(path.Ext(coreFiles[u]))
		c.Files[u] = coreFile{Type: typ, Body: string(raw)}
		fmt.Fprintf(sum, "%s\x00%s\x00%d\x00", u, typ, len(raw))
		sum.Write(raw)
	}
	c.Hash = hex.EncodeToString(sum.Sum(nil)[:16])

	var err error
	if c.Release, err = intFrom(asset, "hub-model.js", coreReleaseRE); err != nil {
		return nil, err
	}
	if c.SW, err = intFrom(asset, "sw.js", swVersionRE); err != nil {
		return nil, err
	}
	raw, err := asset("manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		Version int `json:"mo_version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version < 1 {
		return nil, fmt.Errorf("manifest.json has no mo_version")
	}
	c.Manifest = m.Version
	if c.body, err = json.Marshal(c); err != nil {
		return nil, err
	}
	return c, nil
})

func intFrom(asset func(string) ([]byte, error), name string, re *regexp.Regexp) (int, error) {
	raw, err := asset(name)
	if err != nil {
		return 0, err
	}
	m := re.FindSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("%s doesn't declare %s", name, re)
	}
	return strconv.Atoi(string(m[1]))
}

func (s *Server) handleHubCore(w http.ResponseWriter, r *http.Request) {
	c, err := loadCore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+c.Hash+`"`)
	if r.Header.Get("If-None-Match") == `"`+c.Hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(c.body)
}

func (s *Server) handleHubFile(w http.ResponseWriter, r *http.Request) {
	name, ok := coreFiles[r.URL.Path]
	h := embeddedStatic()
	if !ok || h == nil {
		http.NotFound(w, r)
		return
	}
	h.serve(w, r, name)
}

// isPublicPath reports whether r may be served without a login: the
// installed app's manifest, worker and icons, the launcher's files, and the
// two hub endpoints it calls before anyone has logged in. Static code and a
// machine id only; never data.
func isPublicPath(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	switch p {
	case "/manifest.json", "/sw.js", "/api/hub/hello", "/api/hub/core":
		return true
	}
	if _, ok := coreFiles[p]; ok {
		return true
	}
	for _, dir := range []string{"/icons/", "/favicon/"} {
		if strings.HasPrefix(p, dir) && len(p) > len(dir) && !strings.Contains(p, "..") {
			return true
		}
	}
	return false
}
