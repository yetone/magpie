package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// paced is a vendor that streams its events a gap apart, the first at
// once, or fails with a status after the gap.
type paced struct {
	events []string
	gap    time.Duration
	fail   int
}

func (p *paced) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	if p.fail != 0 {
		time.Sleep(p.gap)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.fail)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for i, ev := range p.events {
		if i > 0 && strings.HasPrefix(ev, "~") { // a gap before it
			time.Sleep(p.gap)
			ev = ev[1:]
		}
		io.WriteString(w, ev+"\n\n")
		f.Flush()
	}
}

// Each: a lead, then reasoning a gap later, then text another gap later,
// then the end another gap later — 20 tokens written in all.
var (
	pacedAnthropic = []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"event: ping\ndata: " + `{"type":"ping"}`,
		"~event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}`,
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"~event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi"}}`,
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}`,
		"~event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
		"event: message_stop\ndata: " + `{"type":"message_stop"}`,
	}
	pacedChat = []string{
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`~data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`,
		`~data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`~data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":12}}}`,
		`data: [DONE]`,
	}
	pacedResponses = []string{
		"event: response.created\ndata: " + `{"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
		"~event: response.reasoning_summary_text.delta\ndata: " + `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"hmm"}`,
		"~event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"hi"}`,
		"~event: response.completed\ndata: " + `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":12}}}}`,
	}
)

const pacedGap = 80 * time.Millisecond

func lastUsage(t *testing.T) usage.Record {
	t.Helper()
	recs := usage.Load(time.Time{})
	if len(recs) == 0 {
		t.Fatal("no usage recorded")
	}
	return recs[len(recs)-1]
}

