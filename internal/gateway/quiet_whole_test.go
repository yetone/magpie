package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A group's first member, its stream held while its vendor said nothing,
// had the agent kept alive (keepAlive: the stream's 200 and comments);
// then the vendor, ignoring stream:true, answered 200 with its reply whole.
// The reply is no failure, so it was released as one would be: as the
// stream's error, "OK: {…the reply…}", a billed answer the agent never got.
// It now goes to the agent as the stream it asked for, every protocol a
// keepalive goes to (Gemini gets none).
func TestQuietWholeReplyStreamed(t *testing.T) {
	const (
		think = "The user wants the weather."
		text  = "Checking."
		args  = `{"city":"Paris"}`
	)
	tools := map[provider.Protocol]string{
		provider.Chat:      `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`,
		provider.Responses: `"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]`,
		provider.Anthropic: `"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]`,
	}
	for _, c := range []struct {
		name, ctype, whole string
		proto              provider.Protocol
		said               string // what the agent's stream says, decoded
		bAsked             int
	}{
		{"chat", "application/json", `{"id":"chatcmpl-9f2","object":"chat.completion","created":1791370000,"model":"m","system_fingerprint":"fp_1","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `","reasoning_content":"` + think + `","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"logprobs":null,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`,
			provider.Chat, "think:" + think + "|text:" + text + "|tool:get_weather " + args + "|stop:tool", 0},
		// a reply that isn't streamed, whatever its type says
		{"chat as text/plain", "text/plain; charset=utf-8", `{"id":"chatcmpl-9f3","object":"chat.completion","created":1791370000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}`,
			provider.Chat, "text:" + text + "|stop:stop", 0},
		{"responses", "application/json", `{"id":"resp_1","object":"response","created_at":1791370000,"status":"completed","model":"m","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"` + think + `"}],"encrypted_content":"gAAAAB"},{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]},{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],"parallel_tool_calls":true,"tool_choice":"auto","tools":[],"usage":{"input_tokens":12,"output_tokens":9,"total_tokens":21,"output_tokens_details":{"reasoning_tokens":5}}}`,
			provider.Responses, "think:" + think + "|text:" + text + "|tool:get_weather " + args + "|stop:tool", 0},
		{"anthropic", "application/json", `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"thinking","thinking":"` + think + `","signature":"EqQBCkYIBxgC"},{"type":"text","text":"` + text + `"},{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":9}}`,
			provider.Anthropic, "think:" + think + "|sig:EqQBCkYIBxgC|text:" + text + "|tool:get_weather " + args + "|stop:tool", 0},
		// a 200 that is no reply of the protocol's failed, as a translated
		// reply that didn't stream does: the next member answers
		{"not a reply", "application/json", `{"status":"queued","task":"t1"}`, provider.Chat, "text:from b|stop:stop", 1},
		// a stream under a type that doesn't say so goes through as it
		// comes, as it did before whole replies were held for the agent
		{"a stream as octet-stream", "application/octet-stream", chatChunk(`{"role":"assistant","content":"from a"}`, "null") + chatChunk(`{}`, `"stop"`) + "data: [DONE]\n\n", provider.Chat, "text:from a|stop:stop", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			quietFast(t)
			heard := make(chan struct{})
			var timedOut atomic.Bool
			a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				select {
				case <-heard: // the agent was kept alive meanwhile
				case <-time.After(3 * time.Second):
					timedOut.Store(true)
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", c.ctype)
				io.WriteString(w, c.whole)
			}))
			t.Cleanup(a.Close)
			b := &scripted{replies: []reply{{200, "text/event-stream", chatChunk(`{"role":"assistant","content":"from b"}`, "null") + chatChunk(`{}`, `"stop"`) + "data: [DONE]\n\n"}}}
			upB := httptest.NewServer(b)
			t.Cleanup(upB.Close)
			for id, u := range map[string]string{"a": a.URL, "b": upB.URL} {
				p := provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m"}}
				switch c.proto {
				case provider.Chat:
					p.Chat = u + "/v1"
				case provider.Responses:
					p.Responses = u + "/v1"
				case provider.Anthropic:
					p.Anthropic = u
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			refusalGroup(t, "a/m", "b/m")
			ask := map[provider.Protocol]struct{ path, body string }{
				provider.Chat:      {"/v1/chat/completions", `{"model":"G","stream":true,"messages":[{"role":"user","content":"weather in Paris?"}],` + tools[provider.Chat] + `}`},
				provider.Responses: {"/v1/responses", `{"model":"G","stream":true,"input":"weather in Paris?",` + tools[provider.Responses] + `}`},
				provider.Anthropic: {"/v1/messages", `{"model":"G","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"weather in Paris?"}],` + tools[provider.Anthropic] + `}`},
			}[c.proto]
			gw := httptest.NewServer(New().Handler())
			t.Cleanup(gw.Close)
			resp, err := http.Post(gw.URL+ask.path, "application/json", strings.NewReader(ask.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var once sync.Once
			var body strings.Builder
			rd := bufio.NewReader(resp.Body)
			for {
				line, err := rd.ReadString('\n')
				body.WriteString(line)
				if strings.HasPrefix(line, ": keepalive") {
					once.Do(func() { close(heard) })
				}
				if err != nil {
					break
				}
			}
			got := body.String()
			if timedOut.Load() || resp.StatusCode != 200 || !strings.HasPrefix(got, ": keepalive") || b.n != c.bAsked {
				t.Fatalf("timed out %v, b asked %d times: %d %q", timedOut.Load(), b.n, resp.StatusCode, got)
			}
			if said := saidIn(c.proto, got); said != c.said || strings.Contains(got, "OK: ") {
				t.Fatalf("the agent's stream says %q, want %q:\n%s", said, c.said, got)
			}
		})
	}
}

// saidIn reads a stream as its agent would, with the protocol's decoder:
// its reasoning, signatures, text, tool calls, stop and errors, in order.
func saidIn(proto provider.Protocol, stream string) string {
	dec := decoder(proto)
	var said []string
	var kind EventKind = -1
	add := func(k EventKind, s string) {
		if k == kind && k != KToolStart && len(said) > 0 {
			said[len(said)-1] += s
			return
		}
		kind = k
		said = append(said, s)
	}
	readSSE(strings.NewReader(stream), func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		return dec(data, func(ev Event) {
			switch ev.Kind {
			case KThink:
				add(KThink, "think:"+ev.Text)
			case KSig:
				add(KSig, "sig:"+ev.Text)
			case KText:
				add(KText, "text:"+ev.Text)
			case KToolStart:
				add(KToolStart, "tool:"+ev.Name+" ")
				kind = KToolArgs
			case KToolArgs:
				said[len(said)-1] += ev.Text
			case KStop:
				add(KStop, "stop:"+ev.Stop)
			case KError:
				add(KError, "error:"+ev.Text)
			}
		})
	})
	return strings.Join(said, "|")
}

func chatChunk(delta, finish string) string {
	return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
}

// The last member left answers a 200 that is no reply of the protocol's
// after an earlier member's stream had the agent kept alive: the agent is
// told it didn't stream, as the stream's error, and the request is logged
// as that failure, not as a 200 answered.
func TestQuietNotAReplyLastFails(t *testing.T) {
	fresh(t)
	quietFast(t)
	heard := make(chan struct{})
	quietOn(t, "a", quietCreated, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"overloaded\"}}\n\n", heard)
	b := &scripted{replies: []reply{{200, "application/json", `{"status":"queued","task":"t1"}`}}}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	// on the agent's own protocol, its reply relayed as it is
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"gpt-test"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	refusalGroup(t, "a/gpt-test", "b/gpt-test")
	s := New()
	code, body, _ := askChat(t, s, "G", heard)
	if code != 200 || !strings.HasPrefix(body, ": keepalive") || b.n == 0 || strings.Contains(body, "OK: ") {
		t.Fatalf("%d %q (b %d)", code, body, b.n)
	}
	if said := saidIn(provider.Chat, body); !strings.HasPrefix(said, "error:b did not stream: ") {
		t.Fatalf("the agent's stream says %q:\n%s", said, body)
	}
	if calls := s.Recent(); len(calls) == 0 || calls[0].Status != http.StatusBadGateway || !strings.Contains(calls[0].Error, "did not stream") {
		t.Fatalf("logged as %+v", calls)
	}
}

// What a client reads off a stream that it can't off the whole reply
// itself: Anthropic's SDKs take a stop's details from message_delta, null
// or not, over message_start's; and a Responses item with no id can be
// named by no event of its own, which openai-node refuses.
func TestWholeAsStreamClientReads(t *testing.T) {
	rec := httptest.NewRecorder()
	if !wholeAsStream(rec, provider.Anthropic, []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"Partial."}],"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"x"},"usage":{"input_tokens":3,"output_tokens":2}}`)) {
		t.Fatal("not streamed")
	}
	var delta map[string]any
	for _, ev := range events(rec.Body.String()) {
		if ev["type"] == "message_delta" {
			delta, _ = ev["delta"].(map[string]any)
		}
	}
	if d, _ := delta["stop_details"].(map[string]any); d["category"] != "cyber" {
		t.Fatalf("message_delta: %v", delta)
	}
	rec = httptest.NewRecorder()
	if !wholeAsStream(rec, provider.Responses, []byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}","status":"completed"}]}`)) {
		t.Fatal("not streamed")
	}
	if got := rec.Body.String(); strings.Contains(got, `"item_id":null`) || !strings.Contains(got, `"arguments":"{\"a\":1}"`) || saidIn(provider.Responses, got) != "tool:f {\"a\":1}|stop:tool" {
		t.Fatalf("%s\nsays %q", got, saidIn(provider.Responses, got))
	}
}

