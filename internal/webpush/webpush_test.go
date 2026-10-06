package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unspace drops the whitespace RFC 8291 wraps its base64url values with.
func unspace(s string) string { return strings.Join(strings.Fields(s), "") }

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64(unspace(s))
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestEncryptRFC8291Example replays RFC 8291 Appendix A: fixed keys and salt
// must give exactly the published header and ciphertext.
func TestEncryptRFC8291Example(t *testing.T) {
	plaintext := mustB64(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := asPriv.PublicKey().Bytes(), mustB64(t, `BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIg
		Dll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8`); !bytes.Equal(got, want) {
		t.Fatal("as_public doesn't match the RFC")
	}
	uaPub, err := ecdh.P256().NewPublicKey(mustB64(t, `BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-
		JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4`))
	if err != nil {
		t.Fatal(err)
	}
	auth := mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := encrypt(plaintext, uaPub, auth, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	header := mustB64(t, `DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z 9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz AC8wEqKK6PBru3jl7A8`)
	ciphertext := mustB64(t, `8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs
		bI_0LpXMuGvnzQ`)
	if len(header) != 86 {
		t.Fatalf("RFC header is %d bytes", len(header))
	}
	if want := append(header, ciphertext...); !bytes.Equal(got, want) {
		t.Errorf("encrypt =\n%x\nwant\n%x", got, want)
	}
}

// decrypt opens a body with Decrypt, failing the test on error.
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	plain, err := Decrypt(body, uaPriv, auth)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return plain
}

// newSubscription makes a browser-side key pair and its subscription.
func newSubscription(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var sub Subscription
	sub.Endpoint = endpoint
	sub.Keys.P256dh = b64.EncodeToString(uaPriv.PublicKey().Bytes())
	sub.Keys.Auth = b64.EncodeToString(auth)
	return sub, uaPriv, auth
}

func TestEncryptRoundTrip(t *testing.T) {
	sub, uaPriv, auth := newSubscription(t, "https://fcm.googleapis.com/fcm/send/x")
	msg := []byte(`{"title":"unky-mo needs you"}`)
	a, err := Encrypt(sub, msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, a, uaPriv, auth); !bytes.Equal(got, msg) {
		t.Errorf("round trip = %q", got)
	}
	b, _ := Encrypt(sub, msg)
	if bytes.Equal(a[:16], b[:16]) || bytes.Equal(a[21:86], b[21:86]) {
		t.Error("salt and ephemeral key must be fresh per message")
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err == nil {
		t.Error("oversized payload accepted")
	}
}

func TestLoadOrCreateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push", "vapid.pem")
	k1, created, err := LoadOrCreateKeys(path)
	if err != nil || !created {
		t.Fatalf("create: %v, created=%v", err, created)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi.Mode(), err)
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	if pub, _ := decodeB64(k1.PublicKey()); len(pub) != 65 || pub[0] != 4 {
		t.Errorf("public key isn't an uncompressed point: %x", pub)
	}

	k2, created, err := LoadOrCreateKeys(path)
	if err != nil || created {
		t.Fatalf("reload: %v, created=%v", err, created)
	}
	if k1.PublicKey() != k2.PublicKey() {
		t.Error("reload gave a different key")
	}

	bad := filepath.Join(t.TempDir(), "vapid.pem")
	_ = os.WriteFile(bad, []byte("garbage"), 0o600)
	if _, _, err := LoadOrCreateKeys(bad); err == nil {
		t.Error("corrupt key file accepted")
	}
	if data, _ := os.ReadFile(bad); string(data) != "garbage" {
		t.Error("corrupt key file was overwritten")
	}
}

func TestVAPIDHeader(t *testing.T) {
	k, _, err := LoadOrCreateKeys(filepath.Join(t.TempDir(), "vapid.pem"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	h, err := k.vapidHeader("https://fcm.googleapis.com:443/fcm/send/abc?x=1", now)
	if err != nil {
		t.Fatal(err)
	}
	var token, key string
	for _, part := range strings.Split(strings.TrimPrefix(h, "vapid "), ", ") {
		switch {
		case strings.HasPrefix(part, "t="):
			token = part[2:]
		case strings.HasPrefix(part, "k="):
			key = part[2:]
		}
	}
	if key != k.PublicKey() {
		t.Errorf("k = %q", key)
	}
	if !k.verifyJWT(token) {
		t.Fatal("JWT signature doesn't verify")
	}
	claimsRaw, _ := b64.DecodeString(strings.Split(token, ".")[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://fcm.googleapis.com" || claims.Exp != now.Add(12*time.Hour).Unix() || claims.Sub != vapidSubject {
		t.Errorf("claims = %+v", claims)
	}
	headRaw, _ := b64.DecodeString(strings.Split(token, ".")[0])
	if string(headRaw) != `{"typ":"JWT","alg":"ES256"}` {
		t.Errorf("header = %s", headRaw)
	}
}

func TestCheckEndpoint(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/QH1abc",
		"https://wns2-par02p.notify.windows.com/w/?token=abc",
		"https://fcm.googleapis.com:443/x",
	} {
		if err := CheckEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://evil.example/fcm.googleapis.com",
		"https://fcm.googleapis.com.evil.example/x",
		"https://evilfcm.googleapis.com.example/x",
		"https://notfcm.googleapis.comx/x",
		"https://user:pw@fcm.googleapis.com/x",
		"https://fcm.googleapis.com:8443/x",
		"https://127.0.0.1/x",
		"https://localhost/x",
		"file:///etc/passwd",
		"",
	} {
		if err := CheckEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSubscriptionValidate(t *testing.T) {
	sub, _, _ := newSubscription(t, "https://fcm.googleapis.com/fcm/send/x")
	if err := sub.Validate(); err != nil {
		t.Fatal(err)
	}
	short := sub
	short.Keys.Auth = b64.EncodeToString([]byte("short"))
	if short.Validate() == nil {
		t.Error("short auth accepted")
	}
	offCurve := sub
	offCurve.Keys.P256dh = b64.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	if offCurve.Validate() == nil {
		t.Error("point off the curve accepted")
	}
	host := sub
	host.Endpoint = "https://example.com/push"
	if host.Validate() == nil {
		t.Error("unknown push host accepted")
	}
}

type fakeDoer struct {
	status int
	reqs   []*http.Request
	bodies [][]byte
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	f.reqs = append(f.reqs, r)
	f.bodies = append(f.bodies, body)
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("nope"))}, nil
}

func TestSend(t *testing.T) {
	k, _, _ := LoadOrCreateKeys(filepath.Join(t.TempDir(), "vapid.pem"))
	sub, uaPriv, auth := newSubscription(t, "https://fcm.googleapis.com/fcm/send/abc")
	doer := &fakeDoer{status: http.StatusCreated}
	s := &Sender{Keys: k, Client: doer}

	msg := []byte(`{"title":"hi"}`)
	if err := s.Send(context.Background(), sub, msg, Options{TTL: time.Hour, Urgency: "high", Topic: "w5"}); err != nil {
		t.Fatal(err)
	}
	r := doer.reqs[0]
	if r.Method != http.MethodPost || r.URL.String() != sub.Endpoint {
		t.Errorf("request %s %s", r.Method, r.URL)
	}
	for h, want := range map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"TTL":              "3600",
		"Urgency":          "high",
		"Topic":            "w5",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if got := decrypt(t, doer.bodies[0], uaPriv, auth); !bytes.Equal(got, msg) {
		t.Errorf("body decrypts to %q", got)
	}

	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		doer.status = status
		if err := s.Send(context.Background(), sub, msg, Options{}); !errors.Is(err, ErrGone) {
			t.Errorf("%d: err = %v, want ErrGone", status, err)
		}
	}
	doer.status = http.StatusBadRequest
	if err := s.Send(context.Background(), sub, msg, Options{}); err == nil || errors.Is(err, ErrGone) || !strings.Contains(err.Error(), "400") {
		t.Errorf("400: err = %v", err)
	}

	n := len(doer.reqs)
	bad := sub
	bad.Endpoint = "https://internal.example/hook"
	if s.Send(context.Background(), bad, msg, Options{}) == nil {
		t.Error("unknown endpoint sent")
	}
	if s.Send(context.Background(), sub, msg, Options{Topic: "@5"}) == nil {
		t.Error("invalid topic sent")
	}
	if s.Send(context.Background(), sub, msg, Options{Urgency: "urgent"}) == nil {
		t.Error("invalid urgency sent")
	}
	if len(doer.reqs) != n {
		t.Error("a refused message still made a request")
	}
}
