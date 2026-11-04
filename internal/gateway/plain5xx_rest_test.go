package gateway

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// One request refused with the same 5xx, word for word, by two accounts
// of a provider that answers other requests is the request refused, not
// each account (mycf_711 on Discord: 127 of a provider's 128 503s were
// "AIError AI服务异常", one agent's request with an empty tool schema the
// upstream always refused; each walked every account into backoff, and
// the other agents' requests found them all resting). Neither rests, the
// last one left isn't asked again after a moment, and an account after
// them answers. One account's 503 alone still rests it, 503s in other
// words are the accounts' own, and a provider that answered nothing
// lately may be down: its alike 503s rest as before.
func TestPlain5xxOnTwoMatesRestsNeither(t *testing.T) {
	for _, x := range []struct {
		name    string
		spares  []string
		also    []string // accounts after acct-1 that refuse too
		differ  bool     // each account says it in its own words
		cold    bool     // the provider answered nothing before
		resting []string // who rests after
		code    int
		tried   string // the accounts asked, in order
	}{
		{"alone", []string{"b@example.com"}, nil, false, false, []string{"codex@me@example.com"}, 200, "acct-1,acct-2"},
		{"two alike, a third answers", []string{"b@example.com", "c@example.com"}, []string{"acct-2"}, false, false, nil, 200, "acct-1,acct-2,acct-3"},
		{"two alike, nobody left", []string{"b@example.com"}, []string{"acct-2"}, false, false, nil, 503, "acct-1,acct-2"},
		{"two in other words", []string{"b@example.com", "c@example.com"}, []string{"acct-2"}, true, false, []string{"codex@me@example.com", "codex@b@example.com"}, 200, "acct-1,acct-2,acct-3"},
		{"two alike, provider down", []string{"b@example.com", "c@example.com"}, []string{"acct-2"}, false, true, []string{"codex@me@example.com", "codex@b@example.com"}, 200, "acct-1,acct-2,acct-3"},
	} {
		t.Run(x.name, func(t *testing.T) {
			codexSignedIn(t, x.spares...)
			if err := provider.SetRouting("codex", provider.Ordered); err != nil {
				t.Fatal(err)
			}
			var armed atomic.Bool
			var n atomic.Int32
			unavailable := func(w http.ResponseWriter) {
				if !armed.Load() {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sse(
						`data: {"type":"response.created","response":{"id":"r0","model":"gpt-5.5"}}`,
						`data: {"type":"response.output_text.delta","delta":"pong"}`,
						`data: {"type":"response.completed","response":{"id":"r0","usage":{"input_tokens":7,"output_tokens":1}}}`))
					return
				}
				k := n.Add(1)
				msg := "AIError AI服务异常"
				if x.differ {
					msg = fmt.Sprintf("upstream %c failed", 'a'+rune(k))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				// a trace id of its own each time, as a vendor's has
				fmt.Fprintf(w, `{"error":{"message":"%s (trace_id: %032x)","type":"api_error"}}`, msg, 0xabc0+int(k))
			}
			var tried []string
			chatgptRefusing(t, &tried, unavailable, x.also...)
			s := New()
			if !x.cold {
				// another request answered: the provider is up
				if code, body := codexAskOn(s, `{"model":"gpt-5.5","stream":true,"input":"hello"}`); code != 200 {
					t.Fatalf("the first request: %d %s", code, body)
				}
			}
			tried = nil
			armed.Store(true)
			code, body := codexAskOn(s, `{"model":"gpt-5.5","stream":true,"input":"ping","tools":[{"type":"function","name":"x","parameters":{}}]}`)
			if code != x.code {
				t.Fatalf("status %d: %s; tried %v", code, body, tried)
			}
			if got := strings.Join(tried, ","); got != x.tried {
				t.Fatalf("tried %s, want %s", got, x.tried)
			}
			r := lastRoute(s)
			who := []string{"codex@me@example.com"}
			for _, sp := range x.spares {
				who = append(who, "codex@"+sp)
			}
			for _, w := range who {
				_, ok := restOf(w)
				if want := slices.Contains(x.resting, w); ok != want {
					t.Fatalf("%s resting %v, want %v; tried %v; tries %+v", w, ok, want, tried, r.Tries)
				}
				if !ok {
					routed.Lock()
					f := routed.failures[w]
					routed.Unlock()
					if f != 0 {
						t.Fatalf("%s still counts %d failures", w, f)
					}
				}
			}
			if x.resting == nil && (r.Tries[0].Rest != nil || r.Tries[1].Rest != nil) {
				t.Fatalf("tries still say they rest: %+v", r.Tries)
			}
		})
	}
}

// What two tries' 5xx are compared by: the words, ids left out, and the
// status; a body that says nothing is never alike, nor an overload, which
// rests the model a moment on its own path.
func TestPlainUnavailableWords(t *testing.T) {
	c := candidate{p: provider.Provider{ID: "relay"}, model: "m"}
	a := refusalWords(503, []byte(`{"error":{"message":"AIError AI服务异常 req 9f8e7d6c5b4a3f2e1d0c"}}`))
	b := refusalWords(503, []byte(`{"error":{"message":"AIError  AI服务异常 req 0a1b2c3d4e5f60718293"}}`))
	if a == "" || a != b {
		t.Fatalf("ids kept apart: %q vs %q", a, b)
	}
	if refusalWords(502, []byte(`{"error":{"message":"AIError AI服务异常"}}`)) == a {
		t.Fatal("a 502 is alike to a 503")
	}
	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{503, `{"error":{"message":"AIError AI服务异常"}}`, true},
		{500, `{"error":{"message":"Internal error"}}`, true},
		{502, `{"error":{"message":"bad upstream"}}`, true},
		{504, `{"error":{"message":"timed out"}}`, false},
		{503, ``, false},
		{503, `{"error":{"message":"The model is overloaded"}}`, false},
		{529, `{"error":{"message":"busy"}}`, false},
		{429, `{"error":{"message":"slow down"}}`, false},
	} {
		if got := plainUnavailable(c, x.status, []byte(x.body)); got != x.want {
			t.Errorf("%d %s: %v, want %v", x.status, x.body, got, x.want)
		}
	}
}
