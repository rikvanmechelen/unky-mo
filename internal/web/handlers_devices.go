package web

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

// Pairing and the paired-device list (docs/plans/pwa.md step 3). All of it
// is 404 without a login configured: tokens only exist alongside one.

const (
	pairFailuresPerMinute = 5
	maxDeviceLabel        = 80
)

// pairLimiter counts failed pairings per client IP over the last minute.
type pairLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func (l *pairLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.failures, ip)
		return false
	}
	l.failures[ip] = recent
	return len(recent) >= pairFailuresPerMinute
}

func (l *pairLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[string][]time.Time{}
	}
	l.failures[ip] = append(l.failures[ip], now)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sameOrigin reports whether a request's Origin (when it has one) is the
// server it was sent to.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}

func (s *Server) devicesEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.deps.Devices == nil || s.deps.Login == nil {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	now := s.deps.Devices.now()
	ip := clientIP(r)
	if s.pairs.blocked(ip, now) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many failed logins; wait a minute"))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		body.Username == "" || body.Password == "" || !utf8.ValidString(body.Label) || len(body.Label) > maxDeviceLabel {
		writeError(w, http.StatusBadRequest, errors.New("send {username, password, label}"))
		return
	}
	if !s.deps.Login.Verify(body.Username, body.Password) {
		s.pairs.fail(ip, now)
		writeError(w, http.StatusUnauthorized, errors.New("wrong username or password"))
		return
	}
	label := stripControl(body.Label)
	if label == "" {
		label = "unnamed device"
	}
	token, d, err := s.deps.Devices.Pair(label, r.Header.Get("Origin"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	// Another origin (the installed app's shell) keeps the token itself: a
	// cookie would be a third-party one.
	if sameOrigin(r) {
		setDeviceCookie(w, r, token)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"token": token, "device": deviceView(d, d.ID)})
}

func deviceView(d Device, current string) map[string]any {
	return map[string]any{
		"id":       d.ID,
		"label":    d.Label,
		"created":  d.Created.UTC().Format(time.RFC3339),
		"lastUsed": d.LastUsed.UTC().Format(time.RFC3339),
		"current":  d.ID == current,
	}
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	d, ok := requestDevice(r)
	if !ok {
		writeJSON(w, map[string]any{"device": nil})
		return
	}
	writeJSON(w, map[string]any{"device": map[string]string{"id": d.ID, "label": d.Label}})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	current, _ := requestDevice(r)
	out := []map[string]any{}
	for _, d := range s.deps.Devices.List() {
		out = append(out, deviceView(d, current.ID))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"devices": out})
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	id := r.PathValue("id")
	if !s.deps.Devices.Revoke(id) {
		http.NotFound(w, r)
		return
	}
	if d, ok := requestDevice(r); ok && d.ID == id {
		clearDeviceCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	if d, ok := requestDevice(r); ok {
		s.deps.Devices.Revoke(d.ID)
	}
	clearDeviceCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}
