package gateway

import (
	"bytes"
	"context"
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

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/yetone/magpie/internal/netproxy"
)

// pingPolicyVendor is an HTTP/2 vendor that answers each request after a
// silence, and holds its clients to gRPC's ping policy, as Google's front
// end does (grpc keepalive.md, chttp2's ping abuse policy): a ping that
// comes within minGap of the last one, with no HEADERS or DATA sent
// between, is a strike, and past two strikes the connection ends with
// GOAWAY ENHANCE_YOUR_CALM "too_many_pings".
type pingPolicyVendor struct {
	ln      net.Listener
	cert    *x509.Certificate
	silence time.Duration
	minGap  time.Duration

	mu      sync.Mutex
	conns   int
	pings   []time.Time
	goaways int
	wg      sync.WaitGroup
}

func newPingPolicyVendor(t *testing.T, silence, minGap time.Duration) *pingPolicyVendor {
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
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	v := &pingPolicyVendor{ln: ln, cert: cert, silence: silence, minGap: minGap}
	conf := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"h2"},
	}
	var open []net.Conn
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			v.mu.Lock()
			v.conns++
			open = append(open, c)
			v.mu.Unlock()
			v.wg.Add(1)
			go func() {
				defer v.wg.Done()
				defer c.Close()
				v.serve(tls.Server(c, conf))
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		v.mu.Lock()
		for _, c := range open {
			c.Close()
		}
		v.mu.Unlock()
		v.wg.Wait()
	})
	return v
}

func (v *pingPolicyVendor) Addr() string { return v.ln.Addr().String() }

func (v *pingPolicyVendor) serve(c *tls.Conn) {
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(c, preface); err != nil || string(preface) != http2.ClientPreface {
		return
	}
	fr := http2.NewFramer(c, c)
	var wmu sync.Mutex
	write := func(f func() error) error {
		wmu.Lock()
		defer wmu.Unlock()
		return f()
	}
	if write(func() error { return fr.WriteSettings() }) != nil {
		return
	}
	// the policy's state: chttp2 resets both when it sends HEADERS or DATA
	var pmu sync.Mutex
	var lastPing time.Time
	strikes := 0
	var lastStream uint32
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				write(fr.WriteSettingsAck)
			}
		case *http2.PingFrame:
			if f.IsAck() {
				continue
			}
			now := time.Now()
			v.mu.Lock()
			v.pings = append(v.pings, now)
			v.mu.Unlock()
			pmu.Lock()
			if !lastPing.IsZero() && now.Sub(lastPing) < v.minGap {
				strikes++
			}
			lastPing = now
			calm := strikes > 2
			last := lastStream
			pmu.Unlock()
			if calm {
				v.mu.Lock()
				v.goaways++
				v.mu.Unlock()
				write(func() error {
					return fr.WriteGoAway(last, http2.ErrCodeEnhanceYourCalm, []byte("too_many_pings"))
				})
				// the in-flight stream is not answered: GFE closes it
				time.Sleep(50 * time.Millisecond)
				return
			}
			data := f.Data
			write(func() error { return fr.WritePing(true, data) })
		case *http2.HeadersFrame:
			id := f.StreamID
			pmu.Lock()
			lastStream = id
			pmu.Unlock()
			v.mu.Lock()
			silence := v.silence
			v.mu.Unlock()
			go func() {
				time.Sleep(silence)
				var hb bytes.Buffer
				enc := hpack.NewEncoder(&hb)
				enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
				enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "text/plain"})
				pmu.Lock()
				strikes, lastPing = 0, time.Time{}
				pmu.Unlock()
				write(func() error {
					if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: hb.Bytes(), EndHeaders: true}); err != nil {
						return err
					}
					return fr.WriteData(id, true, []byte("thought it through"))
				})
			}()
		}
	}
}

