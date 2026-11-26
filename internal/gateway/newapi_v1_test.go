package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// newAPISite answers as new-api routes: OpenAI's API under /v1, the key
// sk-good taken and any other refused 401, and its site's page, 200
// text/html, for any other path (its NoRoute serves the web app).
func newAPISite(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		stream := bytes.Contains(b, []byte(`"stream":true`))
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><html lang="zh"><head><meta charset="utf-8" /><title>New API</title></head><body><div id="root"></div></body></html>`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-good" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"无效的令牌","type":"new_api_error"}}`)
			return
		}
		var rp reply
		switch r.URL.Path {
		case "/v1/chat/completions":
			rp = goodReply(provider.Chat, stream)
		case "/v1/responses":
			rp = goodReply(provider.Responses, stream)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", rp.ctype)
		w.WriteHeader(rp.code)
		io.WriteString(w, rp.body)
	}))
	t.Cleanup(up.Close)
	return up, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// sarosaka1023 on X: a relay added at its root, without the /v1 its API
// is under. Each request met its site's page and failed 502. It is asked
// again under /v1, answered there, and the base is saved with it, so the
// next request goes there at once.
func TestRelayRootAskedUnderV1(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		for _, stream := range []bool{false, true} {
			name := string(proto)
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				up, asked := newAPISite(t)
				p := provider.Provider{ID: "relay", Name: "Relay", Key: "sk-good", Models: []string{"m"}}
				if proto == provider.Chat {
					p.Chat = up.URL
				} else {
					p.Responses = up.URL
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				s := New()
				rec := askOn(s, proto, "relay/m", stream)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from b") {
					t.Fatalf("%d %s (asked %v)", rec.Code, rec.Body, asked())
				}
				q, _ := provider.Find("relay")
				if q.Base(proto) != up.URL+"/v1" {
					t.Fatalf("base %q", q.Base(proto))
				}
				n := len(asked())
				rec = askOn(s, proto, "relay/m", stream)
				if rec.Code != 200 || len(asked()) != n+1 || !strings.HasPrefix(asked()[n], "/v1/") {
					t.Fatalf("again: %d %s (asked %v)", rec.Code, rec.Body, asked())
				}
			})
		}
	}
}

// A key the API under /v1 refuses moves nothing: its 401 is the answer,
// saying where the API is.
func TestRelayRootRefusedSaysV1(t *testing.T) {
	fresh(t)
	up, _ := newAPISite(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-bad", Models: []string{"m"}, Chat: up.URL}); err != nil {
		t.Fatal(err)
	}
	rec := askOn(New(), provider.Chat, "relay/m", false)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "无效的令牌") || !strings.Contains(rec.Body.String(), "set the Base URL to "+up.URL+"/v1") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if q, _ := provider.Find("relay"); q.Chat != up.URL {
		t.Fatalf("base moved to %q", q.Chat)
	}
}
