package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestProxyDownDoesNotRest (#381): a member whose proxy isn't listening
// (127.0.0.1:1082 down, "proxyconnect tcp: … connection refused") never
// reached its vendor. The next member answers, and the first isn't set
// aside: it is asked first again as soon as the proxy is back, not after a
// backoff that grew with every request made while it was down.
func TestProxyDownDoesNotRest(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			fresh(t)
			for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
				t.Setenv(k, "")
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			down := l.Addr().String()
			l.Close() // nothing listens there now
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from b"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			}))
			t.Cleanup(up.Close)
			for _, p := range []provider.Provider{
				{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: scheme + "://" + down},
				{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: up.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := 0; i < 2; i++ {
				code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, "from b") {
					t.Fatalf("request %d: %d %s", i, code, body)
				}
				r := s.trace.routes[len(s.trace.routes)-1]
				if len(r.Tries) != 2 || r.Tries[0].ID != r.Order[0].ID || r.Order[0].Provider != "a" {
					t.Fatalf("request %d: a wasn't asked first: %+v", i, r.Tries)
				}
				if f := r.Tries[0]; f.Fail != "proxy" || f.Rest != nil || !strings.Contains(f.Error, down) {
					t.Fatalf("request %d: a's try %+v", i, f)
				}
			}
			restingUntil.Lock()
			n := len(restingUntil.m)
			restingUntil.Unlock()
			if n != 0 {
				t.Fatalf("%d set aside over a proxy that was down", n)
			}
		})
	}
}
