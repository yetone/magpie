package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// lanNetsForTest has the reach checks see this computer on nets, its
// loopback magpies taken as on a private network, and every verdict
// forgotten, until the test ends.
func lanNetsForTest(t *testing.T, nets *atomic.Value) {
	t.Helper()
	oldHost, oldNets, oldDial := lanReachHost, localNets, reachDial
	lanReachHost = func(host string) bool { return host == "127.0.0.1" }
	localNets = func() []*net.IPNet {
		_, n, _ := net.ParseCIDR(nets.Load().(string))
		return []*net.IPNet{n}
	}
	forget := func() {
		reachSeenOf.Lock()
		clear(reachSeenOf.m)
		reachSeenOf.Unlock()
		printSeen.Lock()
		printSeen.at = time.Time{}
		printSeen.Unlock()
	}
	forget()
	t.Cleanup(func() {
		lanReachHost, localNets, reachDial = oldHost, oldNets, oldDial
		forget()
	})
	t.Cleanup(provider.SeekHere(nil)) // no look around a real network
}

// moveTo has this computer on another network from now on.
func moveTo(nets *atomic.Value, cidr string) {
	nets.Store(cidr)
	printSeen.Lock()
	printSeen.at = time.Time{}
	printSeen.Unlock()
}

// remoteMagpieAt is a fake magpie answering chat completions with its
// name, counting what it was asked.
func remoteMagpieAt(t *testing.T, name string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from `+name+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func askLANGroup(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// AlanWang1027713 on X: two Wi-Fi networks on different subnets, a magpie
// on a computer in each, both in one routing group. On A's network the
// group still sent requests to B's magpie, which no connection reached.
// Now B, taking no connection on the network this computer is on, is
// passed over before anything is sent to it, and isn't rested for it;
// on B's network again (a new set of interface addresses) it is asked
// first, as its place in the group says.
func TestGroupPassesOverRemoteMagpieOnAnotherNetwork(t *testing.T) {
	fresh(t)
	var nets atomic.Value
	nets.Store("192.168.51.7/24")
	lanNetsForTest(t, &nets)
	var officeHits, homeHits atomic.Int32
	office := remoteMagpieAt(t, "office", &officeHits)
	home := remoteMagpieAt(t, "home", &homeHits)
	officeAt := strings.TrimPrefix(office.URL, "http://")
	// office's magpie is on 192.168.31.x: from 192.168.51.x nothing takes
	// the connection (the fake dial stands in for the network)
	var dials atomic.Int32
	reachDial = func(ctx context.Context, hostport string) error {
		if hostport == officeAt {
			dials.Add(1)
		}
		if hostport == officeAt && nets.Load() != "192.168.31.9/24" {
			return &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
		}
		return nil
	}
	for _, p := range []provider.Provider{
		{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: office.URL, Models: []string{"m"}},
		{ID: "home", Name: "Home", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-home", Chat: home.URL, Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"office/m", "home/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()

	// on A's network: home answers, office is never sent the request
	for range 3 {
		if got := askLANGroup(t, h); !strings.Contains(got, "from home") {
			t.Fatalf("on A's network: %s", got)
		}
	}
	if n := officeHits.Load(); n != 0 {
		t.Errorf("office, on another network, was asked %d times", n)
	}
	if r := restingNow(t); len(r) > 0 {
		t.Errorf("rests on A's network: %v", r)
	}
	if n := dials.Load(); n != 1 {
		t.Errorf("office's address dialled %d times for 3 requests on one network, want it once", n)
	}

	// back on B's network: office answers first, as its place says
	moveTo(&nets, "192.168.31.9/24")
	if got := askLANGroup(t, h); !strings.Contains(got, "from office") {
		t.Fatalf("on B's network: %s", got)
	}

	// and on A's again: passed over again, at once
	moveTo(&nets, "192.168.51.7/24")
	officeHits.Store(0)
	if got := askLANGroup(t, h); !strings.Contains(got, "from home") || officeHits.Load() != 0 {
		t.Fatalf("on A's network again: office asked %d times: %s", officeHits.Load(), got)
	}
}

// A Remote magpie that took a connection when it was last asked, and
// whose connection fails now (its computer left the network, or this one
// did between two checks), isn't rested for it: the next member answers,
// and the one that failed is passed over without being dialled again
// until its verdict is checked again, behind the requests.
func TestRemoteMagpieConnectFailureIsNotRested(t *testing.T) {
	fresh(t)
	var nets atomic.Value
	nets.Store("192.168.51.7/24")
	lanNetsForTest(t, &nets)
	var homeHits atomic.Int32
	home := remoteMagpieAt(t, "home", &homeHits)
	var dials atomic.Int32
	var mu sync.Mutex
	up := true // office's address took the connection when last dialled
	reachDial = func(ctx context.Context, hostport string) error {
		dials.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if hostport == "127.0.0.1:1" && !up {
			return &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
		}
		return nil
	}
	// nothing listens on port 1: the real connection is refused
	for _, p := range []provider.Provider{
		{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: "http://127.0.0.1:1", Models: []string{"m"}},
		{ID: "home", Name: "Home", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-home", Chat: home.URL, Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"office/m", "home/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	if got := askLANGroup(t, h); !strings.Contains(got, "from home") {
		t.Fatalf("first: %s", got)
	}
	if r := restingNow(t); len(r) > 0 {
		t.Errorf("rested for a connection never made: %v", r)
	}
	mu.Lock()
	up = false
	mu.Unlock()
	before := dials.Load()
	if got := askLANGroup(t, h); !strings.Contains(got, "from home") {
		t.Fatalf("second: %s", got)
	}
	if n := dials.Load() - before; n != 0 {
		t.Errorf("office dialled %d times again within its verdict, want none", n)
	}
	o, _ := provider.Find("office")
	if !lanUnreachable(*o) {
		t.Error("office's failed connection didn't set its verdict")
	}
}

// What is judged: a Remote magpie at a private address or a .local name,
// not through a proxy of its own; never a cloud provider, a public
// address, a name, or this computer.
func TestReachJudgesRemoteMagpiesOnPrivateNetworksAlone(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://192.168.31.5:3425/v1"}, true},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://10.0.0.2:3425/v1"}, true},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://studio.local:3425/v1"}, true},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://127.0.0.1:3425/v1"}, false},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "https://magpie.example.com/v1"}, false},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://100.101.1.2:3425/v1"}, false},
		{provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://192.168.31.5:3425/v1", Proxy: "socks5://127.0.0.1:1080"}, false},
		{provider.Provider{Chat: "http://192.168.31.5:11434/v1"}, false},
	} {
		if got := reachAddr(c.p) != ""; got != c.want {
			t.Errorf("%s (proxy %q, preset %q): judged %v, want %v", c.p.Chat, c.p.Proxy, c.p.Preset, got, c.want)
		}
	}
}
