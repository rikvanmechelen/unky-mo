package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"
)

// URL tickets let an embedded page (docs/plans/pwa.md step 4) load what
// can't carry an Authorization header: EventSource streams, <img> sources
// and downloads. A ticket names a device and an expiry, signed with a key
// kept next to the devices file, and is only accepted on a few GETs while
// its device is still paired.

const ticketTTL = 12 * time.Hour

// ticketPaths are the only requests a ticket logs in.
var ticketPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/api/transcript/[^/]+$`),
	regexp.MustCompile(`^/api/sessions/[^/]+/subagents/[^/]+/transcript$`),
	regexp.MustCompile(`^/api/sessions/[^/]+/attachments/[^/]+$`),
	regexp.MustCompile(`^/api/sessions/[^/]+/tool-image/[^/]+$`),
	regexp.MustCompile(`^/ca\.crt$`),
}

func ticketPath(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	for _, re := range ticketPaths {
		if re.MatchString(r.URL.Path) {
			return true
		}
	}
	return false
}

// ticketKey loads the signing key, creating it on first use. Kept on disk
// so tickets survive `mo restart`.
func (s *DeviceStore) ticketKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	path := s.path[:len(s.path)-len(".json")] + ".key"
	if raw, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(string(raw)); err == nil && len(key) == 32 {
			s.key = key
			return key, nil
		}
		return nil, errors.New(path + " isn't a ticket key; remove it to make a new one")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), devicesFileMode); err != nil {
		return nil, err
	}
	s.key = key
	return key, nil
}

func ticketMAC(key, body []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return m.Sum(nil)[:16]
}

// IssueTicket signs a ticket for a paired device.
func (s *DeviceStore) IssueTicket(deviceID string) (string, time.Time, error) {
	id, err := hex.DecodeString(deviceID)
	if err != nil || len(id) != 8 {
		return "", time.Time{}, errors.New("bad device id")
	}
	key, err := s.ticketKey()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(ticketTTL).Truncate(time.Second)
	body := make([]byte, 16)
	copy(body, id)
	binary.BigEndian.PutUint64(body[8:], uint64(expires.Unix()))
	return base64.RawURLEncoding.EncodeToString(append(body, ticketMAC(key, body)...)), expires, nil
}

// CheckTicket returns the device a valid, unexpired ticket names, if that
// device is still paired.
func (s *DeviceStore) CheckTicket(ticket string) (Device, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil || len(raw) != 32 {
		return Device{}, false
	}
	key, err := s.ticketKey()
	if err != nil {
		return Device{}, false
	}
	body, mac := raw[:16], raw[16:]
	if !hmac.Equal(mac, ticketMAC(key, body)) {
		return Device{}, false
	}
	if s.now().Unix() >= int64(binary.BigEndian.Uint64(body[8:])) {
		return Device{}, false
	}
	id := hex.EncodeToString(body[:8])
	for _, d := range s.List() {
		if d.ID == id {
			return d, true
		}
	}
	return Device{}, false
}

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	if !s.devicesEnabled(w, r) {
		return
	}
	d, ok := requestDevice(r)
	if !ok {
		writeError(w, http.StatusForbidden, errors.New("tickets are for devices that logged in with the login form"))
		return
	}
	ticket, expires, err := s.deps.Devices.IssueTicket(d.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"ticket": ticket, "expires": expires.UTC().Format(time.RFC3339)})
}
