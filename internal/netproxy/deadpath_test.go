package netproxy

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A request whose connection's path dies after it was written — a proxy's
// tunnel whose far side a NAT or a relay dropped — is discovered only by
// the connection's health check, which goes out PingAfter after the last
// frame the connection received (#4936: a first try read EOF ~20.5s after
// it was written, then magpie's own retry succeeded at once on a new
// connection). The test watches the bound with the timings scaled down: a
// holed connection is pinged at PingAfter, dropped at PingAfter+PingWait,
// and the try on it fails with the connection, while the next one dials
// anew and succeeds.
func TestRequestOnAHoledConnectionFailsAtThePing(t *testing.T) {
	pingAfter, pingWait := time.Second, 500*time.Millisecond

	// The vendor, speaking HTTP/2 over TLS on a listener the shim pumps
	// raw bytes into, as a CONNECT tunnel would.
	srv := newH2Vendor(t)
	// The tunnel: a byte pump with a hole per connection.
	shim, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shim.Close() })
	var mu sync.Mutex
	holed := map[net.Conn]bool{}
	serverHits := 0
	go func() {
		for {
			c, err := shim.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				s, err := net.Dial("tcp", srv.Addr())
				if err != nil {
					return
				}
				defer s.Close()
				mu.Lock()
				serverHits++
				holed[c] = false
				mu.Unlock()
				defer func() { mu.Lock(); delete(holed, c); mu.Unlock() }()
				var once sync.Once
				go func() { // vendor -> client
					io.Copy(c, s)
					once.Do(func() { c.Close() })
				}()
				br := bufio.NewReader(c)
				buf := make([]byte, 16<<10)
				for {
					n, err := br.Read(buf)
					if n > 0 {
						mu.Lock()
						hole := holed[c]
						mu.Unlock()
						if !hole {
							if _, werr := s.Write(buf[:n]); werr != nil {
								once.Do(func() { c.Close() })
								return
							}
						}
					}
					if err != nil {
						once.Do(func() { s.Close() })
						return
					}
				}
			}(c)
		}
	}()
	punchHole := func() {
		mu.Lock()
		defer mu.Unlock()
		for c := range holed {
			holed[c] = true
		}
	}

	client := &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		HTTP2:             &http.HTTP2Config{SendPingTimeout: pingAfter, PingTimeout: pingWait},
	}}
	post := func() (time.Duration, int, error) {
		start := time.Now()
		req, err := http.NewRequest("POST", "https://"+shim.Addr().String()+"/turn", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		took := time.Since(start)
		if err != nil {
			return took, 0, err
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return took, res.StatusCode, nil
	}

	if took, code, err := post(); err != nil || code != 200 {
		t.Fatalf("the first turn: %d in %v, %v", code, took, err)
	}
	punchHole() // the tunnel's far side dies, the way a NAT session does

	took, code, err := post()
	mu.Lock()
	dials := serverHits
	mu.Unlock()
	if err == nil {
		t.Fatalf("the turn on the holed connection answered %d in %v; want the dead path found at the ping", code, took)
	}
	if took < pingAfter-pingAfter/4 {
		t.Fatalf("the turn failed after %v, before the health check was due (%v): %v", took, pingAfter, err)
	}
	if took > pingAfter+pingWait+3*time.Second {
		t.Fatalf("the turn failed after %v; the health check should have found the dead path by %v: %v", took, pingAfter+pingWait, err)
	}
	said := err.Error()
	if !containsAny(said, "connection lost", "EOF", "unexpected EOF", "closed") {
		t.Fatalf("the turn failed with %q; want the connection's own end", said)
	}
	// magpie's own retry: a new connection, the turn served.
	took, code, err = post()
	if err != nil || code != 200 {
		t.Fatalf("the retry: %d in %v, %v", code, took, err)
	}
	mu.Lock()
	dials = serverHits
	mu.Unlock()
	if dials != 2 {
		t.Fatalf("the vendor was dialed %d times; want the retry on a connection of its own", dials)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The health check is the only probe of a connection whose path has died
// silently: a turn in flight on it waits for the next one, so PingAfter is
// how long a vendor's silence before a turn's first byte can cost (#4936:
// a first try read EOF 20.5s in, then a retry that served at once — the
// 20s was the ping). Half of it keeps the budget PingAfter+PingWait at
// 20s, the margin a body under uploadBytes needs on an uplink of about
// 3KB/s before a ping queued behind it is mistaken for a lost one (#3909).
func TestPingAfterSizesTheDeadPathDiscovery(t *testing.T) {
	if PingAfter != 10*time.Second {
		t.Fatalf("PingAfter %v; want 10s: the dead path of a turn in flight is found by the next health check, and #3909's slow bodies keep the %v budget PingAfter+PingWait", PingAfter, PingAfter+PingWait)
	}
	if PingWait != 10*time.Second {
		t.Fatalf("PingWait %v; want 10s", PingWait)
	}
}

// h2Vendor is an HTTP/2 server on a plain listener a test's shim can pump
// raw bytes to.
type h2Vendor struct {
	ln net.Listener
}

func (v *h2Vendor) Addr() string { return v.ln.Addr().String() }

func newH2Vendor(t *testing.T) *h2Vendor {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1").To4()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: ok\n\n")
	}), TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"h2"},
	}}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { ln.Close() })
	return &h2Vendor{ln: ln}
}
