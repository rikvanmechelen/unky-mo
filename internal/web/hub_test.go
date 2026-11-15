package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMachineIDCreatesThenReuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id, err := MachineID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isHexID(id) {
		t.Fatalf("id %q isn't 32 hex characters", id)
	}
	info, err := os.Stat(filepath.Join(dir, "machine-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
	again, err := MachineID(dir)
	if err != nil || again != id {
		t.Errorf("second read = %q, %v; want %q", again, err, id)
	}
}

func TestMachineIDRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(path, []byte("not an id\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MachineID(dir); err == nil {
		t.Fatal("want an error for a corrupt machine-id")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "not an id\n" {
		t.Errorf("corrupt file was replaced: %q", b)
	}
}

func TestHubAddrs(t *testing.T) {
	hosts := []string{"localhost", "127.0.0.1", "::1", "buttercup", "buttercup.local", "192.168.86.47", "fd00::5", "laptop.tail.ts.net"}
	cases := []struct {
		name, scheme, addr string
		want               []string
	}{
		{"wildcard", "https", ":7890", []string{
			"https://buttercup:7890", "https://buttercup.local:7890", "https://192.168.86.47:7890",
			"https://[fd00::5]:7890", "https://laptop.tail.ts.net:7890"}},
		{"unspecified v4", "http", "0.0.0.0:8000", []string{
			"http://buttercup:8000", "http://buttercup.local:8000", "http://192.168.86.47:8000",
			"http://[fd00::5]:8000", "http://laptop.tail.ts.net:8000"}},
		{"explicit host", "https", "192.168.86.47:7890", []string{"https://192.168.86.47:7890"}},
		{"loopback only", "https", "127.0.0.1:7890", nil},
		{"bad addr", "https", "nonsense", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HubAddrs(c.scheme, c.addr, hosts); !reflect.DeepEqual(got, c.want) {
				t.Errorf("HubAddrs = %v\nwant %v", got, c.want)
			}
		})
	}
}

func testHub() *HubInfo {
	return &HubInfo{
		Machine: "0123456789abcdef0123456789abcdef",
		Name:    "buttercup",
		Version: "v1.2.3",
		Addrs:   func() []string { return []string{"https://buttercup.local:7890"} },
	}
}

func TestHubHello(t *testing.T) {
	srv := NewServer(Deps{Hub: testHub()}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hub/hello", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin %q, want *", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", got)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"app": "unky-mo", "machine": "0123456789abcdef0123456789abcdef", "name": "buttercup", "version": "v1.2.3", "protocol": float64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hello = %v\nwant %v", got, want)
	}
}

func TestHubAddrsEndpoint(t *testing.T) {
	srv := NewServer(Deps{Hub: testHub()}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hub/addrs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("addrs must not be readable cross-origin")
	}
	var got struct{ Addrs []string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Addrs, []string{"https://buttercup.local:7890"}) {
		t.Errorf("addrs = %v", got.Addrs)
	}
}

func TestHubRoutesWithoutHub(t *testing.T) {
	srv := NewServer(Deps{}, 0, "test")
	for _, url := range []string{"/api/hub/hello", "/api/hub/addrs"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", url, rec.Code)
		}
	}
}
