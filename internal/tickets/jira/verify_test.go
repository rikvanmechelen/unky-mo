package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyCredsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("missing basic auth: %q", auth)
		}
		w.Write([]byte(`{"displayName":"Rik V.","emailAddress":"rik@moma.org"}`))
	}))
	defer srv.Close()

	who, err := VerifyCreds(context.Background(), srv.URL, "rik@moma.org", "token")
	if err != nil {
		t.Fatalf("VerifyCreds: %v", err)
	}
	if who != "Rik V." {
		t.Errorf("who: got %q", who)
	}
}

func TestVerifyCredsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := VerifyCreds(context.Background(), srv.URL, "e", "t")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("want rejected error, got %v", err)
	}
}

func TestVerifyCredsWrongURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := VerifyCreds(context.Background(), srv.URL, "e", "t")
	if err == nil || !strings.Contains(err.Error(), "right URL") {
		t.Errorf("want wrong-URL error, got %v", err)
	}
}

func TestVerifyCredsFallsBackToEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"displayName":"","emailAddress":"rik@example.com"}`))
	}))
	defer srv.Close()
	who, err := VerifyCreds(context.Background(), srv.URL, "e", "t")
	if err != nil {
		t.Fatal(err)
	}
	if who != "rik@example.com" {
		t.Errorf("want email fallback, got %q", who)
	}
}
