// Package sshtest is an SSH server on loopback for tests of magpie's SSH
// proxy: it logs a user in by password or key and answers each
// direct-tcpip channel (ssh -D's CONNECT) with an http.Handler of its
// own, so nothing a test sends through it leaves the machine.
package sshtest

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is one SSH server a test stood up; it ends with the test.
type Server struct {
	Addr    string // host:port
	URL     string // ssh://host:port
	HostKey string // its host key, authorized_keys form
	signer  ssh.Signer
	ln      net.Listener

	mu      sync.Mutex
	targets []string // host:port of each channel opened, in order
	logins  int
}

// Options are who may log in and what a channel opened through the
// server answers. A nil Handler answers "carried <host><path>".
type Options struct {
	User, Pass string
	Key        ssh.PublicKey // a key the user may log in with
	Handler    http.Handler
	HostSigner ssh.Signer // the server's host key; a new one when nil
	// Version is the banner, "SSH-2.0-Go" when empty; a real OpenSSH's
	// has a space ("SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.20")
	Version string
	// Interactive offers keyboard-interactive and refuses it at once, as
	// OpenSSH without PAM does
	Interactive bool
	// Forward is how a forward is answered: "" carries it, "prohibit"
	// refuses it as AllowTcpForwarding no does, "hangup" ends the whole
	// connection as macOS 27's sshd does
	Forward string
}

// NewKey is a new ed25519 private key: the signer, and the key as an
// OpenSSH PEM a user would paste.
func NewKey(t testing.TB) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, string(pem.EncodeToMemory(b))
}

// Start stands the server up on 127.0.0.1.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	host := o.HostSigner
	if host == nil {
		host, _ = NewKey(t)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), signer: host, ln: ln}
	s.URL = "ssh://" + s.Addr
	s.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
	cfg := &ssh.ServerConfig{ServerVersion: o.Version}
	if o.Interactive {
		cfg.KeyboardInteractiveCallback = func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			return nil, errRefused
		}
	}
	if o.Pass != "" {
		cfg.PasswordCallback = func(m ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if m.User() == o.User && string(pass) == o.Pass {
				return nil, nil
			}
			return nil, errRefused
		}
	}
	if o.Key != nil {
		want := o.Key.Marshal()
		cfg.PublicKeyCallback = func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() == o.User && string(k.Marshal()) == string(want) {
				return nil, nil
			}
			return nil, errRefused
		}
	}
	cfg.AddHostKey(host)
	h := o.Handler
	if h == nil {
		h = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte("carried " + r.Host + r.URL.Path))
		})
	}
	var wg sync.WaitGroup
	conns := map[net.Conn]bool{}
	var connsMu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns[c] = true
			connsMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(c, cfg, h, o.Forward)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		connsMu.Lock()
		for c := range conns {
			c.Close()
		}
		connsMu.Unlock()
		wg.Wait()
	})
	return s
}

// Close ends the server's listener and leaves its connections up: a
// test of what a tunnel does when the server goes away.
func (s *Server) Close() { s.ln.Close() }

// Targets is the host:port of every channel opened through the server.
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

// Logins is how many logins the server let in.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

type refused struct{}

func (refused) Error() string { return "refused" }

var errRefused = refused{}

func (s *Server) serve(c net.Conn, cfg *ssh.ServerConfig, h http.Handler, forward string) {
	defer c.Close()
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	s.mu.Lock()
	s.logins++
	s.mu.Unlock()
	// keepalive@openssh.com and the rest: answered, never acted on
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	defer wg.Wait()
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		target, ok := directTarget(nc.ExtraData())
		if !ok {
			_ = nc.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		s.mu.Lock()
		s.targets = append(s.targets, target)
		s.mu.Unlock()
		switch forward {
		case "prohibit":
			_ = nc.Reject(ssh.Prohibited, "open failed")
			continue
		case "hangup":
			conn.Close()
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer ch.Close()
			br := bufio.NewReader(ch)
			for {
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				res := rec.Result()
				res.ContentLength = int64(rec.Body.Len())
				if res.Write(ch) != nil {
					return
				}
			}
		}()
	}
}

// directTarget reads a direct-tcpip channel's host and port (RFC 4254
// 7.2).
func directTarget(b []byte) (string, bool) {
	str := func() (string, bool) {
		if len(b) < 4 {
			return "", false
		}
		n := binary.BigEndian.Uint32(b)
		if uint32(len(b)-4) < n {
			return "", false
		}
		s := string(b[4 : 4+n])
		b = b[4+n:]
		return s, true
	}
	host, ok := str()
	if !ok || len(b) < 4 {
		return "", false
	}
	port := binary.BigEndian.Uint32(b)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), true
}
