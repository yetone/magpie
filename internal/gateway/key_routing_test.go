package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// justforlxz on X: a gateway key can set how a routing group it asks for
// routes its requests. The group here is In order with the paid member
// listed first; a key set to Free first sends its requests to the free one
// first on Chat, Messages and Responses alike, while a key that sets none,
// and this computer's own request, keep the group's order. A Manual group
// still sends to its pick whatever the key says.
func TestKeyGroupRouting(t *testing.T) {
	fresh(t)
	vendor := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"ok\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ok","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	serveOn(t, "freev", "sk-free", []string{"m"}, vendor)
	serveOn(t, "paidv", "sk-paid", []string{"m"}, vendor)
	liveModels(t, "freev", `{"models":[{"id":"m","free":true}]}`)
	liveModels(t, "paidv", `{"models":[{"id":"other"},{"id":"m"}]}`)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"paidv/m", "freev/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Free", "Plain")
	if _, err := access.Update("routing-key", access.Change{Key: keys[0].ID, Routing: provider.FreeFirst}); err != nil {
		t.Fatal(err)
	}
	s := New()
	first := func(path, secret, body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
			req.RemoteAddr = "192.168.1.20:5000"
		} else {
			req.RemoteAddr = "127.0.0.1:5000"
		}
		n := len(s.trace.routes)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if len(s.trace.routes) == n {
			t.Fatalf("%s: nothing routed", path)
		}
		last := s.trace.routes[len(s.trace.routes)-1]
		if len(last.Order) == 0 {
			t.Fatalf("%s: no order: %+v", path, last)
		}
		return last.Order[0].Provider
	}
	asks := map[string]string{
		"/v1/chat/completions": `{"model":"group/g","messages":[{"role":"user","content":"x"}]}`,
		"/v1/messages":         `{"model":"group/g","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`,
		"/v1/responses":        `{"model":"group/g","input":"x"}`,
	}
	for path, body := range asks {
		if got := first(path, secrets[0], body); got != "freev" {
			t.Errorf("%s: the Free first key went to %s first, want freev", path, got)
		}
		if got := first(path, secrets[1], body); got != "paidv" {
			t.Errorf("%s: a key that sets none went to %s first, want paidv (the group's order)", path, got)
		}
		if got := first(path, "", body); got != "paidv" {
			t.Errorf("%s: this computer's own request went to %s first, want paidv", path, got)
		}
	}

	// Manual: the group's pick, whatever the key says
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"paidv/m", "freev/m"}, Routing: provider.Manual, Pick: "paidv/m"}); err != nil {
		t.Fatal(err)
	}
	if got := first("/v1/chat/completions", secrets[0], asks["/v1/chat/completions"]); got != "paidv" {
		t.Errorf("a Manual group went to %s for the Free first key, want its pick paidv", got)
	}
}

// keyRouted's own cases: unset follows, smart is the group's default (""),
// a Manual group keeps Manual.
func TestKeyRoutedCases(t *testing.T) {
	for _, c := range []struct{ key, group, want string }{
		{"", provider.Ordered, provider.Ordered},
		{"smart", provider.Ordered, ""},
		{provider.Rotate, "", provider.Rotate},
		{provider.Rotate, provider.Manual, provider.Manual},
		{"acme/sticky", provider.LeastUsed, "acme/sticky"},
	} {
		if got := keyRouted(access.Identity{Routing: c.key}, provider.Group{Routing: c.group}).Routing; got != c.want {
			t.Errorf("key %q on a group %q: %q, want %q", c.key, c.group, got, c.want)
		}
	}
}
