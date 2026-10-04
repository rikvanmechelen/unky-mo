package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mo web serves HTTPS with a certificate from a local CA it creates itself
// (the mkcert approach). The CA is made once and kept, so a device that
// trusts it keeps trusting mo web. The server cert it signs is cheap to
// replace and is re-signed whenever it nears expiry or this machine gains a
// name/IP it doesn't cover (DHCP, VPNs). Only the standard library is used,
// so this behaves the same on macOS and Linux.

const (
	caValidity = 10 * 365 * 24 * time.Hour
	// Apple rejects locally-trusted server certs valid for more than 825
	// days; 397 matches the public-CA limit browsers enforce.
	leafValidity    = 397 * 24 * time.Hour
	leafRenewBefore = 30 * 24 * time.Hour
	tlsDirMode      = 0700
	tlsKeyMode      = 0600
	tlsCertMode     = 0644
)

// TLSFiles are the PEM files in mo web's TLS directory.
type TLSFiles struct {
	CA, CAKey, Cert, Key string
}

// TLSFilesIn names the files EnsureTLS keeps in dir.
func TLSFilesIn(dir string) TLSFiles {
	return TLSFiles{
		CA:    filepath.Join(dir, "ca.pem"),
		CAKey: filepath.Join(dir, "ca-key.pem"),
		Cert:  filepath.Join(dir, "cert.pem"),
		Key:   filepath.Join(dir, "key.pem"),
	}
}

// TLSResult is what EnsureTLS found or made.
type TLSResult struct {
	Files       TLSFiles
	CA          *x509.Certificate
	Leaf        *x509.Certificate
	Certificate tls.Certificate // the server cert + key, ready for tls.Config
	NewCA       bool            // the CA was (re)created: devices must trust it again
	NewLeaf     bool            // the server cert was (re)issued
}

// EnsureTLS makes sure dir holds a local CA and a server cert signed by it
// that covers every name in hosts (DNS names or IPs). Existing files are
// reused untouched when still valid, so calling it on every start is cheap
// and doesn't invalidate the trust a device already gave the CA.
func EnsureTLS(dir string, hosts []string, now time.Time) (*TLSResult, error) {
	if err := os.MkdirAll(dir, tlsDirMode); err != nil {
		return nil, err
	}
	files := TLSFilesIn(dir)
	res := &TLSResult{Files: files}

	ca, caKey, err := loadCA(files, now)
	if err != nil {
		ca, caKey, err = createCA(files, now)
		if err != nil {
			return nil, fmt.Errorf("create CA: %w", err)
		}
		res.NewCA = true
	}
	res.CA = ca

	if cert, leaf, ok := loadLeaf(files, ca, hosts, now); ok {
		res.Certificate, res.Leaf = cert, leaf
		return res, nil
	}
	cert, leaf, err := issueLeaf(files, ca, caKey, hosts, now)
	if err != nil {
		return nil, fmt.Errorf("issue server cert: %w", err)
	}
	res.Certificate, res.Leaf, res.NewLeaf = cert, leaf, true
	return res, nil
}

func loadCA(files TLSFiles, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	pair, err := tls.LoadX509KeyPair(files.CA, files.CAKey)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || !ca.IsCA || now.After(ca.NotAfter) {
		return nil, nil, errors.New("unusable CA")
	}
	return ca, key, nil
}

func createCA(files TLSFiles, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	name := "unky-mo local CA"
	if host, err := os.Hostname(); err == nil && host != "" {
		name += " (" + host + ")" // tells CAs from different machines apart on a phone
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"unky-mo"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	// Key first: a crash between the writes leaves a mismatched pair, which
	// loadCA rejects and the next start replaces.
	if err := writeKey(files.CAKey, key); err != nil {
		return nil, nil, err
	}
	if err := writePEM(files.CA, "CERTIFICATE", der, tlsCertMode); err != nil {
		return nil, nil, err
	}
	return ca, key, nil
}

// loadLeaf returns the existing server cert if it's signed by ca, not close
// to expiry, and covers every host.
func loadLeaf(files TLSFiles, ca *x509.Certificate, hosts []string, now time.Time) (tls.Certificate, *x509.Certificate, bool) {
	pair, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		return tls.Certificate{}, nil, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.CheckSignatureFrom(ca) != nil || now.Add(leafRenewBefore).After(leaf.NotAfter) {
		return tls.Certificate{}, nil, false
	}
	for _, h := range hosts {
		if leaf.VerifyHostname(h) != nil {
			return tls.Certificate{}, nil, false
		}
	}
	pair.Leaf = leaf
	return pair, leaf, true
}

func issueLeaf(files TLSFiles, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hosts []string, now time.Time) (tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: "unky-mo web", Organization: []string{"unky-mo"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if err := writeKey(files.Key, key); err != nil {
		return tls.Certificate{}, nil, err
	}
	if err := writePEM(files.Cert, "CERTIFICATE", der, tlsCertMode); err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf, nil
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return n
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, tlsKeyMode)
}

// writePEM replaces path atomically, so a concurrent reader (the TUI's
// startup check and `mo web` both call EnsureTLS) never sees half a file.
func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: typ, Bytes: der}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Fingerprint is a cert's SHA-256 fingerprint in the colon-separated hex
// form browsers and phones show, for checking a copied CA file.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// TLSHosts lists the names and IPs the server cert must cover for a listen
// address: localhost, this machine's hostname (plain and .local), every
// IPv4 address of an up interface, and an explicit listen host.
func TLSHosts(listenAddr string) []string {
	hostname, _ := os.Hostname()
	return tlsHosts(hostname, interfaceIPv4s(), listenAddr)
}

func tlsHosts(hostname string, ips []net.IP, listenAddr string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")

	// macOS hostnames often already end in .local; Linux ones usually don't.
	name := strings.TrimSuffix(strings.ToLower(hostname), ".")
	short := strings.TrimSuffix(name, ".local")
	add(name)
	if short != "" && short != "localhost" && !strings.Contains(short, ".") {
		add(short)
		add(short + ".local")
	}

	for _, ip := range ips {
		add(ip.String())
	}
	if host, _, err := net.SplitHostPort(listenAddr); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			add(strings.ToLower(host))
		}
	}
	return out
}

// interfaceIPv4s returns the IPv4 addresses of up, non-loopback interfaces,
// skipping link-local ones. IPv6 is left out on purpose: temporary
// (privacy) addresses rotate daily, which would re-sign the cert each day.
func interfaceIPv4s() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsGlobalUnicast() {
					ips = append(ips, ip4)
				}
			}
		}
	}
	return ips
}

// sameCert reports whether pemBytes holds cert.
func sameCert(pemBytes []byte, cert *x509.Certificate) bool {
	for {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			return false
		}
		if block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, cert.Raw) {
			return true
		}
	}
}
