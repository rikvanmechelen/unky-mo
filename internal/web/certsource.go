package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// CertSource serves the generated server cert and re-signs it when this
// machine's addresses change, so a laptop that moves from home to the
// office without restarting mo still has a cert for its new IP. The CA is
// never replaced, so every device that trusts it keeps trusting.
type CertSource struct {
	dir   string
	hosts func() []string
	now   func() time.Time

	mu   sync.Mutex // serializes Refresh
	last []string   // sorted host list the current cert was made for
	cur  atomic.Pointer[tls.Certificate]
	leaf atomic.Pointer[x509.Certificate]
}

// NewCertSource ensures dir holds a CA and a cert for hosts() (EnsureTLS)
// and returns a source serving it.
func NewCertSource(dir string, hosts func() []string, now func() time.Time) (*CertSource, *TLSResult, error) {
	if now == nil {
		now = time.Now
	}
	c := &CertSource{dir: dir, hosts: hosts, now: now}
	h := sortedHosts(hosts())
	res, err := EnsureTLS(dir, h, now())
	if err != nil {
		return nil, nil, err
	}
	c.set(res, h)
	return c, res, nil
}

func (c *CertSource) set(res *TLSResult, hosts []string) {
	cert := res.Certificate
	c.cur.Store(&cert)
	c.leaf.Store(res.Leaf)
	c.last = hosts
}

// GetCertificate is tls.Config.GetCertificate.
func (c *CertSource) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.cur.Load(), nil
}

// Leaf is the current server cert.
func (c *CertSource) Leaf() *x509.Certificate { return c.leaf.Load() }

// Refresh re-signs the cert when the host list changed or the cert is near
// expiry. It reports whether the served cert changed.
func (c *CertSource) Refresh() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := sortedHosts(c.hosts())
	now := c.now()
	if slices.Equal(h, c.last) && now.Before(c.Leaf().NotAfter.Add(-leafRenewBefore)) {
		return false, nil
	}
	res, err := EnsureTLS(c.dir, h, now)
	if err != nil {
		return false, err
	}
	changed := !c.Leaf().Equal(res.Leaf)
	c.set(res, h)
	return changed, nil
}

// Run calls Refresh every interval until ctx ends. A failed refresh keeps
// serving the current cert and is retried next time.
func (c *CertSource) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = c.Refresh()
		}
	}
}

func sortedHosts(h []string) []string {
	out := slices.Clone(h)
	slices.Sort(out)
	return slices.Compact(out)
}