// A streamed reply's first content (reasoning here) and first text are
// timed from the request, whatever the vendor speaks and the agent asked
// in, and kept in the usage, the request's route and its try.
func TestUsageKeepsTimeToFirstToken(t *testing.T) {
	gap := pacedGap.Milliseconds()
	for _, c := range []struct {
		name   string
		proto  provider.Protocol
		events []string
		path   string
		body   string
	}{
		{"anthropic", provider.Anthropic, pacedAnthropic, "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		{"chat", provider.Chat, pacedChat, "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", provider.Responses, pacedResponses, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`},
		// translated: the agent's Chat stream from the vendor's Anthropic one
		{"chat from anthropic", provider.Anthropic, pacedAnthropic, "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		// and its Responses stream from the vendor's Chat one
		{"responses from chat", provider.Chat, pacedChat, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(&paced{events: c.events, gap: pacedGap})
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
			switch c.proto {
			case provider.Chat:
				p.Chat = up.URL + "/v1"
			case provider.Responses:
				p.Responses = up.URL + "/v1"
			case provider.Anthropic:
				p.Anthropic = up.URL
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			u := lastUsage(t)
			if u.TTFT < gap || u.FirstText < 2*gap || u.FirstText-u.TTFT < gap/2 || u.Millis < u.FirstText+gap/2 || u.Output != 20 {
				t.Errorf("usage ttft %d, first text %d, ms %d, out %d; want the reasoning after %d ms and the text after %d", u.TTFT, u.FirstText, u.Millis, u.Output, gap, 2*gap)
			}
			r := lastRoute(s)
			// the reply's reasoning, which its speed leaves out (usage.DecodeOf)
			if c.proto != provider.Anthropic && (u.Reasoning != 12 || r.Reasoning != 12) {
				t.Errorf("reasoning: usage %d, route %d; want 12", u.Reasoning, r.Reasoning)
			}
			if len(r.Tries) != 1 || r.TTFT != u.TTFT || r.FirstText != u.FirstText || r.Output != 20 ||
				r.Tries[0].TTFT < gap || r.Tries[0].TTFT > u.TTFT || r.Tries[0].FirstText < 2*gap {
				t.Errorf("route ttft %d/%d out %d, tries %+v", r.TTFT, r.FirstText, r.Output, r.Tries)
			}
			if calls := s.Recent(); len(calls) == 0 || calls[0].TTFT != u.TTFT {
				t.Errorf("request log: %+v", calls)
			}
		})
	}
}

// A reply that isn't streamed has no first token to time.
func TestUsageLeavesTTFTOfAWholeReply(t *testing.T) {
	fresh(t)
	serveOn(t, "a", "ka", []string{"m"}, &keyed{})
	s := New()
	if code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if u := lastUsage(t); u.TTFT != 0 || u.FirstText != 0 || u.Output != 5 {
		t.Errorf("usage %+v", u)
	}
	if r := lastRoute(s); r.TTFT != 0 || r.Tries[0].TTFT != 0 {
		t.Errorf("route %+v", r)
	}
}

// In a group that fails over, each try keeps its own time to its first
// token, from when it was sent, and the usage the request's, from when it
// came: the time the failed try took is in it, as it is in ms.
func TestGroupFailoverTimesEachTry(t *testing.T) {
	fresh(t)
	serveOn(t, "a", "ka", []string{"m"}, &paced{fail: 429, gap: pacedGap})
	serveOn(t, "b", "kb", []string{"m"}, &paced{events: pacedChat, gap: pacedGap})
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/mine","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"hi"`) {
		t.Fatalf("%d %s", code, body)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[1].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
	gap := pacedGap.Milliseconds()
	failed, answered := r.Tries[0], r.Tries[1]
	if failed.TTFT != 0 || failed.FirstText != 0 {
		t.Errorf("the failed try timed: %+v", failed)
	}
	if answered.TTFT < gap || answered.FirstText < 2*gap || answered.TTFT >= answered.FirstText {
		t.Errorf("the answering try: %+v", answered)
	}
	u := lastUsage(t)
	if u.Provider != "b" || u.TTFT < answered.TTFT+failed.Millis || u.FirstText < answered.FirstText+failed.Millis || u.Millis <= u.FirstText {
		t.Errorf("usage %+v; tries %+v", u, r.Tries)
	}
}

// What counts as a reply's first content and first text, in each protocol,
// its events split across writes as they come.
func TestFirstTokenKinds(t *testing.T) {
	for _, c := range []struct {
		ev            string
		content, text bool
	}{
		{`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"ls","input":{}}}`, true, false},
		{`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, false, false},
		{`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a"}}`, true, false},
		{`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"x"}}`, false, false},
		{`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, false, false},
		{`data: {"choices":[{"delta":{"role":"assistant","content":null}}]}`, false, false},
		{`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"ls"}}]}}]}`, true, false},
		{`data: {"choices":[{"delta":{"reasoning":"hm"}}]}`, true, false},
		{`data: {"choices":[{"delta":{"content":"hi"}}]}`, true, true},
		{"event: response.function_call_arguments.delta\ndata: {\"delta\":\"{\"}", true, false},
		{`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"ls"}}`, true, false},
		{`data: {"type":"response.output_item.added","item":{"type":"message"}}`, false, false},
		{`data: {"type":"response.output_text.delta","delta":""}`, false, false},
		{`data: {"candidates":[{"content":{"parts":[{"text":"hm","thought":true}]}}]}`, true, false},
		{`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"ls"}}]}}]}`, true, false},
		{`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`, true, true},
		{`: keep-alive`, false, false},
		{`data: [DONE]`, false, false},
	} {
		content, text := firstKind([]byte(c.ev))
		if content != c.content || text != c.text {
			t.Errorf("%s: content %v text %v", c.ev, content, text)
		}
	}

	// split across writes, \r\n-ended
	f := firstToken{start: time.Now().Add(-time.Second)}
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\r\n\r\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\r\n\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n"
	for i := 0; i < len(stream); i += 7 {
		f.see([]byte(stream[i:min(i+7, len(stream))]))
	}
	// read on past its first text, for when the rest came (flowMs)
	if first, text := f.ms(); first < 1000 || text < first || f.off || f.pend != nil || f.flowFor(0) < 1 {
		t.Errorf("split: %d %d %+v", first, text, f)
	}
	// a JSON reply is not read
	j := firstToken{start: time.Now()}
	j.see([]byte(`{"choices":[{"message":{"content":"hi"}}]}` + "\n\n"))
	if first, _ := j.ms(); first != 0 || !j.off {
		t.Errorf("json: %+v", j)
	}
}

// Codex's own models, relayed to the ChatGPT backend as they came, are
// timed too — its stream says no Content-Type.
func TestCodexBackendTimesFirstToken(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	p := &paced{events: pacedResponses, gap: pacedGap}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		p.ServeHTTP(w, r)
	})
	if code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"hi"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	gap := pacedGap.Milliseconds()
	if u := lastUsage(t); u.TTFT < gap || u.FirstText < 2*gap || u.Millis <= u.FirstText || u.Output != 20 {
		t.Errorf("usage %+v", u)
	}
}

// bursty is a vendor that writes each of its chunks a gap after the last,
// every event of a chunk in one write: a chunk of many is a reply it held
// back and let go at once.
type bursty struct {
	chunks [][]string
	gap    time.Duration
	header bool // no Content-Type, as the ChatGPT backend sends
}

func (b *bursty) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	if b.header {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	f := w.(http.Flusher)
	for i, c := range b.chunks {
		if i > 0 {
			time.Sleep(b.gap)
		}
		io.WriteString(w, strings.Join(c, "\n\n")+"\n\n")
		f.Flush()
	}
}

// burstChunks: a reply's first word at once, then its out tokens' worth
// of words n events in each later chunk, then its end with the usage.
func burstChunks(proto provider.Protocol, words, per, out int) [][]string {
	ev := func(word string) string {
		if proto == provider.Responses {
			return "event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"` + word + `"}`
		}
		return `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"` + word + `"}}]}`
	}
	chunks := [][]string{{ev("hi")}}
	for i := 1; i < words; i += per {
		var c []string
		for j := i; j < min(i+per, words); j++ {
			c = append(c, ev(" word"))
		}
		chunks = append(chunks, c)
	}
	end := []string{`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":` + strconv.Itoa(out) + `}}`, `data: [DONE]`}
	if proto == provider.Responses {
		end = []string{"event: response.completed\ndata: " + `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":` + strconv.Itoa(out) + `}}}`}
	}
	last := &chunks[len(chunks)-1]
	*last = append(*last, end...)
	return chunks
}

