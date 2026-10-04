package web

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// startTLSServer runs ServeTLSOrRedirect on a random loopback port and
// returns its address plus a client that trusts the test CA.
func startTLSServer(t *testing.T) (addr string, client *http.Client) {
	t.Helper()
	res, err := EnsureTLS(t.TempDir(), []string{"localhost", "127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "hello "+r.Proto)
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{res.Certificate}},
	}
	go ServeTLSOrRedirect(ln, srv, func(h string) bool { return res.Leaf.VerifyHostname(h) == nil })
	t.Cleanup(func() { srv.Close() })

	roots := x509.NewCertPool()
	roots.AddCert(res.CA)
	client = &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 5 * time.Second,
	}
	return ln.Addr().String(), client
}

func TestServeTLSOrRedirectServesHTTPS(t *testing.T) {
	addr, client := startTLSServer(t)
	resp, err := client.Get("https://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// HTTP/2 also lifts browsers' six-connections-per-host cap, which the
	// chat view's EventSource streams would otherwise eat into.
	if string(body) != "hello HTTP/2.0" {
		t.Errorf("body %q, want hello HTTP/2.0", body)
	}
}

func TestServeTLSOrRedirectRedirectsPlainHTTP(t *testing.T) {
	addr, client := startTLSServer(t)
	resp, err := client.Get("http://" + addr + "/chat/3?x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want 307", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://"+addr+"/chat/3?x=1" {
		t.Errorf("Location %q", loc)
	}
}

func TestServeTLSOrRedirectRefusesForeignHost(t *testing.T) {
	addr, client := startTLSServer(t)
	req, _ := http.NewRequest("GET", "http://"+addr+"/", nil)
	req.Host = "evil.example"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400 (no open redirect)", resp.StatusCode)
	}
}