// policyPings has the gateway's transports, made after it, trust vendor,
// and ping as the real ones do with the times scaled down: a silence's
// first ping after 100ms (PingAfter, 10s), the next 1s on (PingAgain,
// 5 minutes), against a vendor whose minimum gap is 600ms (gRPC's 5
// minutes): room on both sides of it.
func policyPings(t *testing.T, vendor *pingPolicyVendor) {
	pool := x509.NewCertPool()
	pool.AddCert(vendor.cert)
	wasTLS := upstreamTLS
	wasAfter, wasWait, wasAgain := netproxy.PingAfter, netproxy.PingWait, netproxy.PingAgain
	upstreamTLS = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
	netproxy.PingAfter, netproxy.PingWait, netproxy.PingAgain = 100*time.Millisecond, time.Second, time.Second
	t.Cleanup(func() {
		upstreamTLS = wasTLS
		netproxy.PingAfter, netproxy.PingWait, netproxy.PingAgain = wasAfter, wasWait, wasAgain
	})
}

// zdevoz (Discord): gemini-4-argon through Antigravity thought for about
// four minutes before its first byte, and Google ended the connection:
// "http2: server sent GOAWAY and closed the connection; LastStreamID=3,
// ErrCode=ENHANCE_YOUR_CALM, debug=\"too_many_pings\"". The turn was lost
// at ~235s and sent again from the start. Go's HTTP/2 health check pinged
// the silent connection every PingAfter (its ping's own answer restarts
// the clock), 24 pings in four minutes; Google's front end takes pings
// that close together with nothing sent between as abuse. Now a silence
// gets its first ping at PingAfter, and the next PingAgain later, so the
// vendor thinking for 2.5 (scaled) minutes answers on the first try.
func TestLongSilenceIsNotPingedIntoGoaway(t *testing.T) {
	fresh(t)
	vendor := newPingPolicyVendor(t, 2500*time.Millisecond, 600*time.Millisecond)
	policyPings(t, vendor)
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+vendor.Addr()+"/v1internal:streamGenerateContent?alt=sse", strings.NewReader(`{"request":{}}`))
	began := time.Now()
	res, err := s.client.Do(req)
	took := time.Since(began)
	vendor.mu.Lock()
	pings := append([]time.Time(nil), vendor.pings...)
	goaways, conns := vendor.goaways, vendor.conns
	vendor.mu.Unlock()
	if err != nil {
		t.Fatalf("the turn failed after %v, %d pings, %d GOAWAYs: %v", took, len(pings), goaways, err)
	}
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(b) != "thought it through" {
		t.Fatalf("the turn's answer: %q, %v", b, err)
	}
	if goaways != 0 || conns != 1 {
		t.Fatalf("%d GOAWAYs over %d connections; want the turn answered on its one connection", goaways, conns)
	}
	// a silence is still pinged, from PingAfter on, so a dead path is
	// found as soon as before (#4936)
	if len(pings) == 0 {
		t.Fatal("the silent connection was never pinged")
	}
	if len(pings) > 4 {
		t.Fatalf("%d pings in a %v silence; want one at PingAfter and one each PingAgain", len(pings), vendor.silence)
	}
	if d := pings[0].Sub(began); d > time.Second {
		t.Fatalf("the first ping came %v into the silence; want it about PingAfter (100ms) in", d)
	}
}

// A GOAWAY ends the connection it came on, and the try sent again goes out
// on a new one, never the connection that refused it.
func TestTryAfterGoawayDialsAnew(t *testing.T) {
	fresh(t)
	// a vendor whose minimum gap is longer than PingAgain: the third ping
	// of a silence ends the connection
	vendor := newPingPolicyVendor(t, 2500*time.Millisecond, 5*time.Second)
	policyPings(t, vendor)
	netproxy.PingAgain = 300 * time.Millisecond
	tr := vendorTransport(time.Minute)
	t.Cleanup(tr.CloseIdleConnections)
	cl := &http.Client{Transport: tr}
	ask := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+vendor.Addr()+"/turn", strings.NewReader("{}"))
		res, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		_, err = io.ReadAll(res.Body)
		return err
	}
	err := ask()
	if err == nil || !strings.Contains(err.Error(), "too_many_pings") {
		t.Fatalf("the turn on a vendor that refuses the pings: %v; want its GOAWAY", err)
	}
	vendor.mu.Lock()
	vendor.silence = 0
	vendor.mu.Unlock()
	if err := ask(); err != nil {
		t.Fatalf("the try after the GOAWAY: %v", err)
	}
	vendor.mu.Lock()
	conns := vendor.conns
	vendor.mu.Unlock()
	if conns != 2 {
		t.Fatalf("%d connections dialled; want the try after the GOAWAY on a new one", conns)
	}
}
