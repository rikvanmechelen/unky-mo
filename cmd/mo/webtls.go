package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/web"
	"github.com/spf13/cobra"
)

// webTLSDir holds mo web's local CA and server cert.
func webTLSDir() string {
	return filepath.Join(config.DefaultConfigDir(), "tls")
}

// loadWebTLS returns the cert mo web serves for addr: [web] cert_file +
// key_file when set (res is then nil), else the local-CA cert, created or
// re-signed as needed.
func loadWebTLS(w config.WebConfig, addr string) (tls.Certificate, *web.TLSResult, error) {
	if w.CertFile != "" || w.KeyFile != "" {
		if w.CertFile == "" || w.KeyFile == "" {
			return tls.Certificate{}, nil, errors.New("[web] cert_file and key_file must be set together")
		}
		cert, err := tls.LoadX509KeyPair(w.CertFile, w.KeyFile)
		return cert, nil, err
	}
	res, err := web.EnsureTLS(webTLSDir(), web.TLSHosts(addr), time.Now())
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return res.Certificate, res, nil
}

// checkWebTrust checks whether this machine's browsers trust the local CA.
func checkWebTrust(res *web.TLSResult) web.Trust {
	home, _ := os.UserHomeDir()
	return web.CheckTrust(moexec.DefaultCommander, res, runtime.GOOS, home)
}

// trustWarning is the status-bar notice shown while the local CA isn't
// trusted: what's wrong, the command(s) that fix it on this OS, and where
// the full instructions are. One step per line so nothing gets cut off.
// "" when trusted.
func trustWarning(t web.Trust, goos, caFile string, exists func(string) bool) string {
	var steps []string
	if !t.System {
		steps = append(steps, "  system:  "+web.SystemTrustCommand(goos, caFile, exists))
	}
	if t.NSSDB != "" && !t.NSS {
		steps = append(steps, "  Chrome:  "+web.NSSTrustCommand(t.NSSDB, caFile))
	}
	if len(steps) == 0 {
		return ""
	}
	lines := []string{"⚠ web: browsers on this machine don't trust the mo web CA yet, so https pages will show a warning. To fix, run:"}
	lines = append(lines, steps...)
	lines = append(lines,
		"  then press ctrl+alt+r to re-check, and fully restart Chrome via chrome://restart (closing its windows isn't enough).",
		"  Firefox, phones and other machines: run `mo web tls trust` for step-by-step instructions. (any key dismisses this)")
	return strings.Join(lines, "\n")
}

func webTLSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tls",
		Short: "Manage the local CA and certificate mo web serves HTTPS with",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the certificate, the names it covers, and whether this machine trusts it",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.Web.DisableTLS {
				fmt.Println("TLS is disabled ([web] disable_tls = true): mo web serves plain HTTP.")
				return nil
			}
			res, err := ensureForStartupAddr(cfg)
			if err != nil || res == nil {
				return err
			}
			printTLSResult(res)
			t := checkWebTrust(res)
			fmt.Printf("Trusted by system: %s\n", yesNo(t.System))
			if t.NSSDB != "" {
				fmt.Printf("Trusted by Chrome: %s (%s)\n", yesNo(t.NSS), t.NSSDB)
			}
			if !t.Trusted() {
				fmt.Println("\nRun 'mo web tls trust' for the steps to trust it.")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "trust",
		Short: "Print how to trust the local CA on this machine, other browsers, and phones",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			res, err := ensureForStartupAddr(cfg)
			if err != nil || res == nil {
				return err
			}
			printTrustSteps(res, checkWebTrust(res))
			return nil
		},
	})

	var newCA bool
	regen := &cobra.Command{
		Use:   "regen",
		Short: "Re-issue the server certificate (--ca: also replace the CA, which every device must trust again)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			files := web.TLSFilesIn(webTLSDir())
			remove := []string{files.Cert, files.Key}
			if newCA {
				remove = append(remove, files.CA, files.CAKey)
			}
			for _, p := range remove {
				if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			res, err := ensureForStartupAddr(cfg)
			if err != nil || res == nil {
				return err
			}
			printTLSResult(res)
			fmt.Println("\nRestart mo web to serve it (ctrl+alt+r in the TUI).")
			return nil
		},
	}
	regen.Flags().BoolVar(&newCA, "ca", false, "also create a new CA")
	cmd.AddCommand(regen)
	return cmd
}

