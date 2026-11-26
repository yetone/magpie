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
	t.Cleanup(func() { nearHost, hasNetwork = wasNear, wasNet })
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

	// #7871: a reply that reaches another gateway in the process (an
	// earlier test's, finishing after it) says nothing of this one's
	// network
	t.Run("another gateway heard a reply", func(t *testing.T) {
		s := setup(t, "http://"+deadB+"/v1", true)
		(&Server{}).network.heardNet()
		_, _, r := ask(t, s)
		if f := r.Tries[0]; f.Fail != failOffline || f.Rest != nil {
			t.Fatalf("a's try %+v", f)
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
		s.network.heardNet()
		_, _, r := ask(t, s)
		if f := r.Tries[0]; f.Fail == failOffline || f.Rest == nil || !f.Rest.Until.After(time.Now()) {
			t.Fatalf("a's failure after a reply came didn't rest it: %+v", f)
		}
	})
}