// A reply whose first word came at once and the rest only after a wait,
// all in one burst, wrote nothing over the wait, and tells no speed (John
// on Discord: a Kimi Code reply to OpenCode read 1,367 tok/s): before,
// its 5,000 tokens over the 600 ms from its first word to its end read
// about 8,300 tok/s. The burst is 5,000 tokens so that the time magpie
// takes to pass it on (138 ms on a busy macOS runner at 8db24fd6) reads
// faster than any real stream (MaxDecodeSpeed), as a real burst does;
// 1,000 read 7,246 tok/s. One that streamed steadily keeps its speed, over
// nearly all of its time from its first word.
func TestBurstTellsNoSpeed(t *testing.T) {
	const gap = 600 * time.Millisecond
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		path  string
		body  string
	}{
		{"chat", provider.Chat, "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses from chat", provider.Chat, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`},
	} {
		for _, steady := range []bool{false, true} {
			name := c.name + " burst"
			chunks, g := burstChunks(c.proto, 200, 199, 5000), gap
			if steady {
				name = c.name + " steady"
				chunks, g = burstChunks(c.proto, 11, 1, 200), gap/10
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(&bursty{chunks: chunks, gap: g})
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				s := New()
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "word") {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				u := lastUsage(t)
				span := u.Millis - u.TTFT
				// the upstream's wait starts when it sent the first word,
				// which magpie reads a moment later: the span is the gap
				// less that moment (599 ms of 600 on a busy macOS runner)
				if u.TTFT <= 0 || span < gap.Milliseconds()*9/10 {
					t.Fatalf("usage ttft %d, ms %d; want the end %v after the first word", u.TTFT, u.Millis, gap)
				}
				r := lastRoute(s)
				if len(r.Tries) != 1 || r.Flow != u.Flow || r.Tries[0].Flow != u.Flow {
					t.Errorf("flow: usage %d, route %d, tries %+v", u.Flow, r.Flow, r.Tries)
				}
				n, w := u.Decode()
				if !steady {
					if n != 0 || w != 0 {
						t.Errorf("burst: %d tokens in %d ms (flow %d, %d ms from the first word) read %d tok/s; want no speed", n, w, u.Flow, span, int64(n)*1000/max(w, 1))
					}
					return
				}
				if n != 200 || w < span*7/10 || w > span {
					t.Errorf("steady: %d tokens in %d ms (flow %d); want 200 over most of the %d ms from the first word", n, w, u.Flow, span)
				}
			})
		}
	}
}

// Codex's own models, relayed to the ChatGPT backend as they came, tell no
// speed for a burst either.
func TestCodexBackendBurstTellsNoSpeed(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	b := &bursty{chunks: burstChunks(provider.Responses, 200, 199, 5000), gap: 600 * time.Millisecond, header: true}
	chatgpt(t, b.ServeHTTP)
	if code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"hi"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	u := lastUsage(t)
	// the end after the wait, less the moment the first word took to
	// reach magpie (599 ms of 600 on macOS CI at 58f45d05)
	if u.Millis-u.TTFT < 540 || u.Output != 5000 {
		t.Fatalf("usage %+v", u)
	}
	if n, w := u.Decode(); n != 0 || w != 0 {
		t.Errorf("burst: %d tokens in %d ms (flow %d) read a speed; want none", n, w, u.Flow)
	}
}
