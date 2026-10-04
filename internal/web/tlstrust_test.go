package web

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

func TestVerifiesAgainstRoots(t *testing.T) {
	res, err := EnsureTLS(t.TempDir(), testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(res.CA)
	if !verifies(res.Leaf, roots) {
		t.Error("leaf should verify against a pool holding its CA")
	}
	if verifies(res.Leaf, x509.NewCertPool()) {
		t.Error("leaf verified against an empty pool")
	}
	// A CA created a moment ago can't be in the real system store.
	if verifies(res.Leaf, nil) {
		t.Error("a brand-new CA is trusted by the system store")
	}
}

func TestTrustTrusted(t *testing.T) {
	cases := []struct {
		t    Trust
		want bool
	}{
		{Trust{System: true}, true},
		{Trust{System: false}, false},
		{Trust{System: true, NSSDB: "/db", NSS: false}, false},
		{Trust{System: true, NSSDB: "/db", NSS: true}, true},
	}
	for _, c := range cases {
		if got := c.t.Trusted(); got != c.want {
			t.Errorf("%+v.Trusted() = %v, want %v", c.t, got, c.want)
		}
	}
}

const certutilList = `
Certificate Nickname                                         Trust Attributes
                                                             SSL,S/MIME,JAR/XPI

some client cert                                             u,u,u
unky-mo                                                      C,,
Corp Root CA - Example                                       CT,C,C
email only                                                   ,C,
`

func TestNSSTLSTrustedNicknames(t *testing.T) {
	got := nssTLSTrustedNicknames(certutilList)
	want := []string{"unky-mo", "Corp Root CA - Example"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNSSTrustsCA(t *testing.T) {
	res, err := EnsureTLS(t.TempDir(), testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, err := EnsureTLS(t.TempDir(), testHosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pemOf := func(c *x509.Certificate) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	list := []string{"certutil", "-L", "-d", "sql:/db"}
	nick := func(n string) []any {
		return []any{"certutil", "-L", "-d", "sql:/db", "-n", n, "-a"}
	}

	t.Run("found under another nickname", func(t *testing.T) {
		cmd := mock_exec.NewMockCommander(gomock.NewController(t))
		cmd.EXPECT().Output(gomock.Any(), "", list[0], toAny(list[1:])...).Return([]byte(certutilList), nil, nil)
		cmd.EXPECT().Output(gomock.Any(), "", nick("unky-mo")[0], nick("unky-mo")[1:]...).Return(pemOf(other.CA), nil, nil)
		cmd.EXPECT().Output(gomock.Any(), "", nick("Corp Root CA - Example")[0], nick("Corp Root CA - Example")[1:]...).Return(pemOf(res.CA), nil, nil)
		if !nssTrustsCA(cmd, "/db", res.CA) {
			t.Error("CA is in the db, trusted for TLS")
		}
	})
	t.Run("only a stale copy", func(t *testing.T) {
		cmd := mock_exec.NewMockCommander(gomock.NewController(t))
		cmd.EXPECT().Output(gomock.Any(), "", list[0], toAny(list[1:])...).Return([]byte(certutilList), nil, nil)
		cmd.EXPECT().Output(gomock.Any(), "", gomock.Any(), gomock.Any()).Return(pemOf(other.CA), nil, nil).Times(2)
		if nssTrustsCA(cmd, "/db", res.CA) {
			t.Error("a different CA under the same nickname doesn't mean ours is trusted")
		}
	})
	t.Run("certutil fails", func(t *testing.T) {
		cmd := mock_exec.NewMockCommander(gomock.NewController(t))
		cmd.EXPECT().Output(gomock.Any(), "", list[0], toAny(list[1:])...).Return(nil, nil, errors.New("bad db"))
		if nssTrustsCA(cmd, "/db", res.CA) {
			t.Error("trusted despite certutil failing")
		}
	})
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func TestSystemTrustCommand(t *testing.T) {
	has := func(present ...string) func(string) bool {
		return func(s string) bool {
			for _, p := range present {
				if p == s {
					return true
				}
			}
			return false
		}
	}
	ca := "/home/me/.config/unky-mo/tls/ca.pem"
	cases := []struct {
		name, goos string
		exists     func(string) bool
		want       string
	}{
		{"macOS", "darwin", has(), "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + ca},
		{"debian with p11-kit too", "linux", has("update-ca-certificates", "/usr/local/share/ca-certificates", "trust"),
			"sudo cp " + ca + " /usr/local/share/ca-certificates/unky-mo.crt && sudo update-ca-certificates"},
		{"arch", "linux", has("trust", "update-ca-trust"), "sudo trust anchor " + ca},
		{"fedora without trust", "linux", has("update-ca-trust"),
			"sudo cp " + ca + " /etc/pki/ca-trust/source/anchors/unky-mo.pem && sudo update-ca-trust"},
		{"unknown linux", "linux", has(), "add " + ca + " to your system's trusted root certificates"},
	}
	for _, c := range cases {
		if got := SystemTrustCommand(c.goos, ca, c.exists); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := SystemTrustCommand("darwin", "/Users/Rik V/ca.pem", nil); !strings.HasSuffix(got, " '/Users/Rik V/ca.pem'") {
		t.Errorf("path with a space isn't quoted: %q", got)
	}
}
