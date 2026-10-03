package web

import (
	"crypto/sha256"
	"net"
	"net/http"
	"sync"
)

// BasicAuth wraps h so every request must carry HTTP basic auth credentials
// matching creds. PBKDF2 is deliberately slow and the dashboard polls
// several endpoints, so a successful login is remembered in memory (keyed
// by a SHA-256 of the pair) and later requests skip the KDF. Failed
// attempts always pay the full hashing cost.
func BasicAuth(h http.Handler, creds *Credentials) http.Handler {
	var mu sync.Mutex
	verified := map[[32]byte]bool{}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok {
			key := sha256.Sum256([]byte(user + "\x00" + pass))
			mu.Lock()
			hit := verified[key]
			mu.Unlock()
			if !hit && creds.Verify(user, pass) {
				mu.Lock()
				verified[key] = true
				mu.Unlock()
				hit = true
			}
			if hit {
				h.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="mo web", charset="UTF-8"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// IsLoopbackAddr reports whether a listen address (host:port) only accepts
// connections from this machine. An empty host ("":7890) binds every
// interface, so it is not loopback.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
