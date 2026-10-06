// Package webpush sends Web Push messages: the payload encryption of RFC 8291
// (aes128gcm, RFC 8188) and the VAPID authentication of RFC 8292, over the
// standard library only. It knows nothing about sessions; mo web's notifier
// decides what to send and to whom (see docs/plans/notifications.md).
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxPayload caps a message's plaintext. One aes128gcm record of 4096 bytes
// holds 4079; the cap leaves room and keeps notifications small.
const MaxPayload = 3072

// recordSize is the aes128gcm record size written into the header.
const recordSize = 4096

// vapidSubject is the VAPID "sub" claim: who runs this application server.
// It's sent to the push services, so it names the project rather than a
// person; Apple rejects a sub that isn't a mailto: or https: URL.
const vapidSubject = "https://github.com/rvanmech/unky-mo"

// ErrGone means the push service no longer knows the subscription (404 or
// 410): the browser unsubscribed or the subscription expired. Drop it.
var ErrGone = errors.New("push subscription is gone")

var b64 = base64.RawURLEncoding

// decodeB64 accepts base64url with or without padding, which is how browsers
// and specs variously write keys.
func decodeB64(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// Keys is the application server's VAPID key pair.
type Keys struct {
	priv *ecdsa.PrivateKey
}

// LoadOrCreateKeys reads the VAPID private key at path, creating it (dir
// 0700, file 0600) when the file doesn't exist. A file that exists but
// doesn't parse is an error and is left alone: a new key would silently
// invalidate every device's subscription.
func LoadOrCreateKeys(path string) (*Keys, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			return nil, false, fmt.Errorf("%s: not a PEM EC private key", path)
		}
		priv, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		if priv.Curve != elliptic.P256() {
			return nil, false, fmt.Errorf("%s: not a P-256 key", path)
		}
		return &Keys{priv: priv}, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vapid-*")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, false, err
	}
	return &Keys{priv: priv}, true, nil
}

// publicBytes is the uncompressed public point (65 bytes).
func (k *Keys) publicBytes() []byte {
	pub, err := k.priv.PublicKey.ECDH()
	if err != nil {
		return nil
	}
	return pub.Bytes()
}

// PublicKey is the VAPID public key as base64url: what the browser passes
// to PushManager.subscribe as applicationServerKey.
func (k *Keys) PublicKey() string { return b64.EncodeToString(k.publicBytes()) }

// Subscription is a browser's PushSubscription.toJSON().
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Validate checks the endpoint against the allowlist and the keys' sizes.
func (s Subscription) Validate() error {
	if err := CheckEndpoint(s.Endpoint); err != nil {
		return err
	}
	if _, _, err := s.keys(); err != nil {
		return err
	}
	return nil
}

func (s Subscription) keys() (*ecdh.PublicKey, []byte, error) {
	p, err := decodeB64(s.Keys.P256dh)
	if err != nil {
		return nil, nil, fmt.Errorf("p256dh: %w", err)
	}
	pub, err := ecdh.P256().NewPublicKey(p)
	if err != nil {
		return nil, nil, fmt.Errorf("p256dh: %w", err)
	}
	auth, err := decodeB64(s.Keys.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: %w", err)
	}
	if len(auth) != 16 {
		return nil, nil, fmt.Errorf("auth: %d bytes, want 16", len(auth))
	}
	return pub, auth, nil
}

// pushHosts are the push services a subscription may point at. The server
// POSTs to whatever endpoint a browser hands it, so anything else is refused:
// otherwise a logged-in browser could make mo web POST to internal URLs.
var pushHosts = []string{
	"fcm.googleapis.com",        // Chrome, Edge on Android
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari, iOS Home Screen apps
	"notify.windows.com",        // Edge on Windows
}

// CheckEndpoint refuses anything but an https URL on a known push service.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if u.Scheme != "https" || u.User != nil || u.Host == "" {
		return errors.New("endpoint must be a plain https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("endpoint must use port 443")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pushHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("endpoint host %q isn't a known push service", host)
}

// Encrypt encrypts plaintext for a subscription with a fresh ephemeral key
// and salt, returning the aes128gcm body.
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	uaPub, auth, err := sub.keys()
	if err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(plaintext, uaPub, auth, asPriv, salt)
}

// encrypt is RFC 8291 §3.4 with one RFC 8188 record. The keys and salt are
// parameters so tests can replay the RFC's example.
func encrypt(plaintext []byte, uaPub *ecdh.PublicKey, auth []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("payload is %d bytes, max %d", len(plaintext), MaxPayload)
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()

	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPub.Bytes()) + string(asPub)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	record := append(append([]byte{}, plaintext...), 0x02) // last-record delimiter
	out.Write(gcm.Seal(nil, nonce, record, nil))
	return out.Bytes(), nil
}

// vapidHeader is the Authorization header for a request to endpoint.
func (k *Keys) vapidHeader(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	aud := u.Scheme + "://" + u.Hostname() // the origin: no default port
	if p := u.Port(); p != "" && p != "443" {
		aud += ":" + p
	}
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": aud,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": vapidSubject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64) // JWS ES256: fixed-width r ‖ s, not ASN.1
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + k.PublicKey(), nil
}

// verifyJWT checks an ES256 JWT's signature against the keys (tests use it).
func (k *Keys) verifyJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	return ecdsa.Verify(&k.priv.PublicKey, sum[:], r, s)
}

// Doer is the HTTP client seam (*http.Client satisfies it).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options are the Web Push headers of one message.
type Options struct {
	TTL     time.Duration // how long the push service keeps an undelivered message
	Urgency string        // very-low, low, normal or high; "" leaves it out
	Topic   string        // replaces an undelivered message with the same topic; ≤32 base64url chars
}

// Sender sends messages signed with Keys.
type Sender struct {
	Keys   *Keys
	Client Doer
	Now    func() time.Time
}

func validTopic(t string) bool {
	if len(t) > 32 {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Send encrypts payload for sub and POSTs it to the subscription's push
// service. It returns ErrGone when the service no longer knows sub.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	if err := CheckEndpoint(sub.Endpoint); err != nil {
		return err
	}
	switch opts.Urgency {
	case "", "very-low", "low", "normal", "high":
	default:
		return fmt.Errorf("invalid urgency %q", opts.Urgency)
	}
	if !validTopic(opts.Topic) {
		return fmt.Errorf("invalid topic %q", opts.Topic)
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	auth, err := s.Keys.vapidHeader(sub.Endpoint, now())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(opts.TTL/time.Second)))
	if opts.Urgency != "" {
		req.Header.Set("Urgency", opts.Urgency)
	}
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return fmt.Errorf("push service answered %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}
