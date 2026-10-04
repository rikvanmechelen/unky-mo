package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/web"
)

func TestWebStartupAddr(t *testing.T) {
	cases := []struct {
		addr       string
		creds      bool
		want       string
		lanBlocked bool
	}{
		{":7890", true, ":7890", false},
		{":7890", false, "127.0.0.1:7890", true},
		{"0.0.0.0:9000", false, "127.0.0.1:9000", true},
		{"127.0.0.1:7890", false, "127.0.0.1:7890", false},
		{"localhost:7890", false, "localhost:7890", false},
	}
	for _, c := range cases {
		got, blocked := webStartupAddr(c.addr, c.creds)
		if got != c.want || blocked != c.lanBlocked {
			t.Errorf("webStartupAddr(%q, %v) = (%q, %v), want (%q, %v)", c.addr, c.creds, got, blocked, c.want, c.lanBlocked)
		}
	}
}

func TestWebURLs(t *testing.T) {
	cases := []struct {
		addr, lan string
		want      []string
	}{
		{":7890", "192.168.1.5", []string{"https://localhost:7890", "https://192.168.1.5:7890"}},
		{"0.0.0.0:7890", "", []string{"https://localhost:7890"}},
		{"127.0.0.1:7890", "192.168.1.5", []string{"https://localhost:7890"}},
		{"10.0.0.2:80", "192.168.1.5", []string{"https://10.0.0.2:80"}},
	}
	for _, c := range cases {
		if got := webURLs("https", c.addr, c.lan); !reflect.DeepEqual(got, c.want) {
			t.Errorf("webURLs(https, %q, %q) = %v, want %v", c.addr, c.lan, got, c.want)
		}
	}
	if got := webURLs("http", ":7890", ""); !reflect.DeepEqual(got, []string{"http://localhost:7890"}) {
		t.Errorf("webURLs(http, :7890) = %v", got)
	}
}

func TestTrustWarning(t *testing.T) {
	ca := "/home/me/.config/unky-mo/tls/ca.pem"
	arch := func(s string) bool { return s == "trust" }
	cases := []struct {
		name  string
		trust web.Trust
		goos  string
		want  []string // substrings
		empty bool
	}{
		{"trusted", web.Trust{System: true}, "linux", nil, true},
		{"trusted incl. chrome", web.Trust{System: true, NSSDB: "/db", NSS: true}, "linux", nil, true},
		{"system missing on linux", web.Trust{}, "linux", []string{"sudo trust anchor " + ca, "ctrl+alt+r", "mo web tls trust"}, false},
		{"system missing on macOS", web.Trust{}, "darwin", []string{"sudo security add-trusted-cert", ca}, false},
		{"only chrome missing", web.Trust{System: true, NSSDB: "/db"}, "linux", []string{"Chrome:  certutil -d sql:/db -A -t C,, -n unky-mo -i " + ca, "mo web tls trust"}, false},
	}
	for _, c := range cases {
		got := trustWarning(c.trust, c.goos, ca, arch)
		if c.empty != (got == "") {
			t.Errorf("%s: got %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
		if c.name == "only chrome missing" && strings.Contains(got, "sudo") {
			t.Errorf("%s: system step shown though the system trusts it: %q", c.name, got)
		}
	}
}
