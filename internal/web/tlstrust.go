package web

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// NSSNickname is the name the trust instructions give the CA in an NSS
// database (Chrome/Chromium on Linux).
const NSSNickname = "unky-mo"

// Trust is whether this machine's browsers would accept mo web's cert.
type Trust struct {
	// System: the OS trust store accepts the server cert. On macOS Go asks
	// the Security framework (the keychain Safari and Chrome use); on Linux
	// it reads the system CA bundle (/etc/ssl/certs, as curl does).
	System bool
	// NSSDB is Chrome/Chromium's own certificate database on Linux, which
	// ignores the system store; "" when there is none or certutil is
	// missing, so it can't be checked.
	NSSDB string
	NSS   bool // the CA is in NSSDB, trusted for TLS
}

// Trusted reports whether every store that could be checked trusts the CA.
func (t Trust) Trusted() bool {
	return t.System && (t.NSSDB == "" || t.NSS)
}

// CheckTrust checks the OS trust store and, on Linux, Chrome's NSS
// database. Go loads the Linux system bundle once per process, so a CA
// trusted after startup only shows up after a restart (ctrl+alt+r).
func CheckTrust(cmd moexec.Commander, res *TLSResult, goos, home string) Trust {
	t := Trust{System: verifies(res.Leaf, nil)}
	if goos == "linux" {
		if db := chromeNSSDB(home); db != "" {
			if _, err := exec.LookPath("certutil"); err == nil {
				t.NSSDB = db
				t.NSS = nssTrustsCA(cmd, db, res.CA)
			}
		}
	}
	return t
}

// verifies reports whether leaf chains to roots (nil: the system store).
func verifies(leaf *x509.Certificate, roots *x509.CertPool) bool {
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: roots})
	return err == nil
}

// chromeNSSDB returns the NSS database Chrome uses on Linux: ~/.pki/nssdb
// when it exists, else ~/.local/share/pki/nssdb (Chrome 146+ default).
func chromeNSSDB(home string) string {
	for _, db := range []string{
		filepath.Join(home, ".pki", "nssdb"),
		filepath.Join(home, ".local", "share", "pki", "nssdb"),
	} {
		if st, err := os.Stat(db); err == nil && st.IsDir() {
			return db
		}
	}
	return ""
}

// nssTrustsCA looks for ca among the db's certs trusted for TLS (a "C" in
// the SSL trust flags), whatever nickname it was imported under.
func nssTrustsCA(cmd moexec.Commander, db string, ca *x509.Certificate) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := cmd.Output(ctx, "", "certutil", "-L", "-d", "sql:"+db)
	if err != nil {
		return false
	}
	for _, nick := range nssTLSTrustedNicknames(string(out)) {
		pemBytes, _, err := cmd.Output(ctx, "", "certutil", "-L", "-d", "sql:"+db, "-n", nick, "-a")
		if err == nil && sameCert(pemBytes, ca) {
			return true
		}
	}
	return false
}

// nssTLSTrustedNicknames parses `certutil -L` output ("<nickname>  C,,")
// into the nicknames whose SSL trust flags include C (trusted CA).
func nssTLSTrustedNicknames(out string) []string {
	var nicks []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t\r")
		i := strings.LastIndexAny(line, " \t")
		if i < 0 {
			continue
		}
		flags, nick := line[i+1:], strings.TrimSpace(line[:i])
		ssl, _, ok := strings.Cut(flags, ",")
		if ok && nick != "" && strings.Contains(ssl, "C") {
			nicks = append(nicks, nick)
		}
	}
	return nicks
}

// SystemTrustCommand is the shell command that adds caFile to this OS's
// trust store. exists reports whether a command (bare name) or path
// (absolute) exists, which tells the Linux families apart.
func SystemTrustCommand(goos, caFile string, exists func(string) bool) string {
	q := shellQuote(caFile)
	switch goos {
	case "darwin":
		return "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + q
	case "linux":
		switch {
		// Debian/Ubuntu: checked first, since they often ship p11-kit's
		// trust too, but its store there isn't what /etc/ssl/certs is built from.
		case exists("update-ca-certificates") && exists("/usr/local/share/ca-certificates"):
			return "sudo cp " + q + " /usr/local/share/ca-certificates/unky-mo.crt && sudo update-ca-certificates"
		case exists("trust"): // Arch, Fedora, openSUSE (p11-kit)
			return "sudo trust anchor " + q
		case exists("update-ca-trust"):
			return "sudo cp " + q + " /etc/pki/ca-trust/source/anchors/unky-mo.pem && sudo update-ca-trust"
		}
	}
	return "add " + q + " to your system's trusted root certificates"
}

// NSSTrustCommand adds caFile to the NSS database db as a trusted TLS CA.
func NSSTrustCommand(db, caFile string) string {
	return fmt.Sprintf("certutil -d sql:%s -A -t C,, -n %s -i %s", shellQuote(db), NSSNickname, shellQuote(caFile))
}

// CommandExists is the exists func for SystemTrustCommand on a real system.
func CommandExists(name string) bool {
	if filepath.IsAbs(name) {
		_, err := os.Stat(name)
		return err == nil
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// shellQuote leaves plain paths readable and single-quotes the rest.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
