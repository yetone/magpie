package netproxy

// The plugin host's Bun speaks http:// and https:// proxies only: a fetch
// given socks5:// fails as UnsupportedProxyProtocol. Bridge stands an
// http:// proxy on loopback in front of a SOCKS5 one, so a plugin's
// requests go through the SOCKS proxy a built-in's go through.

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

var (
	bridgesMu sync.Mutex
	bridges   = map[string]string{}
)

// IsSOCKS is whether the proxy's address is a SOCKS5 one.
func IsSOCKS(u *url.URL) bool { return u != nil && (u.Scheme == "socks5" || u.Scheme == "socks5h") }

// Bridge is the http:// address of a proxy on loopback carrying what it is
// given through the SOCKS5 proxy socks, one for each, kept while magpie
// runs.
func Bridge(socks *url.URL) (string, error) {
	key := socks.String()
	bridgesMu.Lock()
	defer bridgesMu.Unlock()
	if a, ok := bridges[key]; ok {
		return a, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s := *socks
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go bridgeConn(c, &s)
		}
	}()
	a := "http://" + ln.Addr().String()
	bridges[key] = a
	return a, nil
}

// bridgeConn serves one client of a bridge: a CONNECT is tunnelled, a
// plain http:// request sent on and answered.
func bridgeConn(c net.Conn, socks *url.URL) {
	defer c.Close()
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(time.Minute))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if req.Method == http.MethodConnect {
		up, err := DialSOCKS(ctx, socks, req.Host)
		if err != nil {
			refuse(c, err)
			return
		}
		defer up.Close()
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, br); closeWrite(up); done <- struct{}{} }()
		go func() { _, _ = io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
		<-done
		<-done
		return
	}
	t := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return DialSOCKS(ctx, socks, addr)
		},
	}
	defer t.CloseIdleConnections()
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	res, err := t.RoundTrip(req.WithContext(context.Background()))
	if err != nil {
		refuse(c, err)
		return
	}
	defer res.Body.Close()
	res.Close = true
	_ = res.Write(c)
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
	}
}

// refuse answers a client the SOCKS proxy didn't carry: a 502 saying why,
// in the words Go's own transport uses ("socks connect …").
func refuse(c net.Conn, err error) {
	msg := err.Error()
	fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
}

// DialSOCKS connects to addr (host:port) through the SOCKS5 proxy socks,
// the user and password in its address used when it has them. The host
// is sent by name, as Go's own transport sends it, so the proxy resolves
// it.
func DialSOCKS(ctx context.Context, socks *url.URL, addr string) (net.Conn, error) {
	proxy := socks.Host
	if socks.Port() == "" {
		proxy = net.JoinHostPort(socks.Hostname(), "1080")
	}
	fail := func(err error) error { return fmt.Errorf("socks connect tcp %s->%s: %w", proxy, addr, err) }
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fail(err)
	}
	port, err := strconv.Atoi(portS)
	if err != nil || port < 1 || port > 65535 {
		return nil, fail(fmt.Errorf("bad port %q", portS))
	}
	c, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, fail(err)
	}
	if d, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(d)
	} else {
		_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if err := socksHandshake(c, socks.User, host, port); err != nil {
		c.Close()
		return nil, fail(err)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

var socksReplies = map[byte]string{
	1: "general SOCKS server failure",
	2: "connection not allowed by ruleset",
	3: "network unreachable",
	4: "host unreachable",
	5: "connection refused",
	6: "TTL expired",
	7: "command not supported",
	8: "address type not supported",
}

func socksHandshake(c net.Conn, user *url.Userinfo, host string, port int) error {
	methods := []byte{0}
	if user != nil {
		methods = append(methods, 2)
	}
	if _, err := c.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	var b [2]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return err
	}
	if b[0] != 5 {
		return errors.New("not a SOCKS5 proxy")
	}
	switch b[1] {
	case 0:
	case 2:
		if user == nil {
			return errors.New("the proxy asks for a user and password")
		}
		pw, _ := user.Password()
		u := user.Username()
		if len(u) > 255 || len(pw) > 255 {
			return errors.New("user or password too long")
		}
		msg := append([]byte{1, byte(len(u))}, u...)
		msg = append(append(msg, byte(len(pw))), pw...)
		if _, err := c.Write(msg); err != nil {
			return err
		}
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return err
		}
		if b[1] != 0 {
			return errors.New("the proxy turned the user and password away")
		}
	default:
		return errors.New("no authentication method the proxy takes")
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(append(req, 1), ip4...)
		} else {
			req = append(append(req, 4), ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("host name too long")
		}
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return err
	}
	if h[1] != 0 {
		if m, ok := socksReplies[h[1]]; ok {
			return errors.New(m)
		}
		return fmt.Errorf("unknown SOCKS reply %d", h[1])
	}
	var skip int
	switch h[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return err
		}
		skip = int(n[0])
	default:
		return fmt.Errorf("unknown address type %d", h[3])
	}
	_, err := io.CopyN(io.Discard, c, int64(skip+2))
	return err
}

// ForBun is a proxy's address as the plugin host's Bun can use it: a
// SOCKS5 one is bridged, any other is as it is.
func ForBun(proxy string) string {
	u, err := Parse(proxy)
	if err != nil || !IsSOCKS(u) {
		return proxy
	}
	if a, err := Bridge(u); err == nil {
		return a
	}
	return proxy
}
