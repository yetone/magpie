package gateway

import (
	"cmp"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// legal451Bodies are what a 451 came with: nothing (the reporter's
// vendor not known, so no body is made up for it), a line of text, an
// error object with no code magpie reads, and StepFun's
// censorship_blocked (#5089), which was read before.
var legal451Bodies = []struct{ name, ctype, body string }{
	{"bare", "text/plain", ""},
	{"text", "text/plain", "Unavailable For Legal Reasons"},
	{"json", "application/json", `{"error":{"message":"The request was blocked.","type":"invalid_request_error"}}`},
	{"stepfun", "application/json", `{"error":{"type":"censorship_blocked","message":"The content you provided or machine outputted is blocked."}}`},
}

// legalOK is a member that answers every request, as a stream when asked
// for one.
type legalOK struct {
	mu sync.Mutex
	n  int
}

func (a *legalOK) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.n++
	a.mu.Unlock()
	if strings.Contains(string(b), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, chatStreamOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, chatOK)
}

func (a *legalOK) asked() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// legalOKOn saves provider id, served by up over Chat.
func legalOKOn(t *testing.T, id string, up http.Handler) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// The reporter's case (Winnie7572 on X): a group whose member answered
// 451 ended there, tries=1, the agent handed the 451 while five members
// stood ready, and the same account answered the request with a word
// changed. A 451 is the vendor refusing the request's content, whatever
// its body: the next member is asked, and the member that refused
// doesn't rest. Each agent protocol, whole and streamed, in-order and
// smart, the 451 said bare or in words.
func TestLegal451FailsOverAndRestsNobody(t *testing.T) {
	asks := []struct {
		name, path string
		proto      provider.Protocol
		body       string
	}{
		{"chat", "/v1/chat/completions", provider.Chat, `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`},
		{"chat-stream", "/v1/chat/completions", provider.Chat, `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages", "/v1/messages", provider.Anthropic, `{"model":"group/g","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages-stream", "/v1/messages", provider.Anthropic, `{"model":"group/g","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", provider.Responses, `{"model":"group/g","input":"hi"}`},
		{"responses-stream", "/v1/responses", provider.Responses, `{"model":"group/g","stream":true,"input":"hi"}`},
	}
	for _, routing := range []string{provider.Ordered, ""} {
		for _, ask := range asks {
			for _, said := range legal451Bodies {
				name := cmp.Or(routing, "smart") + "/" + ask.name + "/" + said.name
				t.Run(name, func(t *testing.T) {
					fresh(t)
					a := &scripted{replies: []reply{{http.StatusUnavailableForLegalReasons, said.ctype, said.body}}}
					scriptedOn(t, "a", ask.proto, a)
					b := &legalOK{}
					legalOKOn(t, "b", b)
					if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: routing}); err != nil {
						t.Fatal(err)
					}
					s := New()
					code, body := sendTo(s, ask.path, ask.body)
					if code != http.StatusOK || b.asked() != 1 || a.n != 1 {
						t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.asked())
					}
					if strings.Contains(body, "451") || strings.Contains(body, "blocked") {
						t.Fatalf("the agent was told the refusal: %s", body)
					}
					r := lastRoute(s)
					if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Status != http.StatusOK {
						t.Fatalf("route %d, tries %+v", r.Status, r.Tries)
					}
					if s.resting("a") {
						t.Fatal("a 451 set the member aside")
					}
				})
			}
		}
	}
}

// A 451 named in a stream's error event, the reply's 200 already sent
// by the vendor but nothing of it said, is the same refusal.
func TestLegal451StreamEventFailsOver(t *testing.T) {
	fresh(t)
	const event = `data: {"error":{"code":451,"message":"The request was blocked."}}` + "\n\n"
	a := &scripted{replies: []reply{{200, "text/event-stream", event}}}
	scriptedOn(t, "a", provider.Chat, a)
	b := &legalOK{}
	legalOKOn(t, "b", b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || !strings.Contains(body, "from b") || a.n != 1 || b.asked() != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.asked())
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}
	if s.resting("a") {
		t.Fatal("a 451 set the member aside")
	}
}

// The last member's 451 goes to the agent as a refusal, with the
// vendor's words and its status, and still rests nobody.
func TestLegal451LastMemberTold(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{http.StatusUnavailableForLegalReasons, "application/json", `{"error":{"message":"The request was blocked."}}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	b := &scripted{replies: []reply{{http.StatusUnavailableForLegalReasons, "", ""}}}
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != refusedStatus || a.n != 1 || b.n != 1 || !strings.Contains(body, "HTTP 451") || !strings.Contains(body, "Unavailable For Legal Reasons") {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	if s.resting("a") || s.resting("b") {
		t.Fatal("a 451 set a member aside")
	}
}

// A 451 that says the region refused is the region's (regionRefused):
// it still goes to the next member, and nobody rests, as before.
func TestLegal451OfARegionFailsOver(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{http.StatusUnavailableForLegalReasons, "application/json", `{"error":{"code":"unsupported_country_region_territory","message":"Country, region, or territory not supported"}}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	b := &legalOK{}
	legalOKOn(t, "b", b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || a.n != 1 || b.asked() != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.asked())
	}
	if s.resting("a") {
		t.Fatal("a region's 451 set the member aside")
	}
}

// What reads a 451 apart from a held try: System One's group loop
// (decideFailed and retryable) and a stream's late error frame
// (restAfter). Each fails over, and none rests.
func TestLegal451ReadEverywhere(t *testing.T) {
	fresh(t)
	s := New()
	p := provider.Provider{ID: "v", Name: "V", Key: "k"}
	c := candidate{p: p, model: "m", rest: "v"}
	for _, said := range legal451Bodies {
		if !retryable(http.StatusUnavailableForLegalReasons, []byte(said.body)) {
			t.Fatalf("%s: retryable(451) = false", said.name)
		}
		if r := s.restAfter(c, http.StatusUnavailableForLegalReasons, http.Header{}, []byte(said.body)); r.By != "off" || r.Why != failRefused {
			t.Fatalf("%s: restAfter = %+v", said.name, r)
		}
		if why, rest := s.decideFailed(c, http.StatusUnavailableForLegalReasons, http.Header{}, []byte(said.body)); rest != nil && rest.By != "off" {
			t.Fatalf("%s: decideFailed = %q, %+v", said.name, why, rest)
		}
		// a 451 named in a 200's stream error frame (failedNamed)
		if r := s.restAfter(c, http.StatusOK, http.Header{}, []byte(`{"error":{"code":451,"message":"blocked"}}`)); r.By != "off" {
			t.Fatalf("late frame: restAfter = %+v", r)
		}
	}
	if s.resting("v") {
		t.Fatal("a 451 set the member aside")
	}
}

// A plugin-served member's 451 (the plugin hands the vendor's status on)
// fails over the same.
func TestLegal451PluginMemberFailsOver(t *testing.T) {
	t.Setenv("FAKE_ZCODE", "1")
	var mu sync.Mutex
	asked := 0
	movedFake(t, "zcode", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		asked++
		mu.Unlock()
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	b := &legalOK{}
	legalOKOn(t, "b", b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"zcode/GLM-5.3", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	mu.Lock()
	n := asked
	mu.Unlock()
	if code != http.StatusOK || n != 1 || b.asked() != 1 {
		t.Fatalf("%d %s (zcode %d, b %d)", code, body, n, b.asked())
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}
}
