package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	creds := testCredentials(t, "mo", "s3cret")
	h := BasicAuth(inner, creds)

	tests := []struct {
		name       string
		user, pass string
		setAuth    bool
		wantStatus int
	}{
		{name: "no credentials", wantStatus: http.StatusUnauthorized},
		{name: "wrong password", user: "mo", pass: "nope", setAuth: true, wantStatus: http.StatusUnauthorized},
		{name: "wrong username", user: "other", pass: "s3cret", setAuth: true, wantStatus: http.StatusUnauthorized},
		{name: "empty credentials", setAuth: true, wantStatus: http.StatusUnauthorized},
		{name: "correct credentials", user: "mo", pass: "s3cret", setAuth: true, wantStatus: http.StatusTeapot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
			if tt.setAuth {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: want %d, got %d", tt.wantStatus, rec.Code)
			}
			// A cached success must not leak to other credentials, so run
			// every case twice.
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("second request status: want %d, got %d", tt.wantStatus, rec.Code)
			}
			if tt.wantStatus == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 response missing WWW-Authenticate header")
			}
		})
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:7890":     true,
		"localhost:7890":     true,
		"[::1]:7890":         true,
		"0.0.0.0:7890":       false,
		":7890":              false,
		"192.168.86.47:7890": false,
		"[::]:7890":          false,
		"not-an-addr":        false,
	}
	for addr, want := range tests {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q): want %v, got %v", addr, want, got)
		}
	}
}
