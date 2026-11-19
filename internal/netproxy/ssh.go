package netproxy

// SSH proxies (@laiuuui on X: a VPS gives only SSH, an address, a port and
// a password or key). A saved proxy whose address is ssh://host:port is
// carried as `ssh -D` carries it, with no ssh command on the computer:
// magpie logs in with golang.org/x/crypto/ssh and stands a SOCKS5 proxy on
// loopback, with a login of its own, whose connections it opens as
// channels of that SSH connection. The saved proxy resolves to that SOCKS
// address (settings.SSHAddress), so every path a SOCKS proxy takes (a
// transport, a CLI's *_PROXY, Bun's HTTP bridge) takes it too, and the
// destination's name is resolved at the server's end.
//
// The server's key is trusted on first use and written into the saved
// proxy (HostKey); after that a server presenting another key is refused,
// until the user trusts it again from the proxy's editor.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/yetone/magpie/internal/settings"
)

func init() { settings.SSHAddress = sshAddress }

// SSHTimeout bounds an SSH login: the TCP connection, the handshake and
// the authentication together.
var SSHTimeout = 15 * time.Second

// sshKeepAlive is how often an idle SSH connection is asked whether it is
// still there; one that doesn't answer in SSHTimeout is dropped, and the
// next request logs in again.
var sshKeepAlive = 20 * time.Second

// HostKeyChangedError is an SSH server presenting a key other than the one
// trusted for it.
type HostKeyChangedError struct{ Was, Now string }

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("ssh: host key changed: was %s, now %s", e.Was, e.Now)
}

// HostKeyFingerprint is the SHA256 fingerprint of a key in authorized_keys
// form, as `ssh-keygen -l` prints it; "" for one that doesn't parse.
func HostKeyFingerprint(authorized string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

type sshTunnel struct {
	ln         net.Listener
	addr       string // socks5h://user:pass@127.0.0.1:port
	user, pass string
	save       bool // a key trusted on first use is written into settings

	mu      sync.Mutex
	cfg     settings.NamedProxy
	client  *ssh.Client
	lastErr error
	closed  bool

	connMu sync.Mutex // one login at a time
}

var (
	tunnelsMu sync.Mutex
	tunnels   = map[string]*sshTunnel{} // by saved proxy id
)

// sshAddress is the SOCKS address of the saved SSH proxy p, a tunnel stood
// up for it the first time it is asked for. The SSH login happens when a
// request first goes through it, not here: this runs for every request.
func sshAddress(p settings.NamedProxy) (string, error) {
	tunnelsMu.Lock()
	defer tunnelsMu.Unlock()
	if t := tunnels[p.ID]; t != nil {
		if t.same(p) {
			return t.addr, nil
		}
		t.Close()
		delete(tunnels, p.ID)
	}
	t, err := openTunnel(p, p.ID != "")
	if err != nil {
		return "", err
	}
	if p.ID != "" {
		tunnels[p.ID] = t
	}
	return t.addr, nil
}

// CloseSSH ends the tunnel of the saved proxy id: it was deleted, or its
// host key is to be learnt again.
func CloseSSH(id string) {
	tunnelsMu.Lock()
	defer tunnelsMu.Unlock()
	if t := tunnels[id]; t != nil {
		t.Close()
		delete(tunnels, id)
	}
}

// SSHReady logs in to the saved SSH proxy p through the tunnel requests
// take (nil when it is logged in already), so a check can say why it
// can't carry them: a refused login reaches a request only as a SOCKS
// failure.
func SSHReady(ctx context.Context, p settings.NamedProxy) error {
	if _, err := sshAddress(p); err != nil {
		return err
	}
	tunnelsMu.Lock()
	t := tunnels[p.ID]
	tunnelsMu.Unlock()
	if t == nil {
		return errors.New("the SSH proxy isn't saved")
	}
	_, err := t.conn(ctx)
	return err
}

// OpenSSH stands a tunnel for an SSH proxy not saved yet (a check of what
// is being typed): its address, and a func that ends it. A key it trusts
// on first use is kept in it alone.
func OpenSSH(p settings.NamedProxy) (string, func(), error) {
	t, err := openTunnel(p, false)
	if err != nil {
		return "", nil, err
	}
	return t.addr, t.Close, nil
}

// CheckSSH logs in to the SSH server of p once, and says the key it
// presented (authorized_keys form): the one to trust, when p trusts none
// yet. A server whose key differs from the trusted one is refused
// (HostKeyChangedError).
func CheckSSH(ctx context.Context, p settings.NamedProxy) (string, error) {
	t := &sshTunnel{cfg: p}
	c, err := t.login(ctx)
	if err != nil {
		return "", err
	}
	c.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cfg.HostKey, nil
}

func openTunnel(p settings.NamedProxy, save bool) (*sshTunnel, error) {
	if _, _, err := sshTarget(p); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("the SSH proxy's local end didn't start: %w", err)
	}
	t := &sshTunnel{ln: ln, cfg: p, user: "magpie", pass: randomHex(16), save: save}
	t.addr = (&url.URL{Scheme: "socks5h", User: url.UserPassword(t.user, t.pass), Host: ln.Addr().String()}).String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go t.serve(c)
		}
	}()
	return t, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// same is whether the tunnel carries the saved proxy p as it is now. A
