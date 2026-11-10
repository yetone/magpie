package gateway

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TLS: `magpie serve --tls-cert <file> --tls-key <file>` (or
// MAGPIE_TLS_CERT and MAGPIE_TLS_KEY) serves the gateway, and `magpie
// web`'s page, over HTTPS to other computers (RoderickZH on Discord: magpie
// on a server as a relay, reached over the internet). The certificate is
// read again when its files change, so a certbot renewal is served without
// a restart. A connection from this computer itself (loopback, or this
// computer calling its own address) may still speak plain HTTP on the same
// port: the agents, MCP servers and status checks magpie wires here call
// http://127.0.0.1, which no certificate for a server's name covers, and
// their requests never leave the machine. Every other peer gets TLS alone;
// plain HTTP from it is answered 400 by net/http.

// TLSCertEnv and TLSKeyEnv name the PEM certificate (its chain after it)
// and private key files the flags set.
const (
	TLSCertEnv = "MAGPIE_TLS_CERT"
	TLSKeyEnv  = "MAGPIE_TLS_KEY"
)

// TLSFiles are the certificate and key files set, "" for none.
func TLSFiles() (cert, key string) {
	return strings.TrimSpace(os.Getenv(TLSCertEnv)), strings.TrimSpace(os.Getenv(TLSKeyEnv))
}

// TLSOn: the gateway serves other computers over HTTPS: with the files
// set, with mTLS on (mtls.go), or with HTTPS on (https.go).
func TLSOn() bool {
	c, k := TLSFiles()
	return c != "" && k != "" || MTLSOn() || HTTPSOn()
}

// Scheme is the gateway's scheme for other computers: https with TLS on.
func Scheme() string {
	if TLSOn() {
		return "https"
	}
	return "http"
}

// SetTLS sets the files the flags name, as absolute paths (a service
// starts in another folder), after checking them as CheckTLS does.
func SetTLS(cert, key string) error {
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	cert, key = abs(strings.TrimSpace(cert)), abs(strings.TrimSpace(key))
	if err := checkTLS(cert, key); err != nil {
		return err
	}
	if cert == "" {
		return nil
	}
	os.Setenv(TLSCertEnv, cert)
	os.Setenv(TLSKeyEnv, key)
	return nil
}

// CheckTLS says why the files set can't be served: only one of the two,
// or a pair that doesn't load. nil with neither set.
func CheckTLS() error { return checkTLS(TLSFiles()) }

func checkTLS(cert, key string) error {
	switch {
	case cert == "" && key == "":
		return nil
	case key == "":
		return fmt.Errorf("TLS needs a key beside the certificate %s: give --tls-key <file> (or %s) too", cert, TLSKeyEnv)
	case cert == "":
		return fmt.Errorf("TLS needs a certificate beside the key %s: give --tls-cert <file> (or %s) too", key, TLSCertEnv)
	}
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		return fmt.Errorf("TLS: the certificate %s and key %s can't be served: %w", cert, key, err)
	}
	return nil
}

// TLSSummary says what the certificate served now is for and until when,
// for the serve banner: "" with TLS off or a pair that doesn't load.
func TLSSummary() string {
	if !TLSOn() {
		return ""
	}
	var c tls.Certificate
	var err error
	if cf, kf := TLSFiles(); cf != "" && kf != "" {
		c, err = tls.LoadX509KeyPair(cf, kf)
	} else {
		var own *tls.Certificate
		if own, err = settingsCert(nil); own != nil {
			c = *own
		}
	}
	if err != nil || len(c.Certificate) == 0 {
		return ""
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return ""
	}
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}
	s := strings.Join(names, ", ")
	if s != "" {
		s += " · "
	}
	return s + "valid until " + leaf.NotAfter.Format("2006-01-02")
}

// certFiles serves the pair in its files, read again when either changes
// (a renewal), in its GetCertificate. A pair that doesn't load then (a
// renewal half written) leaves the one served before in place.
type certFiles struct {
	cert, key string

	mu         sync.Mutex
	served     *tls.Certificate
	stamp, bad [2]fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{fi.ModTime(), fi.Size()}
}

func (f *certFiles) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := [2]fileStamp{stampOf(f.cert), stampOf(f.key)}
	if f.served != nil && (now == f.stamp || now == f.bad) {
		return f.served, nil
	}
	c, err := tls.LoadX509KeyPair(f.cert, f.key)
	if err != nil {
		if f.served == nil {
			return nil, err
		}
		f.bad = now
		log.Printf("TLS: %s and %s changed but don't load (%v): still serving the certificate read before", f.cert, f.key, err)
		return f.served, nil
	}
	f.served, f.stamp, f.bad = &c, now, [2]fileStamp{}
	return f.served, nil
}

