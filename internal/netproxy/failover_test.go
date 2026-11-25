package netproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeNet stands in for the ways to one computer: each name is a
// loopback server, or dead (the dial refused, as one on a network this
// computer isn't on), or hung (a dial nobody answers). Nothing leaves
// this computer.
type fakeNet struct {
	mu     sync.Mutex
	at     map[string]string // name:port → loopback host:port
	dead   map[string]bool
	hung   map[string]bool
	dialed map[string]int
	capped map[string]bool // dialled with a deadline
}

func newFakeNet() *fakeNet {
	lastAnswered.Lock()
	clear(lastAnswered.m)
	lastAnswered.Unlock()
	return &fakeNet{at: map[string]string{}, dead: map[string]bool{}, hung: map[string]bool{}, dialed: map[string]int{}, capped: map[string]bool{}}
}

func (n *fakeNet) serve(t *testing.T, name string, h http.HandlerFunc) {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	n.mu.Lock()
	n.at[name+":80"] = strings.TrimPrefix(s.URL, "http://")
	n.mu.Unlock()
}

func (n *fakeNet) set(m map[string]bool, name string, on bool) {
	n.mu.Lock()
	m[name+":80"] = on
	n.mu.Unlock()
}

func (n *fakeNet) count(name string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dialed[name+":80"]
}

func (n *fakeNet) dispatch() http.RoundTripper {
	// no connection kept between requests: each is dialled, as a way
	// that went down would be
	return Dispatch(&http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		n.mu.Lock()
		n.dialed[addr]++
		_, dl := ctx.Deadline()
		n.capped[addr] = dl
		to, dead, hung := n.at[addr], n.dead[addr], n.hung[addr]
		n.mu.Unlock()
		switch {
		case hung:
			<-ctx.Done()
			return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
		case dead || to == "":
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return (&net.Dialer{}).DialContext(ctx, network, to)
	}})
}

type seen struct {
	mu    sync.Mutex
	paths []string
	hosts []string
	body  []string
}

func (s *seen) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.hosts = append(s.hosts, r.Host)
		s.body = append(s.body, string(b))
		s.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, "ok")
	}
}

func (s *seen) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func post(t *testing.T, rt http.RoundTripper, ctx context.Context, u, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res
}

// evjohnicu on X: one computer reached at home by its LAN address, and
// from outside through Tailscale or EasyTier. With the LAN address
// taking no connection, a request goes to the next address, with its
// path, query and body, and the next request goes there first.
func TestFailoverAsksTheNextAddress(t *testing.T) {
	n := newFakeNet()
	lan, ts := &seen{}, &seen{}
	n.serve(t, "lan.test", lan.handler(200))
	n.serve(t, "ts.test", ts.handler(200))
	n.set(n.dead, "lan.test", true)
	rt := n.dispatch()
	ctx := WithFailover(context.Background(), "http://lan.test", "http://ts.test/")

	res := post(t, rt, ctx, "http://lan.test/v1/chat/completions?x=1", `{"model":"m"}`)
	if res.StatusCode != 200 || ts.n() != 1 {
		t.Fatalf("status %d, Tailscale asked %d times; want its answer", res.StatusCode, ts.n())
	}
	if ts.paths[0] != "/v1/chat/completions?x=1" || ts.body[0] != `{"model":"m"}` || ts.hosts[0] != "ts.test" {
		t.Errorf("Tailscale got %q %q host %q; want the request as sent, at its own host", ts.paths[0], ts.body[0], ts.hosts[0])
	}
	post(t, rt, ctx, "http://lan.test/v1/models", "")
	if n.count("lan.test") != 1 || ts.n() != 2 {
		t.Errorf("LAN dialled %d times, Tailscale asked %d; want the address that answered asked first", n.count("lan.test"), ts.n())
	}

	// back home, with Tailscale off: the LAN address again
	n.set(n.dead, "lan.test", false)
	n.set(n.dead, "ts.test", true)
	post(t, rt, ctx, "http://lan.test/v1/models", "")
	if lan.n() != 1 {
		t.Fatalf("LAN asked %d times; want 1 once Tailscale takes no connection", lan.n())
	}
	tsDials := n.count("ts.test")
	post(t, rt, ctx, "http://lan.test/v1/models", "")
	if lan.n() != 2 || n.count("ts.test") != tsDials {
		t.Errorf("LAN asked %d times, Tailscale dialled %d more; want the LAN address kept", lan.n(), n.count("ts.test")-tsDials)
	}
}

