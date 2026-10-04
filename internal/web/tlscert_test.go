package web

import (
	"bytes"
	"crypto/x509"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

var testHosts = []string{"localhost", "127.0.0.1", "::1", "buttercup", "buttercup.local", "192.168.86.47"}

func readAll(t *testing.T, files TLSFiles) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, p := range []string{files.CA, files.CAKey, files.Cert, files.Key} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[p] = b
	}
	return out
}

func TestEnsureTLSCreatesThenReuses(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := EnsureTLS(dir, testHosts, now)
	if err != nil {
		t.Fatal(err)
	}
	if !first.NewCA || !first.NewLeaf {
		t.Fatalf("first call: NewCA=%v NewLeaf=%v, want both true", first.NewCA, first.NewLeaf)
	}
	for path, mode := range map[string]os.FileMode{first.Files.CAKey: 0600, first.Files.Key: 0600, first.Files.CA: 0644, first.Files.Cert: 0644} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != mode {
			t.Errorf("%s mode %v, want %v", path, st.Mode().Perm(), mode)
		}
	}
	before := readAll(t, first.Files)

	second, err := EnsureTLS(dir, testHosts, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.NewCA || second.NewLeaf {
		t.Fatalf("second call: NewCA=%v NewLeaf=%v, want neither", second.NewCA, second.NewLeaf)
	}
	if !reflect.DeepEqual(before, readAll(t, second.Files)) {
		t.Error("second call rewrote the files")
	}
	if second.Certificate.Leaf == nil || len(second.Certificate.Certificate) != 1 {
		t.Error("reused certificate isn't ready for tls.Config")
	}
}

func TestEnsureTLSLeafChainsToCAForEveryHost(t *testing.T) {
	res, err := EnsureTLS(t.TempDir(), testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(res.CA)
	for _, h := range testHosts {
		if _, err := res.Leaf.Verify(x509.VerifyOptions{DNSName: h, Roots: roots}); err != nil {
			t.Errorf("verify for %q: %v", h, err)
		}
	}
	// Apple's rules for locally-trusted server certs.
	if d := res.Leaf.NotAfter.Sub(res.Leaf.NotBefore); d > 825*24*time.Hour {
		t.Errorf("leaf valid for %v, Apple allows at most 825 days", d)
	}
	if !reflect.DeepEqual(res.Leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("leaf EKU %v, want serverAuth", res.Leaf.ExtKeyUsage)
	}
	if res.Leaf.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		t.Errorf("leaf signed with %v", res.Leaf.SignatureAlgorithm)
	}
	if !res.CA.IsCA || res.CA.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA lacks CA:TRUE / certSign")
	}
}

func TestEnsureTLSReissuesLeafButKeepsCA(t *testing.T) {
	cases := map[string]struct {
		hosts []string
		at    time.Duration
	}{
		"new host (IP changed)": {append(append([]string{}, testHosts...), "10.0.0.7"), 0},
		"near expiry":           {testHosts, 380 * 24 * time.Hour},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			first, err := EnsureTLS(dir, testHosts, now)
			if err != nil {
				t.Fatal(err)
			}
			caBefore, _ := os.ReadFile(first.Files.CA)

			res, err := EnsureTLS(dir, c.hosts, now.Add(c.at))
			if err != nil {
				t.Fatal(err)
			}
			if res.NewCA || !res.NewLeaf {
				t.Fatalf("NewCA=%v NewLeaf=%v, want only a new leaf", res.NewCA, res.NewLeaf)
			}
			if caAfter, _ := os.ReadFile(res.Files.CA); !bytes.Equal(caBefore, caAfter) {
				t.Error("CA was rewritten")
			}
			for _, h := range c.hosts {
				if res.Leaf.VerifyHostname(h) != nil {
					t.Errorf("new leaf doesn't cover %q", h)
				}
			}
		})
	}
}

func TestEnsureTLSReplacesBrokenOrExpiredCA(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		if _, err := EnsureTLS(dir, testHosts, now); err != nil {
			t.Fatal(err)
		}
		res, err := EnsureTLS(dir, testHosts, now.Add(11*365*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !res.NewCA || !res.NewLeaf {
			t.Fatalf("NewCA=%v NewLeaf=%v, want both", res.NewCA, res.NewLeaf)
		}
	})
	t.Run("key mismatch", func(t *testing.T) {
		dir := t.TempDir()
		res, err := EnsureTLS(dir, testHosts, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		// The server key in place of the CA key: the pair no longer matches.
		key, _ := os.ReadFile(res.Files.Key)
		if err := os.WriteFile(res.Files.CAKey, key, 0600); err != nil {
			t.Fatal(err)
		}
		res, err = EnsureTLS(dir, testHosts, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !res.NewCA || !res.NewLeaf {
			t.Fatalf("NewCA=%v NewLeaf=%v, want both", res.NewCA, res.NewLeaf)
		}
	})
}

func TestEnsureTLSReissuesLeafFromAnotherCA(t *testing.T) {
	dir := t.TempDir()
	res, err := EnsureTLS(dir, testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(res.Files.CA); err != nil {
		t.Fatal(err)
	}
	res, err = EnsureTLS(dir, testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.NewCA || !res.NewLeaf {
		t.Fatalf("NewCA=%v NewLeaf=%v: a leaf signed by the old CA must be re-issued", res.NewCA, res.NewLeaf)
	}
	if res.Leaf.CheckSignatureFrom(res.CA) != nil {
		t.Error("leaf isn't signed by the new CA")
	}
}

func TestTLSHosts(t *testing.T) {
	lan := []net.IP{net.ParseIP("192.168.86.47").To4(), net.ParseIP("10.8.0.2").To4()}
	cases := []struct {
		name, hostname, addr string
		want                 []string
	}{
		{"linux short name", "buttercup", ":7890",
			[]string{"localhost", "127.0.0.1", "::1", "buttercup", "buttercup.local", "192.168.86.47", "10.8.0.2"}},
		{"macOS .local name", "Riks-MacBook-Pro.local", "0.0.0.0:7890",
			[]string{"localhost", "127.0.0.1", "::1", "riks-macbook-pro.local", "riks-macbook-pro", "192.168.86.47", "10.8.0.2"}},
		{"fqdn", "dev.example.com", "127.0.0.1:7890",
			[]string{"localhost", "127.0.0.1", "::1", "dev.example.com", "192.168.86.47", "10.8.0.2"}},
		{"explicit listen host", "", "100.64.1.2:7890",
			[]string{"localhost", "127.0.0.1", "::1", "192.168.86.47", "10.8.0.2", "100.64.1.2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tlsHosts(c.hostname, lan, c.addr); !reflect.DeepEqual(got, c.want) {
				t.Errorf("tlsHosts(%q, %q) =\n  %v\nwant\n  %v", c.hostname, c.addr, got, c.want)
			}
		})
	}
}

func TestFingerprintFormat(t *testing.T) {
	res, err := EnsureTLS(t.TempDir(), testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(res.CA)
	if len(fp) != 32*3-1 || fp[2] != ':' {
		t.Errorf("fingerprint %q isn't colon-separated SHA-256 hex", fp)
	}
}
