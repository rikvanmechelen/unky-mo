package main

import (
	"reflect"
	"testing"
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
		{":7890", "192.168.1.5", []string{"http://localhost:7890", "http://192.168.1.5:7890"}},
		{"0.0.0.0:7890", "", []string{"http://localhost:7890"}},
		{"127.0.0.1:7890", "192.168.1.5", []string{"http://localhost:7890"}},
		{"10.0.0.2:80", "192.168.1.5", []string{"http://10.0.0.2:80"}},
	}
	for _, c := range cases {
		if got := webURLs(c.addr, c.lan); !reflect.DeepEqual(got, c.want) {
			t.Errorf("webURLs(%q, %q) = %v, want %v", c.addr, c.lan, got, c.want)
		}
	}
}
