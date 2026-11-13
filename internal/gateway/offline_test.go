package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// TestOfflineRestsNobody (usrname1833 on Discord): with this computer off
// the network, a routing group's members each failed to connect, each
// rested, and the first stayed behind the others once the network was
// back. A connection that fails while nothing can be reached rests nobody;
// one provider failing while another's host takes a connection, or
// while replies came in lately, still rests that one.
func TestOfflineRestsNobody(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	wasNear, wasNet := nearHost, hasNetwork
	t.Cleanup(func() {
		nearHost, hasNetwork = wasNear, wasNet
		forgetNetwork()
	})
	// the test's servers are all on loopback: they stand for the
	// providers' hosts out on the network
	nearHost = func(string) bool { return false }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from b"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	deadA := deadAddr(t, up.Listener.Addr().String())
	deadB := deadAddr(t, up.Listener.Addr().String(), deadA)

	resting := func() int {
		restingUntil.Lock()
		defer restingUntil.Unlock()
		n := 0
		for _, until := range restingUntil.m {
			if until.After(time.Now()) {
				n++
			}
		}
		return n
	}
	setup := func(t *testing.T, bURL string, netUp bool) *Server {
		t.Helper()
		fresh(t)
		forgetNetwork()
		hasNetwork = func() bool { return netUp }
		for _, p := range []provider.Provider{
			{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://" + deadA + "/v1"},
			{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: bURL},
		} {
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.SaveGroup(provider.Group{Name: "Pair", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		return New()
	}
	ask := func(t *testing.T, s *Server) (int, string, *Route) {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"group/pair","messages":[{"role":"user","content":"hi"}]}`)
		return code, body, s.trace.routes[len(s.trace.routes)-1]
	}

	t.Run("nothing reachable", func(t *testing.T) {
		s := setup(t, "http://"+deadB+"/v1", true)
		code, body, r := ask(t, s)
		if code < 500 {
			t.Fatalf("%d %s", code, body)
		}
		if len(r.Tries) < 2 {
			t.Fatalf("tries %+v", r.Tries)
		}
		if f := r.Tries[0]; f.Fail != failOffline || f.Rest != nil || !strings.Contains(f.Error, deadA) {
			t.Fatalf("a's try %+v", f)
		}
		if f := r.Tries[len(r.Tries)-1]; f.Fail != failOffline || f.Rest != nil && f.Rest.Until.After(time.Now()) {
			t.Fatalf("b's try %+v", f)
		}
		if n := resting(); n != 0 {
			t.Fatalf("%d set aside while this computer was off the network", n)
		}
	})

	t.Run("no interface up", func(t *testing.T) {
		s := setup(t, up.URL+"/v1", false)
		code, body, r := ask(t, s)
		if code != 200 || !strings.Contains(body, "from b") {
			t.Fatalf("%d %s", code, body)
		}
		if f := r.Tries[0]; f.Fail != failOffline || f.Rest != nil {
			t.Fatalf("a's try %+v", f)
		}
		if n := resting(); n != 0 {
			t.Fatalf("%d set aside with no network", n)
		}
	})

	t.Run("another provider reachable", func(t *testing.T) {
		s := setup(t, up.URL+"/v1", true)
		code, body, r := ask(t, s)
		if code != 200 || !strings.Contains(body, "from b") {
			t.Fatalf("%d %s", code, body)
		}
		if f := r.Tries[0]; f.Fail == failOffline || f.Rest == nil || !f.Rest.Until.After(time.Now()) {
			t.Fatalf("a's own failure didn't rest it: %+v", f)
		}
		if n := resting(); n != 1 {
			t.Fatalf("%d resting, want a alone", n)
		}
	})

	t.Run("answered lately", func(t *testing.T) {
		s := setup(t, "http://"+deadB+"/v1", true)
		heardNet()
		_, _, r := ask(t, s)
		if f := r.Tries[0]; f.Fail == failOffline || f.Rest == nil || !f.Rest.Until.After(time.Now()) {
			t.Fatalf("a's failure after a reply came didn't rest it: %+v", f)
		}
	})
}

// forgetNetwork forgets what was heard of the network and the verdict kept.
func forgetNetwork() {
	lastHeard.Store(0)
	offlineSeen.Lock()
	offlineSeen.at, offlineSeen.off = time.Time{}, false
	offlineSeen.Unlock()
}

// A provider connected to addresses of the user's choosing (Resolve) is
// dialled by its name here, which a managed network that works may not
// resolve: its host says nothing of the network, as a proxied one's.
func TestOtherHostsSkipResolved(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "mine", Name: "Mine", Key: "k", Models: []string{"m"}, Chat: "https://own.example.com/v1"},
		{ID: "gw", Name: "Gw", Key: "k", Models: []string{"m"}, Chat: "https://api.corp.example.com/v1", Resolve: map[string][]string{"api.corp.example.com": {"10.0.0.1"}}},
		{ID: "open", Name: "Open", Key: "k", Models: []string{"m"}, Chat: "https://api.open.example.com/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	wasNear := nearHost
	t.Cleanup(func() { nearHost = wasNear })
	nearHost = func(string) bool { return false }
	me, _ := provider.Find("mine")
	got := otherHosts(*me)
	if len(got) != 1 || got[0] != "api.open.example.com:443" {
		t.Errorf("otherHosts = %v, want only the provider with no rule", got)
	}
}