// An answer from the remote, a refusal or a server error among them, is
// its own: the request isn't sent again elsewhere.
func TestFailoverKeepsTheRemotesAnswer(t *testing.T) {
	for _, status := range []int{401, 403, 429, 502} {
		n := newFakeNet()
		lan, ts := &seen{}, &seen{}
		n.serve(t, "lan.test", lan.handler(status))
		n.serve(t, "ts.test", ts.handler(200))
		ctx := WithFailover(context.Background(), "http://lan.test", "http://ts.test")
		res := post(t, n.dispatch(), ctx, "http://lan.test/v1/responses", "{}")
		if res.StatusCode != status || ts.n() != 0 {
			t.Errorf("%d: got %d, other address asked %d times; want the remote's own answer", status, res.StatusCode, ts.n())
		}
	}
}

// An address nobody answers is given failoverTry while another is left,
// and the last one left the request's own wait.
func TestFailoverGivesUpOnAnAddressNobodyAnswers(t *testing.T) {
	old := failoverTry
	failoverTry = 50 * time.Millisecond
	t.Cleanup(func() { failoverTry = old })
	n := newFakeNet()
	ts := &seen{}
	n.serve(t, "et.test", ts.handler(200))
	n.set(n.hung, "lan.test", true)
	// a bound of the test's own, so a dial waited on for ever fails it
	bound, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx := WithFailover(bound, "http://lan.test:80", "http://et.test:80")
	start := time.Now()
	post(t, n.dispatch(), ctx, "http://lan.test/v1/models", "")
	if ts.n() != 1 || time.Since(start) > 10*time.Second {
		t.Fatalf("EasyTier asked %d times after %v; want it asked once the LAN dial was given up", ts.n(), time.Since(start))
	}
	n.mu.Lock()
	lastCapped := n.capped["et.test:80"]
	n.mu.Unlock()
	if lastCapped {
		t.Error("the last address left was dialled with a deadline of failover's own")
	}
}

// An address may stand under a path (a reverse proxy's), and the path
// below it goes after it.
func TestFailoverAddressWithAPath(t *testing.T) {
	n := newFakeNet()
	far := &seen{}
	n.serve(t, "far.test", far.handler(200))
	n.set(n.dead, "lan.test", true)
	ctx := WithFailover(context.Background(), "http://lan.test", "http://far.test/magpie")
	post(t, n.dispatch(), ctx, "http://lan.test/v1/messages", "{}")
	if far.n() != 1 || far.paths[0] != "/magpie/v1/messages" {
		t.Errorf("got %v; want /magpie/v1/messages", far.paths)
	}
}

// When no address takes the connection the error is the last one's, a
// connection never made, so what follows one (looking for where the
// remote moved) still does.
func TestFailoverNoneConnects(t *testing.T) {
	n := newFakeNet()
	n.set(n.dead, "lan.test", true)
	n.set(n.dead, "ts.test", true)
	ctx := WithFailover(context.Background(), "http://lan.test", "http://ts.test")
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://lan.test/v1/models", nil)
	_, err := n.dispatch().RoundTrip(req)
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" || n.count("lan.test") != 1 || n.count("ts.test") != 1 {
		t.Errorf("err %v, dialled %d and %d; want a dial error after each was tried once", err, n.count("lan.test"), n.count("ts.test"))
	}
}

// A request to another host, or with no addresses given, goes as before.
func TestFailoverLeavesOtherRequests(t *testing.T) {
	n := newFakeNet()
	other := &seen{}
	n.serve(t, "other.test", other.handler(200))
	n.serve(t, "ts.test", (&seen{}).handler(200))
	n.set(n.dead, "lan.test", true)
	ctx := WithFailover(context.Background(), "http://lan.test", "http://ts.test")
	post(t, n.dispatch(), ctx, "http://other.test/v1/models", "")
	if other.n() != 1 || n.count("ts.test") != 0 {
		t.Errorf("other host asked %d, Tailscale dialled %d", other.n(), n.count("ts.test"))
	}
	req, _ := http.NewRequest("GET", "http://lan.test/v1/models", nil)
	if _, err := n.dispatch().RoundTrip(req); err == nil || n.count("ts.test") != 0 {
		t.Errorf("no addresses given: err %v, Tailscale dialled %d; want the LAN error alone", err, n.count("ts.test"))
	}
}
