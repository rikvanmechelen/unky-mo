package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rvanmech/unky-mo/internal/bashsnap"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/web"
)

// hookScriptDir is where the embedded status hook script is installed, so
// Claude's settings never point into a particular unky-mo checkout.
func hookScriptDir() string {
	return filepath.Join(config.DefaultConfigDir(), "hooks")
}

// installStatusHooks writes the embedded hook script and makes sure Claude's
// settings carry exactly the V2 hook set pointing at it, plus the Bash
// snapshot hooks unless [web] disable_bash_diffs. changed reports whether
// either the script or settings.json had to be updated.
func installStatusHooks(cfg *config.Config) (script string, changed bool, err error) {
	moBin, err := os.Executable()
	if err != nil {
		return "", false, fmt.Errorf("locating mo binary: %w", err)
	}
	script, scriptChanged, err := claude.EnsureStatusHookScript(hookScriptDir(), moBin)
	if err != nil {
		return "", false, fmt.Errorf("writing hook script: %w", err)
	}
	snapshotBin := moBin
	if cfg.Web.DisableBashDiffs {
		snapshotBin = ""
	}
	settingsChanged, err := claude.EnsureHooksV2(script, snapshotBin)
	if err != nil {
		return "", false, fmt.Errorf("installing hooks: %w", err)
	}
	return script, scriptChanged || settingsChanged, nil
}

// startupChecks brings the installation up to date before the TUI starts
// (hooks, web certificate, background web server) and returns notices for
// the status bar, plus a warning that should stay up until a keypress
// (the web CA isn't trusted yet). Runs on every TUI start, including
// ctrl+alt+r, so it must be idempotent and cheap when nothing changed.
// Failures are reported, never fatal.
func startupChecks(cfg *config.Config) (notices []string, warning string) {

	if _, changed, err := installStatusHooks(cfg); err != nil {
		notices = append(notices, "hook setup failed: "+err.Error())
	} else if changed {
		notices = append(notices, "updated Claude status hooks")
	}
	if store, err := bashsnap.NewStore(moexec.DefaultCommander); err == nil {
		store.Sweep()
	}

	if !cfg.Web.Disabled {
		creds, err := web.LoadCredentials(webCredentialsPath())
		if err != nil {
			notices = append(notices, "web failed: "+err.Error())
			return notices, ""
		}
		addr, lanBlocked := webStartupAddr(cfg.Web.ListenAddr(), creds != nil)
		scheme := "http"
		if !cfg.Web.DisableTLS {
			scheme = "https"
			// Made here rather than only in `mo web` so the trust check
			// below sees the cert the server is about to serve.
			if _, res, err := loadWebTLS(cfg.Web, addr); err != nil {
				notices = append(notices, "web TLS failed: "+err.Error())
			} else if res != nil {
				warning = trustWarning(checkWebTrust(res), runtime.GOOS, res.Files.CA, web.CommandExists)
			}
		}
		ctx := ops.NewContext(tmux.NewClient(cfg.TmuxSession))
		if err := ops.EnsureWebServer(ctx, addr); err != nil {
			notices = append(notices, "web failed: "+err.Error())
		} else {
			note := "web: " + strings.Join(webURLs(scheme, addr, lanIPv4()), " · ")
			if lanBlocked {
				note += " (localhost only — run 'mo web auth set' for LAN access)"
			}
			notices = append(notices, note)
		}
	}
	return notices, warning
}

// webStartupAddr narrows a non-loopback addr to 127.0.0.1 on the same port
// when no login is configured — `mo web` refuses to expose the prompt box
// (which drives live agents) unauthenticated.
func webStartupAddr(addr string, haveCreds bool) (string, bool) {
	if haveCreds || web.IsLoopbackAddr(addr) {
		return addr, false
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, false // let `mo web` report the bad address
	}
	return net.JoinHostPort("127.0.0.1", port), true
}

// webURLs lists the URLs a listen address is reachable at: localhost for
// loopback or wildcard binds, plus lanIP for wildcard binds; an explicit
// host is shown as-is.
func webURLs(scheme, addr, lanIP string) []string {
	prefix := scheme + "://"
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{prefix + addr}
	}
	switch {
	case host == "" || host == "0.0.0.0" || host == "::":
		urls := []string{prefix + "localhost:" + port}
		if lanIP != "" {
			urls = append(urls, prefix+net.JoinHostPort(lanIP, port))
		}
		return urls
	case web.IsLoopbackAddr(addr):
		return []string{prefix + "localhost:" + port}
	default:
		return []string{prefix + net.JoinHostPort(host, port)}
	}
}

// lanIPv4 returns the first private IPv4 address of an up, non-loopback
// interface, or "" if there is none. Display-only.
func lanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
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
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					return ip4.String()
				}
			}
		}
	}
	return ""
}
