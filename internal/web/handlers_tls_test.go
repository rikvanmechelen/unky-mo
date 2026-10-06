package web

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testCA(t *testing.T) *x509.Certificate {
	t.Helper()
	res, err := EnsureTLS(t.TempDir(), []string{"localhost"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return res.CA
}

func TestCACertServesDER(t *testing.T) {
	ca := testCA(t)
	srv := NewServer(Deps{CA: ca}, 0, "test")

	for _, tc := range []struct{ url, ctype string }{
		{"/ca.crt", "application/x-x509-ca-cert"},
		{"/ca.crt?as=file", "application/octet-stream"},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.url, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.ctype {
			t.Errorf("%s: Content-Type %q, want %q", tc.url, got, tc.ctype)
		}
		if !bytes.Equal(rec.Body.Bytes(), ca.Raw) {
			t.Errorf("%s: body isn't the CA's DER", tc.url)
		}
		if _, err := x509.ParseCertificate(rec.Body.Bytes()); err != nil {
			t.Errorf("%s: %v", tc.url, err)
		}
	}
}

func TestCACertWithoutCA(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ca.crt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/ca.crt: status %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tls", nil))
	var info map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["ca"] != false || len(info) != 1 {
		t.Errorf("/api/tls = %v, want {ca: false}", info)
	}
}

func TestTLSInfo(t *testing.T) {
	ca := testCA(t)
	srv := NewServer(Deps{CA: ca}, 0, "test")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tls", nil))
	var info struct {
		CA          bool   `json:"ca"`
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if !info.CA || info.Name != ca.Subject.CommonName || info.Fingerprint != Fingerprint(ca) {
		t.Errorf("/api/tls = %+v", info)
	}
}
