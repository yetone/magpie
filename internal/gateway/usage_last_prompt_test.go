package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A reply that more than one upstream request answered tells its client
// the last request's prompt (input, cache writes, cache reads): Claude
// Code reads the last reply's three as how full its conversation is, and
// each later request re-sent what the earlier one had. Summed, a reply
// after a ToolSearch read 1,154,857 cached tokens for a ~577k
// conversation and Claude Code compacted early (sleepy_shibe on
// Discord). Each request is still billed (Deferred, billed).

// anthUsageOf is what an Anthropic stream's client takes as the reply's
// usage: message_start's, each message_delta's counts laid over it.
func anthUsageOf(t *testing.T, raw string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, l := range strings.Split(raw, "\n") {
		data, ok := strings.CutPrefix(l, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage map[string]any `json:"usage"`
			} `json:"message"`
			Usage map[string]any `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		u := ev.Usage
		if ev.Type == "message_start" {
			u = ev.Message.Usage
		}
		for k, v := range u {
			if n, ok := v.(float64); ok {
				out[k] = n
			}
		}
	}
	return out
}

// relaySegment is what an Anthropic client is told of one reply's
// events, and the usage its call is billed for.
func relaySegment(t *testing.T, seg chan Event) (string, Usage) {
	t.Helper()
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	var u Usage
	go func() {
		defer close(done)
		relay(rec, httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", &Request{Model: "claude-opus-5-5", Stream: true}, seg, &u,
			func() {}, nil, func(string, string, bool) {})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reply never ended")
	}
	return rec.Body.String(), u
}

func wantPrompt(t *testing.T, got map[string]float64, input, write, read float64) {
	t.Helper()
	if got["input_tokens"] != input || got["cache_creation_input_tokens"] != write || got["cache_read_input_tokens"] != read {
		t.Errorf("client told input %v, cache writes %v, cache reads %v; want the last request's %v, %v, %v",
			got["input_tokens"], got["cache_creation_input_tokens"], got["cache_read_input_tokens"], input, write, read)
	}
}

func wantBilled(t *testing.T, u Usage, input, write, read int) {
	t.Helper()
	if b := u.billed(); b.Input != input || b.CacheWrite != write || b.CacheRead != read {
		t.Errorf("billed input %d, cache writes %d, cache reads %d; want every request's: %d, %d, %d", b.Input, b.CacheWrite, b.CacheRead, input, write, read)
	}
}

// The reporter's numbers, in the bridge: the message calling Claude
// Code's own ToolSearch goes on into the next, which answers, and the
// client hears one reply.
func TestClaudeOwnCallReplyTellsLastPrompt(t *testing.T) {
	for _, c := range []struct {
		name                        string
		second                      string
		input, write, read          float64
		billIn, billWrite, billRead int
	}{
		{"reporter", `"input_tokens":6,"cache_creation_input_tokens":576,"cache_read_input_tokens":577848`, 6, 576, 577848, 8, 829, 1154857},
		// a second request that missed the cache: what the first read is
		// not told as this one's, nor billed twice
		{"cache missed", `"input_tokens":577854,"cache_creation_input_tokens":0,"cache_read_input_tokens":0`, 577854, 0, 0, 577856, 253, 577009},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := []string{
				se(`{"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":253,"cache_read_input_tokens":577009,"output_tokens":1}}}`),
				se(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"ToolSearch"}}`),
				se(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":2,"cache_creation_input_tokens":253,"cache_read_input_tokens":577009,"output_tokens":221}}`),
				se(`{"type":"message_stop"}`),
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"loaded"}]}}`,
				se(`{"type":"message_start","message":{"id":"m2","model":"claude-opus-5-5","usage":{` + c.second + `,"output_tokens":1}}}`),
				se(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				se(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stopped"}}`),
				se(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + c.second + `,"output_tokens":124}}`),
				se(`{"type":"message_stop"}`),
			}
			run := &subscriptionRun{}
			seg := run.attach()
			go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			raw, u := relaySegment(t, seg)
			got := anthUsageOf(t, raw)
			wantPrompt(t, got, c.input, c.write, c.read)
			if got["output_tokens"] != 345 {
				t.Errorf("output %v, want both messages' 221+124, as both were streamed", got["output_tokens"])
			}
			wantBilled(t, u, c.billIn, c.billWrite, c.billRead)
			if u.billed().Output != 345 {
				t.Errorf("billed output %d, want 345", u.billed().Output)
			}
		})
	}
}

// The interactive mode's message written after its call was told: its
// usage goes with the next reply, billed, but the next reply's prompt is
// its own.
func TestClaudeTUILateMessagePromptNotTold(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 {
		t.Fatalf("first reply: %s", raw)
	}
	seg := c.answer("toolu_a")
	row := func(id, block, stop, u string) string {
		return `{"type":"assistant","message":{"id":"` + id + `","model":"claude-opus-5-5","role":"assistant","content":[` + block + `],"stop_reason":"` + stop + `","usage":{` + u + `}}}`
	}
	c.write(
		row("msg_late", tuiCall("toolu_a", 1), "tool_use", `"input_tokens":2,"cache_creation_input_tokens":253,"cache_read_input_tokens":577009,"output_tokens":221`),
		row("msg_ans", `{"type":"text","text":"done"}`, "end_turn", `"input_tokens":6,"cache_creation_input_tokens":576,"cache_read_input_tokens":577848,"output_tokens":124`),
		tuiDone,
	)
	raw, u := relaySegment(t, seg)
	got := anthUsageOf(t, raw)
	wantPrompt(t, got, 6, 576, 577848)
	if got["output_tokens"] != 345 {
		t.Errorf("output %v, want the late message's 221 and the answer's 124", got["output_tokens"])
	}
	wantBilled(t, u, 8, 829, 1154857)
}

// The translated route's tries (#5115): Gemini answering with only its
// reasoning, asked again three times, and a cut reply's continuation.
func TestRetriedReplyTellsLastPrompt(t *testing.T) {
	gem := func(text string, thought bool) string {
		return fmt.Sprintf(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":%q,"thought":%t}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":500000,"cachedContentTokenCount":380000,"candidatesTokenCount":10}}`+"\n\n", text, thought)
	}
	for _, a := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"gem/gemini-3.8-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", "/v1/messages", `{"model":"gem/gemini-3.8-flash","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(a.name, func(t *testing.T) {
			fresh(t)
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				if calls <= 3 {
					io.WriteString(w, gem("Let me see.", true))
					return
				}
				io.WriteString(w, gem("hello", false))
			}))
			t.Cleanup(up.Close)
			saveGem(t, up.URL, "gemini-3.8-flash")
			code, body := post(t, a.path, a.body)
			if code != 200 || calls != 4 || !strings.Contains(body, "hello") {
				t.Fatalf("not asked past three empty replies: %d calls, %d %s", calls, code, body)
			}
			if a.name == "chat" {
				if u := chatUsageOf(body); u["prompt_tokens"] != 500000.0 {
					t.Errorf("client prompt_tokens %v, want the last try's 500000, not the four tries' sum", u["prompt_tokens"])
				}
			} else {
				wantPrompt(t, anthUsageOf(t, body), 120000, 0, 380000)
			}
			records := usage.Load(time.Time{})
			if len(records) != 1 || records[0].Input != 4*120000 || records[0].CacheRead != 4*380000 {
				t.Errorf("ledger %+v, want each of the four tries billed", records)
			}
		})
	}
}

// A later try or round whose prompt reads nothing from the cache tells its
// own zero, not the earlier request's reads, which are billed once.
func TestLaterTryZeroCacheReadNotTold(t *testing.T) {
	start := func(input, read int) string {
		return fmt.Sprintf(`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":0}}}`, input, read)
	}
	t.Run("continuation", func(t *testing.T) {
		f := &cutAnthropic{
			cuts:  map[int][]string{0: {start(7, 100), anthText("hel")}},
			whole: []string{start(11, 0), anthText("lo"), anthStop(4)},
		}
		body := continuationChatServer(t, "m", f)
		if f.calls() != 2 {
			t.Fatalf("calls = %d, want 2", f.calls())
		}
		u := chatUsageOf(body)
		details, _ := u["prompt_tokens_details"].(map[string]any)
		if u["prompt_tokens"] != 11.0 || details["cached_tokens"] != 0.0 {
			t.Errorf("client told %v, want the continuation's prompt 11 with nothing cached", u)
		}
		if r := usage.Load(time.Time{}); len(r) != 1 || r[0].Input != 18 || r[0].CacheRead != 100 {
			t.Errorf("ledger %+v, want input 7+11, the cut try's 100 reads once", r)
		}
	})
	t.Run("search", func(t *testing.T) {
		fresh(t)
		apis := newSearchAPIs(t)
		if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
			t.Fatal(err)
		}
		calls := 0
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.ReadAll(r.Body)
			calls++
			if calls == 1 {
				writeStream(w, []string{
					start(5, 100),
					`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"web_search","input":{}}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"latest go\"}"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
					`{"type":"message_stop"}`,
				}, false, "")
				return
			}
			writeStream(w, []string{start(9, 0), anthText("Go 1.27.1 is out."), anthStop(4), `{"type":"message_stop"}`}, false, "")
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"m"}, Anthropic: up.URL}); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"up/m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"What is the latest Go?"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`))
		r.Header.Set("x-api-key", Token)
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		if w.Code != 200 || calls != 2 {
			t.Fatalf("search reply = %d after %d rounds: %s", w.Code, calls, w.Body)
		}
		wantPrompt(t, anthUsageOf(t, w.Body.String()), 9, 0, 0)
		if r := usage.Load(time.Time{}); len(r) != 1 || r[0].Input != 14 || r[0].CacheRead != 100 {
			t.Errorf("ledger %+v, want input 5+9, the first round's 100 reads once", r)
		}
	})
}
