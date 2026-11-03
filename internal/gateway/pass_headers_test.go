package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// heardHeaders' sibling for a fake vendor: the headers of each request it
// was sent.
type sentTo struct {
	mu   sync.Mutex
	seen []http.Header
}

func (s *sentTo) last() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		return nil
	}
	return s.seen[len(s.seen)-1]
}

func (s *sentTo) answer(vendor provider.Protocol, status int) http.HandlerFunc {
	reply := vendorReply(vendor, status, "x")
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Clone())
		s.mu.Unlock()
		reply(w, r)
	}
}

// the agent's request, carrying a header to pass, one to pass that also
// has a fixed value, magpie's key in every place an agent puts it, and a
// header nobody lists
func passRequest(path, body string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("X-Tenant", "tenant-from-client")
	r.Header.Set("X-Route", "route-from-client")
	r.Header.Set("X-Trace-Token", "trace-secret-0123456789")
	r.Header.Set("X-Unlisted", "no")
	r.Header.Set("Authorization", "Bearer magpie-gateway-key")
	r.Header.Set("X-Api-Key", "magpie-gateway-key")
	return r
}

// 3jRzfF52Qx on X: a header the provider lists goes to it as the agent
// sent it, on every API (relayed, translated, count_tokens), standing over
// the provider's fixed Header of the same name; the fixed one goes when
// the agent sends none; magpie's key never goes.
func TestPassHeadersReachTheProvider(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		vendor           provider.Protocol
	}{
		{"messages relayed", "/v1/messages", `{"model":"fake/sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic},
		{"messages to chat", "/v1/messages", `{"model":"fake/sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"chat", "/v1/chat/completions", `{"model":"fake/sol","messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"chat stream", "/v1/chat/completions", `{"model":"fake/sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"responses to chat", "/v1/responses", `{"model":"fake/sol","input":"hi"}`, provider.Chat},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"fake/sol","messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &sentTo{}
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "sk-upstream-0123456789", Models: []string{"sol"},
				Headers:     map[string]string{"X-Route": "route-fixed", "X-Fixed": "fixed"},
				PassHeaders: []string{"x-tenant", "X-ROUTE", "x-trace-token", "x-missing"}}
			rhVendor(t, p, c.vendor, up.answer(c.vendor, 200))
			s := New()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, passRequest(c.path, c.body))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			h := up.last()
			if h == nil {
				t.Fatal("the vendor was sent nothing")
			}
			for k, want := range map[string]string{
				"X-Tenant":      "tenant-from-client",
				"X-Route":       "route-from-client", // the client's over the fixed one
				"X-Trace-Token": "trace-secret-0123456789",
				"X-Fixed":       "fixed",
			} {
				if got := h.Values(k); len(got) != 1 || got[0] != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			for _, k := range []string{"X-Unlisted", "X-Missing"} {
				if got := h.Get(k); got != "" {
					t.Errorf("%s = %q sent", k, got)
				}
			}
			if a := h.Get("Authorization") + h.Get("X-Api-Key"); strings.Contains(a, "magpie-gateway-key") || !strings.Contains(a, "sk-upstream-0123456789") {
				t.Errorf("the vendor's key headers %q, %q", h.Get("Authorization"), h.Get("X-Api-Key"))
			}
		})
	}
}

// A listed header, and its fixed value, go to the provider that lists it
// alone: a fallback that lists nothing is sent neither, and a group's
// member that lists it is sent it. What the call log shows of a passed
// header is masked as any header is.
func TestPassHeadersOnlyToTheirProvider(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	first, spare := &sentTo{}, &sentTo{}
	rhVendor(t, provider.Provider{ID: "ag", Name: "AG", Key: "k", Models: []string{"sol"}, Fallback: []string{"spare/sol"},
		PassHeaders: []string{"X-Tenant", "X-Trace-Token"}}, provider.Anthropic, first.answer(provider.Anthropic, 429))
	rhVendor(t, provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"sol"}},
		provider.Chat, spare.answer(provider.Chat, 200))
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, passRequest("/v1/messages", `{"model":"ag/sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := first.last().Get("X-Tenant"); got != "tenant-from-client" {
		t.Errorf("the listing provider was sent X-Tenant %q", got)
	}
	if h := spare.last(); h == nil || h.Get("X-Tenant") != "" || h.Get("X-Trace-Token") != "" {
		t.Errorf("the fallback that lists nothing was sent %v", h)
	}
	data, _ := json.Marshal(s.Recent()[0].Headers())
	if strings.Contains(string(data), "trace-secret-0123456789") {
		t.Errorf("a passed token header shown unmasked:\n%s", data)
	}
	if !strings.Contains(string(data), "tenant-from-client") {
		t.Errorf("the passed header isn't in the call's upstream headers:\n%s", data)
	}

	// a group: its first member lists nothing and is refused, the next
	// lists it
	fresh(t)
	noRests()
	other, member := &sentTo{}, &sentTo{}
	rhVendor(t, provider.Provider{ID: "oth", Name: "Oth", Key: "k", Models: []string{"sol"}},
		provider.Chat, other.answer(provider.Chat, 429))
	rhVendor(t, provider.Provider{ID: "mem", Name: "Mem", Key: "k", Models: []string{"sol"}, PassHeaders: []string{"X-Tenant"}},
		provider.Chat, member.answer(provider.Chat, 200))
	if err := provider.SaveGroup(provider.Group{Name: "Grp", Members: []string{"oth/sol", "mem/sol"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, passRequest("/v1/chat/completions", `{"model":"group/grp","messages":[{"role":"user","content":"hi"}]}`))
	if rec.Code != 200 {
		t.Fatalf("group: %d %s", rec.Code, rec.Body)
	}
	if h := other.last(); h == nil || h.Get("X-Tenant") != "" {
		t.Errorf("the member that lists nothing was sent %v", h)
	}
	if h := member.last(); h == nil || h.Get("X-Tenant") != "tenant-from-client" {
		t.Errorf("the member that lists it was sent %v", h)
	}
}