// host key the tunnel learnt itself and p doesn't hold yet (being written)
// is the same; one p trusts that differs is not.
func (t *sshTunnel) same(p settings.NamedProxy) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cfg
	if c.URL != p.URL || c.User != p.User || c.Pass != p.Pass || c.Key != p.Key {
		return false
	}
	return p.HostKey == "" || p.HostKey == c.HostKey
}

// Close ends the tunnel: its listener and its SSH connection.
func (t *sshTunnel) Close() {
	t.mu.Lock()
	t.closed = true
	c := t.client
	t.client = nil
	t.mu.Unlock()
	if t.ln != nil {
		t.ln.Close()
	}
	if c != nil {
		c.Close()
	}
}

// sshTarget is the host:port of p's server and its login.
func sshTarget(p settings.NamedProxy) (string, *ssh.ClientConfig, error) {
	u, err := url.Parse(strings.TrimSpace(p.URL))
	if err != nil || u.Hostname() == "" {
		return "", nil, &Why{Why: "an SSH server looks like 203.0.113.5:22"}
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	var auth []ssh.AuthMethod
	if strings.TrimSpace(p.Key) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(p.Key))
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			if p.Pass == "" {
				return "", nil, &Why{Why: "the private key has a passphrase: type it in the password field"}
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(p.Key), []byte(p.Pass))
			if err != nil {
				return "", nil, &Why{Why: "the private key's passphrase is wrong"}
			}
		} else if err != nil {
			return "", nil, &Why{Why: "the private key can't be read: paste the whole file, from its BEGIN line to its END line", Err: err}
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if p.Pass != "" {
		pass := p.Pass
		auth = append(auth, ssh.Password(pass), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			out := make([]string, len(qs))
			for i := range out {
				out[i] = pass
			}
			return out, nil
		}))
	}
	if len(auth) == 0 {
		return "", nil, &Why{Why: "an SSH server needs a password or a private key"}
	}
	cfg := &ssh.ClientConfig{User: strings.TrimSpace(p.User), Auth: auth, Timeout: SSHTimeout}
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(p.HostKey)); err == nil {
		// the server is asked for the kind of key trusted, which it may
		// hold beside others: offered another kind, it would look changed
		cfg.HostKeyAlgorithms = hostKeyAlgorithms(k.Type())
	}
	return net.JoinHostPort(u.Hostname(), port), cfg, nil
}

