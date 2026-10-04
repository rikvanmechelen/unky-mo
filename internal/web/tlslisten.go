package web

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"time"
)

// ServeTLSOrRedirect serves srv over TLS on ln (srv.TLSConfig must carry
// the certificate) and answers plain HTTP on the same port with a redirect
// to https, so old http:// bookmarks keep working. Each connection's first
// byte decides: a TLS handshake starts with 0x16, an HTTP request with a
// letter. The redirect only goes to hosts the cert covers (allowHost), so
// it can't be used as an open redirect.
func ServeTLSOrRedirect(ln net.Listener, srv *http.Server, allowHost func(string) bool) error {
	defer ln.Close()
	tlsL := newChanListener(ln.Addr())
	httpL := newChanListener(ln.Addr())

	redirect := &http.Server{
		Handler:           redirectToHTTPS(allowHost),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go redirect.Serve(httpL)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				tlsL.closeWith(err)
				httpL.closeWith(err)
				return
			}
			go routeConn(conn, tlsL, httpL)
		}
	}()
	return srv.ServeTLS(tlsL, "", "")
}

func routeConn(conn net.Conn, tlsL, httpL *chanListener) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return
	}
	peeked := &peekedConn{Conn: conn, r: br}
	if first[0] == 0x16 { // TLS handshake record
		tlsL.deliver(peeked)
	} else {
		httpL.deliver(peeked)
	}
}

func redirectToHTTPS(allowHost func(string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if !allowHost(host) {
			http.Error(w, "this server only speaks https", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
}

// peekedConn is a conn whose first bytes were already read into r.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener is a net.Listener fed conns over a channel.
type chanListener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

func (l *chanListener) deliver(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
	}
}

func (l *chanListener) closeWith(err error) {
	l.once.Do(func() {
		l.err = err
		close(l.done)
	})
}

func (l *chanListener) Close() error   { l.closeWith(net.ErrClosed); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }
