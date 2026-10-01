package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin account whose proxy is down, HTTP or SOCKS5, never reached its
// vendor: as for a built-in's (TestProxyDownDoesNotRest), the next account
// answers and the first isn't set aside.
func TestPluginProxyDownDoesNotRest(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			fresh(t)
			for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
				t.Setenv(k, "")
			}
			t.Setenv("MAGPIE_BUN", bun)
			t.Cleanup(plugin.Settle)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			down := l.Addr().String()
			l.Close()
			// the proxy b goes through, which answers as the vendor
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"},"finish_reason":null}]}`+"\n\n")
				fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer good.Close()
			t.Setenv("FAKE_BASE", "http://vendor.invalid/v1")

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
			if _, err := plugin.Add(ctx, abs); err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.Providers(ctx); err != nil {
				t.Fatal(err)
			}
			for _, team := range []string{"a", "b"} {
				st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
				if err != nil {
					t.Fatal(err)
				}
				if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := plugin.Providers(ctx); err != nil {
				t.Fatal(err)
			}
			if err := provider.SwitchLogin("fakeco", "a@fake"); err != nil {
				t.Fatal(err)
			}
			p, err := provider.Find("fakeco")
			if err != nil {
				t.Fatal(err)
			}
			p.AccountProxies = map[string]string{"a@fake": scheme + "://" + down, "b@fake": good.URL}
			if err := provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := 0; i < 2; i++ {
				code, body := postAs(t, s, "", `{"model":"fakeco/fake-1","messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, "from b") {
					t.Fatalf("request %d: %d %s", i, code, body)
				}
				r := s.trace.routes[len(s.trace.routes)-1]
				if len(r.Tries) != 2 {
					t.Fatalf("request %d: tries %+v", i, r.Tries)
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
