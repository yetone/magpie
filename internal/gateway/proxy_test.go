package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// seen records the requests a server was sent: through a proxy, the host
// asked for (the request's absolute URL), and the path.
type seen struct {
	mu   sync.Mutex
	name string
	got  []string
}

func (s *seen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	host := r.URL.Host // set only on a request sent to a proxy
	if host == "" {
		host = r.Host
	}
	s.mu.Lock()
	s.got = append(s.got, host+r.URL.Path)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/models") {
		io.WriteString(w, `{"object":"list","data":[{"id":"m","object":"model"}]}`)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+s.name+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}

func (s *seen) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *seen) reset() {
	s.mu.Lock()
	s.got = nil
	s.mu.Unlock()
}

// Each provider's requests go through its own proxy (#237): A's through the
// one it names, B's through none though a global one is set, C's, which
// follows the global one, through that — the gateway's turns and the model
// lists fetched for them alike.
func TestProviderProxy(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	own, global, direct := &seen{name: "own proxy"}, &seen{name: "global proxy"}, &seen{name: "vendor"}
	ownProxy, globalProxy, vendor := httptest.NewServer(own), httptest.NewServer(global), httptest.NewServer(direct)
	t.Cleanup(ownProxy.Close)
	t.Cleanup(globalProxy.Close)
	t.Cleanup(vendor.Close)

	// the vendors are at names no proxy stands in front of (loopback never
	// goes through one): dialled directly, every *.test is the vendor
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); strings.HasSuffix(host, ".test") {
			addr = vendor.Listener.Addr().String()
		}
		return d.DialContext(ctx, network, addr)
	}
	transport := func() *http.Transport {
		return &http.Transport{Proxy: netproxy.Func, DialContext: dial, ResponseHeaderTimeout: 10 * time.Second}
	}
	defaultTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = netproxy.Dispatch(transport())
	t.Cleanup(func() { http.DefaultClient.Transport = defaultTransport })

	st := settings.Load()
	st.Proxy = globalProxy.URL
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: ownProxy.URL},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: "http://b.test/v1", Proxy: "direct"},
		{ID: "c", Name: "C", Key: "kc", Models: []string{"m"}, Chat: "http://c.test/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(provider.Provider{ID: "d", Name: "D", Key: "kd", Chat: "http://d.test/v1", Proxy: "ftp://x"}); err == nil {
		t.Fatal("an ftp:// proxy was taken")
	}
	if pa, _ := provider.Find("a"); pa.Proxy != ownProxy.URL {
		t.Fatalf("a's proxy isn't kept: %q", pa.Proxy)
	}
	if pc, _ := provider.Find("c"); pc.Proxy != "" {
		t.Fatalf("c follows the global proxy, not %q", pc.Proxy)
	}

	s := New()
	s.client = &http.Client{Transport: netproxy.Dispatch(transport())}
	check := func(what string, got *seen, want string, others ...*seen) {
		t.Helper()
		if l := got.list(); len(l) != 1 || l[0] != want {
			t.Fatalf("%s: %v, want [%s]", what, l, want)
		}
		for _, o := range others {
			if l := o.list(); len(l) != 0 {
				t.Fatalf("%s: %s was sent %v", what, o.name, l)
			}
		}
		for _, x := range []*seen{own, global, direct} {
			x.reset()
		}
	}
	for _, c := range []struct {
		model string
		by    *seen
		want  string
		not   []*seen
	}{
		{"a/m", own, "a.test/v1/chat/completions", []*seen{global, direct}},
		{"b/m", direct, "b.test/v1/chat/completions", []*seen{own, global}},
		{"c/m", global, "c.test/v1/chat/completions", []*seen{own, direct}},
	} {
		code, body := postAs(t, s, "", `{"model":"`+c.model+`","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "from "+c.by.name) {
			t.Fatalf("%s: %d %s", c.model, code, body)
		}
		check(c.model, c.by, c.want, c.not...)
	}

	// the model lists fetched for them go the same ways
	ctx := context.Background()
	for _, c := range []struct {
		id   string
		by   *seen
		want string
		not  []*seen
	}{
		{"a", own, "a.test/v1/models", []*seen{global, direct}},
		{"b", direct, "b.test/v1/models", []*seen{own, global}},
		{"c", global, "c.test/v1/models", []*seen{own, direct}},
	} {
		p, err := provider.Find(c.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(ctx); err != nil {
			t.Fatalf("%s's models: %v", c.id, err)
		}
		check(c.id+"'s models", c.by, c.want, c.not...)
	}

	// back to following the global proxy, A's turns go through it
	pa, _ := provider.Find("a")
	pa.Proxy = ""
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	if code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from global proxy") {
		t.Fatalf("a following the global proxy: %d %s", code, body)
	}
	check("a following the global proxy", global, "a.test/v1/chat/completions", own, direct)
}