func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// login opens an SSH connection to the tunnel's server, checking its key
// against the trusted one, or trusting it when there is none.
func (t *sshTunnel) login(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	p := t.cfg
	t.mu.Unlock()
	addr, cfg, err := sshTarget(p)
	if err != nil {
		return nil, err
	}
	var learnt string
	cfg.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if p.HostKey == "" {
			learnt = got
			return nil
		}
		want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(p.HostKey))
		if err == nil && bytes.Equal(want.Marshal(), key.Marshal()) {
			return nil
		}
		was := HostKeyFingerprint(p.HostKey)
		if was == "" {
			was = "unreadable"
		}
		return &HostKeyChangedError{Was: was, Now: ssh.FingerprintSHA256(key)}
	}
	ctx, cancel := context.WithTimeout(ctx, SSHTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, sshWhy(addr, err)
	}
	if d, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(d)
	}
	// a context ended mid-handshake ends it
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	stop()
	if err != nil {
		raw.Close()
		return nil, sshWhy(addr, err)
	}
	_ = raw.SetDeadline(time.Time{})
	if learnt != "" {
		t.mu.Lock()
		if t.cfg.HostKey == "" {
			t.cfg.HostKey = learnt
		}
		t.mu.Unlock()
		if t.save {
			saveHostKey(p, learnt)
		}
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

// saveHostKey writes the key trusted on first use into the saved proxy, if
// it is still the one logged in to and trusts none.
func saveHostKey(p settings.NamedProxy, key string) {
	s := settings.Load()
	for i := range s.Proxies {
		q := &s.Proxies[i]
		if q.ID == p.ID && q.URL == p.URL && q.HostKey == "" {
			q.HostKey = key
			_ = settings.Save(s)
			return
		}
	}
}

// SSHForwardWhy is why a request through a logged-in SSH proxy failed,
// from the error its local SOCKS end gave: the server's own address, not
// the tunnel's, is the one to check. nil when the error says nothing an
// SSH server is known for.
func SSHForwardWhy(p settings.NamedProxy, msg string) *Why {
	addr, _, err := sshTarget(p)
	if err != nil {
		return nil
	}
	w := &Why{Addr: addr, Err: textErr(msg)}
	switch {
	case strings.Contains(msg, "not allowed by ruleset"):
		w.Why = "the SSH server at {addr} let magpie log in but won't forward connections: its sshd_config needs AllowTcpForwarding yes"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "unreachable"):
		w.Why = "the SSH server at {addr} couldn't reach the site: check that the server itself reaches the internet"
	default:
		return nil
	}
	return w
}

// sshWhy is an SSH login's failure said as Why: what to check, the
// library's words beside it.
func sshWhy(addr string, err error) error {
	w := &Why{Addr: addr, Err: err}
	var changed *HostKeyChangedError
	msg := err.Error()
	switch {
	case errors.As(err, &changed):
		w.Why, w.Was, w.Now = "the SSH server's host key changed: it was {was} and is now {now}. If the server was reinstalled, trust the new key under Advanced", changed.Was, changed.Now
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "unexpected message type 51 "):
		// 51 is a refused login: OpenSSH without PAM refuses
		// keyboard-interactive at once, where the library waits for its
		// questions (60), after the password was turned away
		w.Why = "the SSH server at {addr} turned the login away: check the user and the password or key"
	case refused(msg):
		w.Why = "nothing answers SSH at {addr}: check the address and port"
	case strings.Contains(msg, "no such host"):
		w.Why = "the SSH server's name doesn't resolve"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		w.Why = "no answer from {addr} in time: check the address, and that the server's firewall lets SSH in"
	case strings.Contains(msg, "handshake failed"), strings.Contains(msg, "EOF"):
		w.Why = "{addr} doesn't speak SSH, or hung up"
	default:
		w.Why = "SSH to {addr} failed"
	}
	return w
}