// ensureForStartupAddr runs EnsureTLS for the address the TUI starts mo web
// on, so the CLI and the server agree on the names the cert covers. A nil
// result means [web] cert_file is in use and there's no local CA.
func ensureForStartupAddr(cfg *config.Config) (*web.TLSResult, error) {
	creds, err := web.LoadCredentials(webCredentialsPath())
	if err != nil {
		return nil, err
	}
	addr, _ := webStartupAddr(cfg.Web.ListenAddr(), creds != nil)
	_, res, err := loadWebTLS(cfg.Web, addr)
	if err != nil {
		return nil, err
	}
	if res == nil {
		fmt.Printf("mo web uses your own certificate: %s\n", cfg.Web.CertFile)
	}
	return res, nil
}

func printTLSResult(res *web.TLSResult) {
	fmt.Printf("CA:          %s\n", res.Files.CA)
	fmt.Printf("  name:      %s\n", res.CA.Subject.CommonName)
	fmt.Printf("  sha256:    %s\n", web.Fingerprint(res.CA))
	fmt.Printf("  expires:   %s\n", res.CA.NotAfter.Format("2006-01-02"))
	fmt.Printf("Server cert: %s\n", res.Files.Cert)
	fmt.Printf("  covers:    %s\n", strings.Join(certNames(res.Leaf), ", "))
	fmt.Printf("  expires:   %s (re-signed automatically)\n", res.Leaf.NotAfter.Format("2006-01-02"))
}

func certNames(c *x509.Certificate) []string {
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	return names
}

func printTrustSteps(res *web.TLSResult, t web.Trust) {
	ca := res.Files.CA
	fmt.Printf("mo web's local CA: %s\n", ca)
	fmt.Printf("SHA-256 fingerprint: %s\n", web.Fingerprint(res.CA))
	fmt.Println("(check the fingerprint wherever you install it — the file is only as trustworthy as how you copied it)")

	fmt.Println("\nThis machine:")
	if t.System {
		fmt.Println("  ✓ system trust store")
	} else {
		fmt.Println("  " + web.SystemTrustCommand(runtime.GOOS, ca, web.CommandExists))
	}
	if t.NSSDB != "" {
		if t.NSS {
			fmt.Println("  ✓ Chrome (NSS database)")
		} else {
			fmt.Println("  Chrome: " + web.NSSTrustCommand(t.NSSDB, ca) + "  (then open chrome://restart — closing its windows isn't enough)")
		}
	}
	fmt.Println("  Then press ctrl+alt+r in the TUI to re-check.")

	fmt.Println("\nFirefox (any OS): Settings → Privacy & Security → Certificates → View Certificates →")
	fmt.Println("  Authorities → Import, select ca.pem, tick \"Trust this CA to identify websites\".")
	fmt.Println("\niPhone/iPad: AirDrop or mail ca.pem to the device, install it under Settings → General →")
	fmt.Println("  VPN & Device Management, then enable it under Settings → General → About →")
	fmt.Println("  Certificate Trust Settings.")
	fmt.Println("\nAndroid: Settings → Security → Encryption & credentials → Install a certificate → CA certificate.")
	fmt.Println("\nOther computers: copy ca.pem over and use the command for that OS:")
	fmt.Println("  macOS:          " + web.SystemTrustCommand("darwin", "ca.pem", nil))
	fmt.Println("  Debian/Ubuntu:  sudo cp ca.pem /usr/local/share/ca-certificates/unky-mo.crt && sudo update-ca-certificates")
	fmt.Println("  Arch/Fedora:    sudo trust anchor ca.pem")
	fmt.Println("  Chrome (Linux): certutil -d sql:$HOME/.pki/nssdb -A -t C,, -n " + web.NSSNickname + " -i ca.pem")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
