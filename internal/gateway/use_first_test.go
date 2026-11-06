package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// quotaKeyed answers from the key each request came with, or, once out,
// with OpenAI's 429 insufficient_quota.
type quotaKeyed struct {
	mu    sync.Mutex
	tried []string
	out   bool
}

func (k *quotaKeyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.tried = append(k.tried, key)
	out := k.out
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if out {
		w.WriteHeader(429)
		io.WriteString(w, carriedQuotaBody)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
}

// A provider set to be used first takes a model's group's requests ahead
// of the one listed before it, under Smart (hzh_1119 on Discord: an API
// key's monthly credit before the subscription), and once it answers 429
// insufficient_quota the request goes on to the other.
func TestUseFirstLeadsTheGroup(t *testing.T) {
	fresh(t)
	sub, api := &quotaKeyed{}, &quotaKeyed{}
	serveOn(t, "sub", "ksub", []string{"m"}, sub)
	serveOn(t, "api", "kapi", []string{"m"}, api)
	s := New()
	ask := func(n string) string {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"auto/m","messages":[{"role":"user","content":"`+n+`"}]}`)
		if code != 200 {
			t.Fatalf("%s: %d %s", n, code, body)
		}
		return body
	}

	if body := ask("listed order"); !strings.Contains(body, "from ksub") {
		t.Fatalf("none set: %s, want the first listed", body)
	}
	if err := provider.SetUseFirst("api", true); err != nil {
		t.Fatal(err)
	}
	if body := ask("use first"); !strings.Contains(body, "from kapi") {
		t.Fatalf("api set to be used first: %s", body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Order) != 2 || r.Order[0].Provider != "api" || !r.Order[0].UseFirst || r.Order[1].UseFirst {
		t.Fatalf("trace: %+v", r.Order)
	}

	api.mu.Lock()
	api.out = true
	api.mu.Unlock()
	if body := ask("out of credit"); !strings.Contains(body, "from ksub") {
		t.Fatalf("api out of quota: %s", body)
	}
	if got := strings.Join(api.tried, ","); got != "kapi,kapi" {
		t.Fatalf("api tried %s", got)
	}
}
