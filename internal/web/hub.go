package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The hub endpoints let an installed app find this machine on whatever
// network it's on (docs/plans/pwa.md): /api/hub/hello says which machine
// answers at an address, /api/hub/addrs where else it can be reached.

// HubProtocol is the version of the hub API and the app's messages. A core
// works with machines one version older or newer than its own.
const HubProtocol = 1

// HubInfo identifies this machine to the app.
type HubInfo struct {
	Machine string // MachineID: stable across addresses and restarts
	Name    string // the hostname, for people
	Version string // mo's build version
	// Addrs lists the URLs the machine can be reached at right now.
	Addrs func() []string
}

// MachineID reads this machine's id from <dir>/machine-id, creating it on
// first use. It lives in the state dir rather than the config dir, since a
// config dir shared through dotfiles would give two machines one id. A file
// that isn't a valid id is an error and is never replaced: a new id makes
// every device see a new machine.
func MachineID(dir string) (string, error) {
	path := filepath.Join(dir, "machine-id")
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if !isHexID(id) {
			return "", fmt.Errorf("%s doesn't hold a machine id; remove it to make a new one", path)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	tmp, err := os.CreateTemp(dir, ".machine-id-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return id, os.Rename(tmp.Name(), path)
}

// isHexID reports whether s is 32 lowercase hex characters.
func isHexID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// HubAddrs lists the URLs another device can reach a server listening on
// listenAddr at, from the names and IPs its cert covers (TLSHosts). Loopback
// names are left out. An explicit listen host is the only address.
func HubAddrs(scheme, listenAddr string, hosts []string) []string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil
	}
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		hosts = []string{host}
	}
	var out []string
	for _, h := range hosts {
		if h == "localhost" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			continue
		}
		out = append(out, scheme+"://"+net.JoinHostPort(h, port))
	}
	return out
}

// hubCORSPaths can be called from another origin: the installed app's
// shell, served from wherever it was installed, talking to this machine.
// Without credentials mode, so cookies never cross; the shell sends its
// own token.
var hubCORSPaths = map[string]bool{
	"/api/hub/pair":           true,
	"/api/hub/whoami":         true,
	"/api/hub/addrs":          true,
	"/api/push":               true, // push through the shell (step 6)
	"/api/push/subscriptions": true,
	"/api/push/test":          true,
	"/api/push/presence":      true,
}

var originRE = regexp.MustCompile(`^https?://[a-z0-9.\-]+(:\d{1,5})?$|^https?://\[[0-9a-f:.]+\](:\d{1,5})?$`)

// scriptPolicy lets the pages run only the dashboard's own script files:
// no inline script, no eval, no plugins, no <base> pointing elsewhere.
// Transcripts, files and tool output are attacker-influenced text that the
// pages only ever insert as text; this is the second layer if a mistake
// ever lets one in as markup. The launcher shares the origin and holds
// every machine's token, so it matters most there. Connections and frames
// stay open: the launcher talks to and frames other machines.
const scriptPolicy = "script-src 'self'; object-src 'none'; base-uri 'self'"

// frameAncestors is the Content-Security-Policy that lets only this origin
// and the shells devices paired from frame the dashboard.
func (s *Server) frameAncestors() string {
	policy := "frame-ancestors 'self'"
	if s.deps.Devices == nil {
		return policy
	}
	seen := map[string]bool{}
	for _, d := range s.deps.Devices.List() {
		o := strings.ToLower(d.Origin)
		if o == "" || seen[o] || !originRE.MatchString(o) {
			continue
		}
		seen[o] = true
		policy += " " + o
	}
	return policy
}

func (s *Server) handleHubHello(w http.ResponseWriter, r *http.Request) {
	hub := s.deps.Hub
	if hub == nil {
		http.NotFound(w, r)
		return
	}
	// Any page may ask: the installed app probes from its own origin, which
	// is usually another address of this machine or another machine.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"app":      "unky-mo",
		"machine":  hub.Machine,
		"name":     hub.Name,
		"version":  hub.Version,
		"protocol": HubProtocol,
	})
}

func (s *Server) handleHubAddrs(w http.ResponseWriter, r *http.Request) {
	hub := s.deps.Hub
	if hub == nil {
		http.NotFound(w, r)
		return
	}
	addrs := []string{}
	if hub.Addrs != nil {
		if a := hub.Addrs(); a != nil {
			addrs = a
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"addrs": addrs})
}