// A Responses text's citations go as their own events too, before its
// text is done: ai-sdk (opencode) makes its sources of these alone.
func TestWholeAsStreamResponsesCitations(t *testing.T) {
	rec := httptest.NewRecorder()
	cite := `{"type":"url_citation","start_index":0,"end_index":4,"url":"https://example.com/w","title":"W"}`
	if !wholeAsStream(rec, provider.Responses, []byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Sun.","annotations":[`+cite+`]}]}]}`)) {
		t.Fatal("not streamed")
	}
	var types []string
	var added map[string]any
	for _, ev := range events(rec.Body.String()) {
		types = append(types, ev["type"].(string))
		if ev["type"] == "response.output_text.annotation.added" {
			added = ev
		}
	}
	a, _ := added["annotation"].(map[string]any)
	if added["item_id"] != "msg_1" || added["annotation_index"] != float64(0) || a["url"] != "https://example.com/w" {
		t.Fatalf("annotation.added: %v", added)
	}
	if got := strings.Join(types, " "); !strings.Contains(got, "response.output_text.delta response.output_text.annotation.added response.output_text.done") {
		t.Fatalf("events: %s", got)
	}
}

// A held try's reply under a type that says neither JSON nor a stream,
// once the agent has a stream's headers: its first bytes decide, however
// they are split, and a stream goes through whole, the bytes held of its
// start too.
func TestHeldUnsureStreamPasses(t *testing.T) {
	for _, c := range []struct {
		name   string
		writes []string
		stream bool
	}{
		{"a stream split in its first word", []string{"\n da", "ta: {\"x\":1}\n\n"}, true},
		{"a comment", []string{": hi\n\n"}, true},
		{"an id first", []string{"id: 1\n", "data: {\"x\":1}\n\n"}, true},
		{"a retry first, split", []string{"ret", "ry: 1000\ndata: {}\n\n"}, true},
		{"a whole reply", []string{"{\"id\":", "1}"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h := newHoldWriter(rec, true)
			h.alive = &keptAlive{proto: provider.Chat, sent: true}
			h.Header().Set("Content-Type", "application/octet-stream")
			h.WriteHeader(http.StatusOK)
			all := strings.Join(c.writes, "")
			for _, w := range c.writes {
				if n, err := h.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("wrote %d of %q: %v", n, w, err)
				}
			}
			if c.stream && (!h.passing || h.whole || rec.Body.String() != all) {
				t.Fatalf("passing %v, whole %v, agent has %q", h.passing, h.whole, rec.Body.String())
			}
			if !c.stream && (h.passing || !h.whole || h.held.String() != all || rec.Body.Len() != 0) {
				t.Fatalf("passing %v, whole %v, held %q, agent has %q", h.passing, h.whole, h.held.String(), rec.Body.String())
			}
		})
	}
}

// A held try's reply under a type that says neither JSON nor a stream,
// its first bytes slow to come: the agent, sent nothing of it yet, is
// kept alive meanwhile, as it was before such a reply was held.
func TestHeldUnsureKeptAlive(t *testing.T) {
	quietFast(t)
	rec := httptest.NewRecorder()
	h := newHoldWriter(rec, true)
	h.alive = &keptAlive{proto: provider.Chat, sent: true}
	h.atLine = true
	h.Header().Set("Content-Type", "application/octet-stream")
	h.WriteHeader(http.StatusOK)
	time.Sleep(2 * keepaliveEvery)
	h.mu.Lock()
	h.keepQuiet()
	h.mu.Unlock()
	if got := rec.Body.String(); got != ": keepalive\n" {
		t.Fatalf("the agent has %q", got)
	}
	h.Write([]byte("data: {}\n\n"))
	if got := rec.Body.String(); got != ": keepalive\ndata: {}\n\n" {
		t.Fatalf("the agent has %q", got)
	}
}