// tlsConfig is the server's TLS for the files set, nil with none set.
func tlsConfig() (*tlsServer, error) {
	if err := CheckTLS(); err != nil {
		return nil, err
	}
	cert, key := TLSFiles()
	if cert == "" || key == "" {
		return nil, nil
	}
	f := &certFiles{cert: cert, key: key}
	if _, err := f.get(nil); err != nil {
		return nil, err
	}
	return &tlsServer{files: f}, nil
}

// serverTLS is what a listener serves with: the files set, or nothing
// until mTLS is turned on (tlsServer.config asks on each connection).
func serverTLS() (*tlsServer, error) {
	t, err := tlsConfig()
	if err != nil {
		return nil, err
	}
	if t == nil {
		t = &tlsServer{}
	}
	return t, nil
}

// ServeTLS wraps ln to serve TLS to other computers, with plain HTTP for
// this computer's own connections, when the files are set or mTLS is on,
// as each connection comes.
func ServeTLS(ln net.Listener) (net.Listener, error) {
	t, err := serverTLS()
	if err != nil {
		return ln, err
	}
	return newTLSListener(ln, t), nil
}

// fromThisComputer: the connection's peer is this computer — loopback, or
// this computer calling one of its own addresses, which the kernel answers
// without the network (a MAGPIE_ADDR on one interface's address, which
// the agents wired here then call). A var for the tests, which have no
// other computer to call from.
var fromThisComputer = func(c net.Conn) bool {
	r, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	if r.IP.IsLoopback() {
		return true
	}
	l, ok := c.LocalAddr().(*net.TCPAddr)
	return ok && l.IP.Equal(r.IP)
}

// sniffWait is how long a connection from this computer has to send its
// first byte, which says whether it speaks TLS.
const sniffWait = 10 * time.Second

// tlsListener hands the server each connection as TLS, unless it comes
// from this computer and its first byte isn't a TLS handshake's. The
// first byte is waited for on a goroutine of the connection's own, so a
// client that connects and says nothing (a browser's preconnect) never
// holds up the next.
type tlsListener struct {
	net.Listener
	serve   *tlsServer
	conns   chan net.Conn
	done    chan struct{}
	stopped chan struct{}
	err     error // why accepting stopped, set before stopped closes
	once    sync.Once
}

func newTLSListener(ln net.Listener, serve *tlsServer) *tlsListener {
	l := &tlsListener{Listener: ln, serve: serve, conns: make(chan net.Conn), done: make(chan struct{}), stopped: make(chan struct{})}
	go l.accept()
	return l
}

func (l *tlsListener) accept() {
	var wait time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() { //nolint:staticcheck // what net/http's Serve looks at
				// out of file descriptors and the like: wait, as net/http does
				wait = min(max(2*wait, 5*time.Millisecond), time.Second)
				select {
				case <-time.After(wait):
					continue
				case <-l.done:
				}
			}
			l.err = err
			close(l.stopped)
			return
		}
		wait = 0
		if !fromThisComputer(c) {
			// a WSL distro's agent may call over plain HTTP, and shows no
			// client certificate: the guards judge it by its key
			wsl := fromWSLNet(c)
			cfg := l.serve.config(!wsl)
			// (with --tls-cert alone, as before: TLS, and nothing asked)
			switch {
			case cfg == nil:
				l.taken(c)
			case wsl && (MTLSOn() || HTTPSOn()):
				go l.sniff(c, cfg)
			default:
				l.taken(tls.Server(c, cfg))
			}
			continue
		}
		cfg := l.serve.config(false)
		if cfg == nil {
			l.taken(c)
			continue
		}
		go l.sniff(c, cfg)
	}
}

func (l *tlsListener) sniff(c net.Conn, cfg *tls.Config) {
	c.SetReadDeadline(time.Now().Add(sniffWait))
	first := make([]byte, 1)
	if _, err := io.ReadFull(c, first); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	p := &peeked{Conn: c, r: io.MultiReader(bytes.NewReader(first), c)}
	if first[0] == 0x16 { // a TLS record of type handshake
		l.hand(tls.Server(p, cfg))
		return
	}
	l.hand(p)
}

func (l *tlsListener) hand(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
	}
}

// taken hands the server a connection the listener's own Accept returned,
// as a bare listener's Accept would, even when Close came while it was
// being taken: the server's Accept waits for the loop to stop, so a
// connection taken just before the end began (a handover's) is served,
// not reset. It waits a second at most for a server no longer accepting.
func (l *tlsListener) taken(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		select {
		case l.conns <- c:
		case <-time.After(time.Second):
			c.Close()
		}
	}
}

// Accept returns the next connection, and an error only once the loop
// taking them has stopped (Close ends it), so none it took is lost.
func (l *tlsListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.stopped:
		return nil, l.err
	}
}

func (l *tlsListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// peeked is a connection whose first byte was read to tell TLS from plain
// HTTP, and is read again from r.
type peeked struct {
	net.Conn
	r io.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }
