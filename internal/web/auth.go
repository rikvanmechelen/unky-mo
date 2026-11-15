package web

import (
	"context"
	"crypto/sha256"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const deviceCookie = "mo_device"

// basicVerifySlots caps concurrent basic-auth password checks.
const basicVerifySlots = 4

type deviceKey struct{}

// requestDevice is the paired device a request logged in as, if any.
func requestDevice(r *http.Request) (Device, bool) {
	d, ok := r.Context().Value(deviceKey{}).(Device)
	return d, ok
}

// RequireLogin wraps h so every request must be logged in: with a paired
// device's token (the mo_device cookie, or Authorization: Bearer), or with
// basic auth matching creds. PBKDF2 is deliberately slow and the dashboard
// polls several endpoints, so a successful basic login is remembered in
// memory (keyed by a SHA-256 of the pair) and later requests skip the KDF.
// Failed attempts always pay the full hashing cost.
//
// The public paths (isPublicPath: the launcher, worker and manifest) and
// POST /api/hub/pair, which checks the password itself, need no login. A
// browser navigation without one goes to the launcher's login form, the
// page's own fetches get a plain 401, and anything else (curl) gets the
// basic challenge, so there's no native prompt in a browser.
//
// Basic auth is throttled like pairing (5 failures a minute per client IP,
// then 429), and at most basicVerifySlots password checks run at once: each
// failed guess costs a full PBKDF2, so without both a LAN host could pin
// every core or work through a dictionary.
func RequireLogin(h http.Handler, creds *Credentials, devices *DeviceStore) http.Handler {
	var mu sync.Mutex
	verified := map[[32]byte]bool{}
	var failures pairLimiter
	slots := make(chan struct{}, basicVerifySlots)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r) || (r.Method == http.MethodPost && r.URL.Path == "/api/hub/pair") ||
			(r.Method == http.MethodOptions && hubCORSPaths[r.URL.Path]) {
			h.ServeHTTP(w, r)
			return
		}
		if devices != nil && embeddedStaticRequest(r) {
			h.ServeHTTP(w, r)
			return
		}
		if devices != nil && ticketPath(r) {
			if t := r.URL.Query().Get("mo_ticket"); t != "" {
				if d, ok := devices.CheckTicket(t); ok {
					h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, d)))
					return
				}
			}
		}
		if devices != nil {
			// Both credentials are tried: a revoked device's cookie left in
			// the browser must not hide a valid Bearer token (the shell's
			// frame on the same address sends both).
			if c, err := r.Cookie(deviceCookie); err == nil && c.Value != "" {
				if d, touched, ok := devices.lookup(c.Value); ok {
					if touched {
						setDeviceCookie(w, r, c.Value)
					}
					h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, d)))
					return
				}
			}
			if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && v != "" {
				if d, _, ok := devices.lookup(v); ok {
					h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, d)))
					return
				}
			}
		}
		user, pass, ok := r.BasicAuth()
		if ok {
			key := sha256.Sum256([]byte(user + "\x00" + pass))
			mu.Lock()
			hit := verified[key]
			mu.Unlock()
			if !hit {
				ip, now := clientIP(r), time.Now()
				if failures.blocked(ip, now) {
					http.Error(w, "too many failed logins; wait a minute", http.StatusTooManyRequests)
					return
				}
				slots <- struct{}{}
				good := creds.Verify(user, pass)
				<-slots
				if good {
					mu.Lock()
					verified[key] = true
					mu.Unlock()
					hit = true
				} else {
					failures.fail(ip, now)
				}
			}
			if hit {
				h.ServeHTTP(w, r)
				return
			}
		}
		switch r.Header.Get("Sec-Fetch-Mode") {
		case "navigate":
			if devices != nil {
				http.Redirect(w, r, "/hub?login=1&next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
		case "":
		default:
			if devices != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="mo web", charset="UTF-8"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// embeddedStaticRequest reports a request for one of the dashboard's static files
// from a page that embeds it (docs/plans/pwa.md step 4): an iframe's first
// load and its scripts and styles can't carry the token, and they're the
// repo's own files. Data (/api, /ca.crt) still needs a login, and so does a
// top-level page, which goes to the login form instead.
func embeddedStaticRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ca.crt" {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "iframe", "frame", "script", "style", "image", "font", "manifest":
		return true
	}
	return false
}

// setDeviceCookie keeps a device logged in on this address. HttpOnly, so
// no script on the page can read the token.
func setDeviceCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(deviceIdle / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearDeviceCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
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
