package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// IPv4 fallback (tony on Discord: models.dev and the update check failed
// with a bare EOF, direct or not, while curl got through). Where a TUN
// (xray, Clash's) takes every connection but can't carry IPv6 out, a
// connection to a site's IPv6 address is accepted, so the dialer's own
// fallback to IPv4 never starts, and closed once TLS begins. When a
// connection to an IPv6 address ends before a byte comes back, its
// host:port is dialed over IPv4 for a while, and the request that met it
// is sent again once, over IPv4, when nothing of it can have reached the
// site: an HTTPS request (no byte came back, so TLS never finished) or an
// idempotent one, and its body can be had again. The request goes as it
// was; connections over IPv4, and through a proxy, are left as they are.

// v6Broken is how long a host:port whose IPv6 failed is dialed over IPv4.
const v6Broken = 30 * time.Minute

var v4Only struct {
	sync.Mutex
	at map[string]time.Time // host:port → when its IPv6 failed
}

func v4Since(addr string) time.Time {
	v4Only.Lock()
	defer v4Only.Unlock()
	at := v4Only.at[addr]
	if !at.IsZero() && time.Since(at) > v6Broken {
		delete(v4Only.at, addr)
		return time.Time{}
	}
	return at
}

func markV4(addr string) {
	v4Only.Lock()
	defer v4Only.Unlock()
	if v4Only.at == nil {
		v4Only.at = map[string]time.Time{}
	}
	v4Only.at[addr] = time.Now()
}

// v4Fallback has t dial a host:port whose IPv6 failed over IPv4, and
// watch connections to an IPv6 address for failing so.
func v4Fallback(t *http.Transport) {
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network == "tcp" && !v4Since(addr).IsZero() {
			network = "tcp4"
		}
		c, err := dial(ctx, network, addr)
		if err != nil || c == nil {
			return c, err
		}
		if a, ok := c.RemoteAddr().(*net.TCPAddr); ok && a.IP.To4() == nil {
			return &v6Conn{Conn: c, addr: addr}, nil
		}
		return c, nil
	}
}

// v6Conn is a connection to an IPv6 address, marking its host:port for
// IPv4 if it fails before a byte comes back.
type v6Conn struct {
	net.Conn
	addr string
	mu   sync.Mutex
	read bool // a byte came back
	shut bool // closed on magpie's side
}

func (c *v6Conn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.seen(n, err)
	return n, err
}

func (c *v6Conn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.seen(0, err)
	return n, err
}

func (c *v6Conn) Close() error {
	c.mu.Lock()
	c.shut = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *v6Conn) seen(n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.read {
		return
	}
	if n > 0 {
		c.read = true
		return
	}
	// a timeout, or a close of magpie's own, says nothing of the path
	if err == nil || c.shut || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	c.read = true // once
	markV4(c.addr)
}

// v4Retry is res, err of req, or of req sent again when its first try met
// an IPv6 address that failed (see v4Fallback) and it can be sent again.
func v4Retry(t *http.Transport, req *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := t.RoundTrip(req)
	if err == nil || req.URL == nil {
		return res, err
	}
	if t.Proxy != nil {
		if p, perr := t.Proxy(req); perr != nil || p != nil {
			return res, err
		}
	}
	if req.Context().Err() != nil || v4Since(hostPort(req)).Before(start) {
		return res, err
	}
	if req.URL.Scheme != "https" && !idempotent(req.Method) {
		return res, err
	}
	again := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return res, err
		}
		body, berr := req.GetBody()
		if berr != nil {
			return res, err
		}
		again.Body = body
	}
	return t.RoundTrip(again)
}

func idempotent(m string) bool {
	switch m {
	case "", "GET", "HEAD", "OPTIONS", "TRACE", "PUT", "DELETE":
		return true
	}
	return false
}

// hostPort is the host:port req's connection is dialed to, directly.
func hostPort(req *http.Request) string {
	if p := req.URL.Port(); p != "" {
		return net.JoinHostPort(req.URL.Hostname(), p)
	}
	port := "80"
	if req.URL.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(req.URL.Hostname(), port)
}
