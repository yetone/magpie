package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// stealHost is a host a vendor's redirect names: it records every request
// that reaches it.
type stealHost struct {
	*httptest.Server
	mu   sync.Mutex
	seen []http.Header
}

func newStealHost(t *testing.T) *stealHost {
	t.Helper()
	s := &stealHost{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Clone())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"stolen":true}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stealHost) reached() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.seen...)
}

// A vendor answering the native route with a redirect to another host
// has its 3xx passed back to the caller as it came, and the host it names
// gets nothing: not the x-api-key and Authorization of an Anthropic
// provider, the x-goog-api-key of a Gemini one, nor a provider's own
// header. A redirect on the vendor's own origin is followed, signed.
func TestNativeRedirectKeepsTheKey(t *testing.T) {
	fresh(t)
	steal := newStealHost(t)
	up := newVendorAPI(t)
	up.answer = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/away"):
			w.Header().Set("Location", steal.URL+"/steal")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return true
		case strings.HasSuffix(r.URL.Path, "/here"):
			w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/here")+"/landed")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return true
		}
		return false
	}
	hdr := map[string]string{"X-Relay-Secret": "relay-secret"}
	saveVendor(t, provider.Provider{ID: "claudeish", Name: "Claudeish", Anthropic: up.URL + "/anthropic", Key: "ak-secret", Models: []string{"c"}, Headers: hdr})
	saveVendor(t, provider.Provider{ID: "gem", Name: "Gem", Gemini: up.URL + "/v1beta", Key: "AIza-secret", Models: []string{"gemini-2.5-pro"}, Headers: hdr})
	h := nativeServe(t)
	for _, id := range []string{"claudeish", "gem"} {
		rec := nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+id+"/v1/away", "127.0.0.1:50000", `{"model":"m"}`, "Content-Type", "application/json")
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != steal.URL+"/steal" {
			t.Errorf("%s: %d %q %s, want the vendor's 307 as it came", id, rec.Code, rec.Header().Get("Location"), rec.Body)
		}
		before := len(up.asked())
		rec = nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+id+"/v1/here", "127.0.0.1:50000", `{"model":"m"}`, "Content-Type", "application/json")
		if rec.Code != 201 {
			t.Fatalf("%s: a same-origin redirect: %d %s", id, rec.Code, rec.Body)
		}
		got := up.asked()[before:]
		if len(got) != 2 || !strings.HasSuffix(got[1].uri, "/v1/landed") || got[1].body != `{"model":"m"}` || got[1].header.Get("X-Relay-Secret") != "relay-secret" {
			t.Fatalf("%s: a same-origin redirect reached %+v", id, got)
		}
		if id == "gem" && got[1].header.Get("X-Goog-Api-Key") != "AIza-secret" || id == "claudeish" && got[1].header.Get("X-Api-Key") != "ak-secret" {
			t.Errorf("%s: a same-origin redirect lost the key: %v", id, got[1].header)
		}
	}
	if seen := steal.reached(); len(seen) != 0 {
		t.Fatalf("the redirect's host was sent %d requests, the first with %v", len(seen), seen[0])
	}
}

// The same on a translated route: a vendor's redirect to another host
// is not followed, so a provider's own header and its key stay with it.
func TestChatRedirectKeepsTheKey(t *testing.T) {
	fresh(t)
	steal := newStealHost(t)
	up := newVendorAPI(t)
	up.answer = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		w.Header().Set("Location", steal.URL+"/v1/chat/completions")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk-secret", Models: []string{"m"}, Headers: map[string]string{"X-Relay-Secret": "relay-secret", "Api-Key": "vk-secret"}})
	h := nativeServe(t)
	rec := nativeDo(h, "POST", "http://127.0.0.1:3425/v1/chat/completions", "127.0.0.1:50000", `{"model":"vendor/m","messages":[{"role":"user","content":"hi"}]}`, "Content-Type", "application/json")
	if rec.Code == 200 {
		t.Errorf("the redirect's host answered: %s", rec.Body)
	}
	if len(up.asked()) == 0 {
		t.Fatal("the vendor was never asked")
	}
	if seen := steal.reached(); len(seen) != 0 {
		t.Fatalf("the redirect's host was sent %d requests, the first with %v", len(seen), seen[0])
	}
}
