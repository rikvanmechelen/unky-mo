package web

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cross-site request refusal (docs/plans/review-remediation.md, step 1.1).
//
// The dashboard drives live agents, so a request that changes something
// must come from the dashboard's own pages. Without it, any web page open
// in the user's browser could POST a prompt to 127.0.0.1:7890 as a "simple"
// request (no preflight): with no login configured nothing else stops it,
// and with one, SameSite=Lax doesn't either when the page is another port
// of the same host (sites ignore ports).
//
// Unsafe methods are refused when the browser says they're cross-origin:
// Sec-Fetch-Site anything but same-origin or none (a typed URL, a
// bookmark), or, from a browser that doesn't send it, an Origin that isn't
// this server. Requests with neither header (curl, scripts) pass, as they
// can't be forged by a page. The installed app's shell calls hubCORSPaths
// from another origin by design; those carry its own Bearer token (never a
// cookie, the shell omits credentials) or a password, so they're exempt.

// errCrossSite answers an unsafe request another site's page sent.
var errCrossSite = errors.New("cross-site request refused")

func crossSiteRefused(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	// The shell's cross-origin calls carry its Bearer token, which a page
	// can't forge; pairing carries the password. A cookie alone (a same-site
	// page on another port, or no login at all) gets no exemption: it could
	// otherwise register its own push endpoint or drop the user's.
	if hubCORSPaths[r.URL.Path] && (r.URL.Path == "/api/hub/pair" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		return !sameOrigin(r)
	default: // same-site, cross-site
		return true
	}
}

// hostAllowlist answers whether a Host header names this machine. It's only
// used over plain HTTP ([web] disable_tls): there, DNS rebinding (a page on
// evil.example whose name now resolves to 127.0.0.1) would make the attacker
// page same-origin with the dashboard and let it read every answer. Over TLS
// the certificate doesn't cover the attacker's name, so the browser never
// gets that far. The list is TLSHosts (loopback names, the hostname, the
// interface addresses, extra_hosts), re-read at most every 30 seconds since
// addresses change when the network does.
type hostAllowlist struct {
	hosts func() []string
	mu    sync.Mutex
	at    time.Time
	set   map[string]bool
	now   func() time.Time
}

func newHostAllowlist(hosts func() []string) *hostAllowlist {
	return &hostAllowlist{hosts: hosts, now: time.Now}
}

func (a *hostAllowlist) allows(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.set == nil || a.now().Sub(a.at) > 30*time.Second {
		a.set = map[string]bool{}
		for _, h := range a.hosts() {
			a.set[strings.ToLower(strings.Trim(h, "[]"))] = true
		}
		a.at = a.now()
	}
	return a.set[host]
}

// SetPlainHTTPHosts turns on the Host check for a server that listens
// without TLS. hosts lists the names it answers to.
func (s *Server) SetPlainHTTPHosts(hosts func() []string) {
	s.hosts = newHostAllowlist(hosts)
}
