package netproxy

import (
	"bytes"
	"context"
	"net"
	"strings"
	"time"
)

// Why is a proxy's failure said so the user can act on it: Why is an
// English sentence the page translates, its {addr}, {was} and {now} filled
// from the fields, and Err the error it explains, shown beside it. A
// failed check of a saved proxy said only Go's words before ("malformed
// HTTP response "SSH-2.0-OpenSSH_9.6"" for an SSH server typed as a
// proxy, @laiuuui on X).
type Why struct {
	Why, Addr, Was, Now string
	Err                 error
}

func (w *Why) Error() string {
	s := strings.NewReplacer("{addr}", w.Addr, "{was}", w.Was, "{now}", w.Now).Replace(w.Why)
	if w.Err == nil {
		return s
	}
	return s + " (" + w.Err.Error() + ")"
}

func (w *Why) Unwrap() error { return w.Err }

type textErr string

func (e textErr) Error() string { return string(e) }

// Explain is why a request through the proxy at address (http://,
// https://, socks5://, as Parse reads it) failed with msg, said as Why;
// nil when msg is nothing it knows. msg is Go's error text, its proxy
// login already taken out.
func Explain(address, msg string) *Why {
	addr := address
	if u, err := Parse(address); err == nil && u.Host != "" {
		addr = u.Host
		if u.Port() == "" {
			addr = net.JoinHostPort(u.Hostname(), map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme])
		}
	}
	w := &Why{Addr: addr, Err: textErr(msg)}
	switch {
	case strings.Contains(msg, `"SSH-`), strings.Contains(msg, "unexpected protocol version 83"):
		// an HTTP proxy's CONNECT answered with an SSH banner, or a SOCKS
		// greeting with its "S"
		w.Why = "{addr} is an SSH server, not a proxy: add it as SSH, with its user and password"
	// readLoopPeekFailLocked: the server spoke before the request went
	// out, as an SSH server's banner does over a plain http:// proxy
	case strings.Contains(msg, "malformed HTTP"), strings.Contains(msg, "unexpected protocol version"), strings.Contains(msg, "readLoopPeekFailLocked"):
		w.Why = "{addr} answered, but not as this kind of proxy: check whether it is HTTP, SOCKS5 or SSH"
	case strings.Contains(msg, "first record does not look like a TLS handshake"):
		w.Why = "{addr} doesn't speak TLS: write http:// instead of https://"
	case strings.Contains(msg, "Proxy Authentication Required"), strings.Contains(msg, "HTTP 407"):
		w.Why = "{addr} wants a username and password, or turned these away"
	case strings.Contains(msg, "username/password authentication failed"):
		w.Why = "{addr} turned the username and password away"
	case strings.Contains(msg, "no acceptable authentication methods"):
		w.Why = "{addr} wants a username and password"
	case refused(msg):
		w.Why = "nothing answers at {addr}: check the address and port, and that the proxy is running"
	case strings.Contains(msg, "no such host"):
		w.Why = "the proxy's host name doesn't resolve"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Client.Timeout"):
		w.Why = "no answer through {addr} in time: check the address, and that a firewall lets magpie reach it"
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "EOF"):
		w.Why = "{addr} hung up: check whether it is HTTP, SOCKS5 or SSH, and its login"
	default:
		return nil
	}
	return w
}

// refused is whether msg is a connection refused: "connection refused" on
// macOS and Linux, and Windows' "No connection could be made because the
// target machine actively refused it" (WSAECONNREFUSED), which magpie on
// Windows read as nothing it knew.
func refused(msg string) bool {
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused")
}

// ExplainServer is Explain, and when the error text can't tell what
// answered (Go reads a real OpenSSH banner, "SSH-2.0-OpenSSH_9.6p1
// Ubuntu-3ubuntu13.20", as an HTTP status line with a malformed code),
// it reads the first bytes the server sends: an SSH server says "SSH-"
// before anything is sent to it. Only a check the user asked for calls
// this, never a request.
func ExplainServer(ctx context.Context, address, msg string) *Why {
	w := Explain(address, msg)
	if w == nil || strings.HasPrefix(w.Why, "{addr} is an SSH server") || !strings.Contains(msg, "malformed HTTP") && !strings.Contains(msg, "unexpected protocol version") && !strings.Contains(msg, "readLoopPeekFailLocked") && !strings.Contains(msg, "EOF") && !strings.Contains(msg, "connection reset") {
		return w
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", w.Addr)
	if err != nil {
		return w
	}
	defer c.Close()
	d, _ := ctx.Deadline()
	_ = c.SetReadDeadline(d)
	b := make([]byte, 4)
	n := 0
	for n < len(b) {
		k, err := c.Read(b[n:])
		n += k
		if err != nil {
			break
		}
	}
	if bytes.Equal(b[:n], []byte("SSH-")) {
		w.Why = "{addr} is an SSH server, not a proxy: add it as SSH, with its user and password"
	}
	return w
}
