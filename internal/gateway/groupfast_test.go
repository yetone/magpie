package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fastUp is every vendor at once: the gateway's calls to api.openai.com,
// api.anthropic.com or a relay all come here, and it keeps the last.
type fastUp struct {
	mu                    sync.Mutex
	host, path, beta, raw string
}

func (u *fastUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.path, u.beta, u.raw = r.URL.Path, r.Header.Get("anthropic-beta"), string(b)
	u.mu.Unlock()
	if strings.Contains(string(b), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			io.WriteString(w, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","model":"x","usage":{"input_tokens":1}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			io.WriteString(w, sse(
				`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"x","status":"in_progress","output":[]}}`,
				`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","item_id":"o1","output_index":0,"content_index":0,"delta":"ok"}`,
				`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"x","status":"completed","output":[{"type":"message","id":"o1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
		default:
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages"):
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		io.WriteString(w, `{"id":"r1","object":"response","status":"completed","model":"x","output":[{"type":"message","id":"o1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	default:
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}
}

func (u *fastUp) last(t *testing.T) (host, path, beta string, body map[string]any) {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := json.Unmarshal([]byte(u.raw), &body); err != nil {
		t.Fatalf("%v: %q", err, u.raw)
	}
	return u.host, u.path, u.beta, body
}

// fasted is a gateway whose OpenAI key (oa), Anthropic key (an) and relay
// (rl) all answer from one fake vendor, which sees the host each call was
// for.
func fasted(t *testing.T) (*Server, *fastUp) {
	t.Helper()
	fresh(t)
	up := &fastUp{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	for _, p := range []provider.Provider{
		{ID: "oa", Name: "OpenAI", Key: "ko", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6.1-sol", "gpt-6.1-mini"}},
		{ID: "an", Name: "Anthropic", Key: "ka", Anthropic: "https://api.anthropic.com", Models: []string{"claude-opus-5-5", "claude-sonnet-5"}},
		{ID: "rl", Name: "Relay", Key: "kr", Chat: "https://relay.example/v1", Models: []string{"gpt-6.1-sol"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	s.client = &http.Client{Transport: qoderRoundTrip(func(r *http.Request) (*http.Response, error) {
		up.mu.Lock()
		up.host = r.URL.Host
		up.mu.Unlock()
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	return s, up
}

func saveFastGroup(t *testing.T, members, fast []string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{ID: "f", Name: "F", Members: members, Fast: fast, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func postProto(t *testing.T, s *Server, path, body string) Route {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if strings.HasSuffix(path, "/messages") {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	return lastRoute(s)
}

var fastAsks = []struct{ name, path, body string }{
	{"chat", "/v1/chat/completions", `{"model":"group/f","messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"group/f","input":"hi"}`},
	{"anthropic", "/v1/messages", `{"model":"group/f","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`},
}

// A group's member sent fast on an OpenAI key is asked for priority
// processing (service_tier "priority") at its own effort, whichever API
// the agent spoke — Chat, Responses or Anthropic's — and the trace says so.
func TestFastMemberOpenAI(t *testing.T) {
	s, up := fasted(t)
	if err := provider.SetModelEfforts("oa/gpt-6.1-sol", []string{"low", "medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	saveFastGroup(t, []string{"oa/gpt-6.1-sol:high:fast", "rl/gpt-6.1-sol"}, nil)
	g, _, _ := provider.FindGroup("group/f")
	if g.Members[0] != "oa/gpt-6.1-sol:high" || len(g.Fast) != 1 || g.Fast[0] != "oa/gpt-6.1-sol:high" {
		t.Fatalf("saved %v fast %v", g.Members, g.Fast)
	}
	for _, a := range fastAsks {
		r := postProto(t, s, a.path, a.body)
		host, path, _, body := up.last(t)
		if host != "api.openai.com" || body["service_tier"] != "priority" || body["model"] != "gpt-6.1-sol" {
			t.Fatalf("%s: sent to %s%s %v", a.name, host, path, body)
		}
		effort := body["reasoning_effort"]
		if rs, ok := body["reasoning"].(map[string]any); ok {
			effort = rs["effort"]
		}
		if effort != "high" {
			t.Errorf("%s: effort %v in %v", a.name, effort, body)
		}
		if len(r.Tries) != 1 || !r.Tries[0].Fast || r.Tries[0].Fixed != "high" || !r.Order[0].Fast {
			t.Fatalf("%s: traced %+v %+v", a.name, r.Tries, r.Order)
		}
		if r.Group == nil || len(r.Group.Fast) != 1 || r.Group.Fast[0] != "oa/gpt-6.1-sol:high" {
			t.Fatalf("%s: group %+v", a.name, r.Group)
		}
	}
}

// Claude's fast mode on an Anthropic key: speed "fast" and its beta, as
// Anthropic's API has the agent's request or one translated to it.
func TestFastMemberAnthropic(t *testing.T) {
	s, up := fasted(t)
	saveFastGroup(t, []string{"an/claude-opus-5-5"}, []string{"an/claude-opus-5-5"})
	for _, a := range fastAsks {
		r := postProto(t, s, a.path, a.body)
		host, path, beta, body := up.last(t)
		if host != "api.anthropic.com" || path != "/v1/messages" || body["speed"] != "fast" || !strings.Contains(beta, claudeFastBeta) {
			t.Fatalf("%s: sent to %s%s beta %q %v", a.name, host, path, beta, body)
		}
		if _, ok := body["service_tier"]; ok {
			t.Errorf("%s: OpenAI's tier sent to Anthropic: %v", a.name, body)
		}
		if !r.Tries[0].Fast {
			t.Errorf("%s: traced %+v", a.name, r.Tries)
		}
	}
}

// A member not sent fast, and one whose vendor has no fast mode magpie can
// ask for (a relay; Claude Sonnet), goes as the agent asked: no tier, no
// speed, no beta.
func TestFastMemberLeftAlone(t *testing.T) {
	s, up := fasted(t)
	check := func(what string) {
		t.Helper()
		for _, a := range fastAsks {
			r := postProto(t, s, a.path, a.body)
			_, _, beta, body := up.last(t)
			if _, ok := body["service_tier"]; ok {
				t.Errorf("%s %s: tier sent %v", what, a.name, body)
			}
			if _, ok := body["speed"]; ok || strings.Contains(beta, claudeFastBeta) {
				t.Errorf("%s %s: speed sent %v %q", what, a.name, body, beta)
			}
			if r.Tries[0].Fast {
				t.Errorf("%s %s: traced fast", what, a.name)
			}
		}
	}
	saveFastGroup(t, []string{"oa/gpt-6.1-sol", "an/claude-opus-5-5"}, nil)
	check("not fast")
	saveFastGroup(t, []string{"an/claude-opus-5-5"}, nil)
	check("opus not fast")
	// fast saved while it was OpenAI's, the provider's address since moved
	// to a relay: the member stays, sent as asked
	saveFastGroup(t, []string{"oa/gpt-6.1-sol:fast"}, nil)
	p, _ := provider.Find("oa")
	p.Chat, p.Responses = "https://relay.example/v1", ""
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	check("moved to a relay")
	for _, m := range []string{"rl/gpt-6.1-sol:fast", "an/claude-sonnet-5:fast"} {
		if err := provider.SaveGroup(provider.Group{ID: "g", Members: []string{m}}); err == nil || !strings.Contains(err.Error(), "no fast mode") {
			t.Errorf("%s saved: %v", m, err)
		}
	}
}

// The rewrite in each wire format, and only where the vendor has it.
func TestWithFastWire(t *testing.T) {
	for proto, want := range map[string]string{"chat": `"service_tier":"priority"`, "responses": `"service_tier":"priority"`, "anthropic": `"speed":"fast"`} {
		var p provider.Protocol
		switch proto {
		case "chat":
			p = provider.Chat
		case "responses":
			p = provider.Responses
		default:
			p = provider.Anthropic
		}
		if got := string(withFast(p, []byte(`{"model":"m"}`))); !strings.Contains(got, want) {
			t.Errorf("%s: %s", proto, got)
		}
	}
	r, err := parseAnthropic([]byte(`{"model":"m","max_tokens":10,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !r.Fast {
		t.Fatalf("speed not read: %v %+v", err, r)
	}
	for _, c := range []struct {
		host, model string
		want        bool
	}{
		{"api.anthropic.com", "claude-opus-5-5", true},
		{"api.anthropic.com", "claude-opus-4-8-20260101", true},
		{"api.anthropic.com", "claude-sonnet-5", false},
		{"relay.example", "claude-opus-5-5", false},
	} {
		b, err := build(provider.Anthropic, r, c.model, c.host, false)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(string(b), `"speed":"fast"`)
		if got != c.want {
			t.Errorf("%s %s: speed %v, want %v", c.host, c.model, got, c.want)
		}
	}
	for host, want := range map[string]bool{"api.openai.com": true, "chatgpt.com": true, "relay.example": false} {
		if got := strings.Contains(string(buildResponses(r, "gpt-6.1-sol", host, false)), `"service_tier":"priority"`); got != want {
			t.Errorf("responses %s: tier %v, want %v", host, got, want)
		}
	}
}