// conn is the tunnel's SSH connection, logged in to now if it has none.
func (t *sshTunnel) conn(ctx context.Context) (*ssh.Client, error) {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	t.mu.Lock()
	if c := t.client; c != nil || t.closed {
		t.mu.Unlock()
		if c == nil {
			return nil, errors.New("the SSH proxy was closed")
		}
		return c, nil
	}
	t.mu.Unlock()
	c, err := t.login(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastErr = err
	if err != nil {
		return nil, err
	}
	if t.closed {
		c.Close()
		return nil, errors.New("the SSH proxy was closed")
	}
	t.client = c
	go t.watch(c)
	return c, nil
}

// watch drops c from the tunnel when it ends, or stops answering, so the
// next request logs in again instead of waiting on a dead connection.
func (t *sshTunnel) watch(c *ssh.Client) {
	done := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(done)
	}()
	tick := time.NewTicker(sshKeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-done:
			t.drop(c)
			return
		case <-tick.C:
			answered := make(chan error, 1)
			go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); answered <- err }()
			select {
			case err := <-answered:
				if err != nil {
					c.Close()
				}
			case <-time.After(SSHTimeout):
				c.Close()
			case <-done:
			}
		}
	}
}

func (t *sshTunnel) drop(c *ssh.Client) {
	t.mu.Lock()
	if t.client == c {
		t.client = nil
	}
	t.mu.Unlock()
	c.Close()
}

// dial opens addr through the SSH server, logging in again once when the
// connection it had turned out to be gone.
func (t *sshTunnel) dial(ctx context.Context, addr string) (net.Conn, error) {
	c, err := t.conn(ctx)
	if err != nil {
		return nil, err
	}
	up, err := c.DialContext(ctx, "tcp", addr)
	var open *ssh.OpenChannelError
	if err == nil || errors.As(err, &open) || ctx.Err() != nil {
		return up, err
	}
	t.drop(c)
	if c, err = t.conn(ctx); err != nil {
		return nil, err
	}
	return c.DialContext(ctx, "tcp", addr)
}

// serve is one SOCKS5 client of the tunnel: its login checked, then one
// CONNECT carried through the SSH server.
func (t *sshTunnel) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(SSHTimeout + 15*time.Second))
	var b [2]byte
	if _, err := io.ReadFull(c, b[:]); err != nil || b[0] != 5 {
		return
	}
	methods := make([]byte, b[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if !bytes.Contains(methods, []byte{2}) {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	if _, err := c.Write([]byte{5, 2}); err != nil {
		return
	}
	if _, err := io.ReadFull(c, b[:]); err != nil || b[0] != 1 {
		return
	}
	user := make([]byte, b[1])
	if _, err := io.ReadFull(c, user); err != nil {
		return
	}
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return
	}
	pass := make([]byte, b[0])
	if _, err := io.ReadFull(c, pass); err != nil {
		return
	}
	if subtle.ConstantTimeCompare(user, []byte(t.user)) != 1 || subtle.ConstantTimeCompare(pass, []byte(t.pass)) != 1 {
		_, _ = c.Write([]byte{1, 1})
		return
	}
	if _, err := c.Write([]byte{1, 0}); err != nil {
		return
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return
	}
	var host string
	switch h[3] {
	case 1, 4:
		ip := make([]byte, map[byte]int{1: 4, 4: 16}[h[3]])
		if _, err := io.ReadFull(c, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 3:
		if _, err := io.ReadFull(c, b[:1]); err != nil {
			return
		}
		name := make([]byte, b[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
	default:
		socksReply(c, 8)
		return
	}
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return
	}
	if h[1] != 1 { // CONNECT only
		socksReply(c, 7)
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(b[:]))))
	ctx, cancel := context.WithTimeout(context.Background(), SSHTimeout+15*time.Second)
	up, err := t.dial(ctx, addr)
	cancel()
	if err != nil {
		var open *ssh.OpenChannelError
		code := byte(1)
		if errors.As(err, &open) {
			code = 5
			if open.Reason == ssh.Prohibited {
				code = 2
			}
		} else if strings.Contains(err.Error(), "in response to channel open") {
			// the server hung up on the forward, as macOS 27's sshd
			// does: it won't forward, like a refusal
			code = 2
		}
		socksReply(c, code)
		return
	}
	defer up.Close()
	if !socksReply(c, 0) {
		return
	}
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

func socksReply(c net.Conn, code byte) bool {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err == nil
}
