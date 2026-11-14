package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestReasoningSpeedEvidence(t *testing.T) {
	thought := `data: {"choices":[{"delta":{"reasoning_content":"a thought"}}]}` + "\n\n"
	for _, c := range []struct {
		name   string
		proto  provider.Protocol
		event  string
		hidden bool
		want   bool
	}{
		{"chat", provider.Chat, thought, false, true},
		{"anthropic", provider.Anthropic, `data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"a thought"}}` + "\n\n", false, true},
		{"late Responses summary", provider.Responses, `data: {"type":"response.reasoning_summary_text.delta","delta":"a thought"}` + "\n\n", false, false},
		{"translated hidden Responses", provider.Responses, thought, false, false},
		{"translated redacted thinking", provider.Anthropic, thought, true, false},
		{"Gemini summary", provider.Gemini, thought, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := firstToken{start: time.Now().Add(-time.Second), hidden: c.hidden}
			f.see([]byte(c.event))
			f.start = f.start.Add(-time.Second)
			f.see([]byte(c.event))
			if got := f.reasoningMs(c.proto) > 0; got != c.want {
				t.Fatalf("reasoning evidence=%v; want %v", got, c.want)
			}
		})
	}
	f := firstToken{start: time.Now().Add(-time.Second)}
	f.see([]byte(thought + thought))
	if f.reasoningMs(provider.Chat) != 0 {
		t.Fatal("a buffered burst acquired reasoning timing")
	}
}

func TestReasoningSpeedThroughGateway(t *testing.T) {
	events := []string{
		`~data: {"choices":[{"delta":{"reasoning_content":"first thought"}}]}`,
		`~data: {"choices":[{"delta":{"reasoning_content":"second thought"}}]}`,
		`~data: {"choices":[{"delta":{"reasoning_content":"third thought"}}]}`,
		`~data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"check","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":120,"completion_tokens_details":{"reasoning_tokens":100}}}`,
		`data: [DONE]`,
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			fresh(t)
			// each thought a gap after the agent had the one before (#5948)
			rec := newHearing(0, "first thought", "second thought", "third thought")
			up := httptest.NewServer(&paced{events: events, gap: 150 * time.Millisecond, heard: rec.heard})
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Key: "k", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":300}`
			if path == "/v1/responses" {
				body = `{"model":"fake/m1","stream":true,"input":"hi"}`
			}
			s := New()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
			u := lastUsage(t)
			r := lastRoute(s)
			if rec.Code != 200 || u.FirstReasoning <= 0 || u.FirstText != 0 || u.Reasoning != 100 {
				t.Fatalf("streamed reasoning missing: %d %+v %s", rec.Code, u, rec.Body)
			}
			n, w := u.Decode()
			if n != 120 || w < usage.MinDecodeMs {
				t.Fatalf("visible reasoning has no speed: %d/%d %+v", n, w, u)
			}
			if r.FirstReasoning != u.FirstReasoning || len(r.Tries) != 1 || r.Tries[0].FirstReasoning <= 0 || s.Recent()[0].FirstReasoning != u.FirstReasoning {
				t.Fatalf("trace disagrees: %+v %+v", u, r)
			}
		})
	}
}

// A reply's speed is over the time it took to come, not what magpie
// writes down of it once it has: a busy disk held saving sticks after the
// answer for seconds, and the reply's ms with it, so a reply that streamed
// at its pace read as one held back and let go in a burst, and told no
// speed (#1939, bytecode-x: Millis 3772 for 450 ms of stream). The disk is
// stood in for by holding the sticks' save.
func TestSpeedEndsWithTheReply(t *testing.T) {
	events := []string{
		`~data: {"choices":[{"delta":{"reasoning_content":"first thought"}}]}`,
		`~data: {"choices":[{"delta":{"reasoning_content":"second thought"}}]}`,
		`~data: {"choices":[{"delta":{"reasoning_content":"third thought"}}]}`,
		`~data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"check","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":120,"completion_tokens_details":{"reasoning_tokens":100}}}`,
		`data: [DONE]`,
	}
	const held = 2 * time.Second
	fresh(t)
	// each thought a gap after the agent had the one before (#5948)
	rec := newHearing(0, "first thought", "second thought", "third thought")
	up := httptest.NewServer(&paced{events: events, gap: 150 * time.Millisecond, heard: rec.heard})
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	sticksSaving.Lock()
	let := time.AfterFunc(held, sticksSaving.Unlock)
	t.Cleanup(func() {
		if let.Stop() {
			sticksSaving.Unlock()
		}
	})
	began := time.Now()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if took := time.Since(began); took < held {
		t.Fatalf("the request took %v, before the sticks were let go: nothing waited on saving them", took)
	}
	u := lastUsage(t)
	if rec.Code != 200 || u.Reasoning != 100 {
		t.Fatalf("%d %+v %s", rec.Code, u, rec.Body)
	}
	if u.Millis >= held.Milliseconds() {
		t.Fatalf("the reply's ms %d count the %v magpie took saving after it", u.Millis, held)
	}
	if n, w := u.Decode(); n != 120 || w < usage.MinDecodeMs || w > u.Millis {
		t.Fatalf("a reply that came at its pace tells no speed: %d/%d %+v", n, w, u)
	}
	if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].Millis > u.Millis {
		t.Fatalf("trace: %+v", r.Tries)
	}
}

func TestHiddenReasoningThroughTranslation(t *testing.T) {
	events := []string{
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
		`~data: {"type":"response.reasoning_summary_text.delta","delta":"first summary"}`,
		`~data: {"type":"response.reasoning_summary_text.delta","delta":"second summary"}`,
		`~data: {"type":"response.reasoning_summary_text.delta","delta":"third summary"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"c1","name":"check"}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1500,"output_tokens_details":{"reasoning_tokens":1400}}}}`,
	}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(&paced{events: events, gap: 150 * time.Millisecond})
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Responses: up.URL + "/v1", Key: "k", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":300}`
			if path == "/v1/responses" {
				body = `{"model":"fake/m1","stream":true,"input":"hi"}`
			}
			s := New()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
			u := lastUsage(t)
			r := lastRoute(s)
			n, w := u.Decode()
			if rec.Code != 200 || u.FirstReasoning != 0 || r.FirstReasoning != 0 || n != 0 || w != 0 || u.Reasoning != 1400 {
				t.Fatalf("hidden reasoning acquired speed after translation: %d %+v %s", rec.Code, u, rec.Body)
			}
		})
	}
}

// A visible portion never licenses timing a sealed portion of the same turn.
func TestMixedRedactedReasoning(t *testing.T) {
	events := []string{
		`data: {"type":"message_start","message":{"id":"m","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`~data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought"}}`,
		`~data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"second thought"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"sealed-test"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"check","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":120}}`,
		`data: {"type":"message_stop"}`,
	}
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(&paced{events: events, gap: 150 * time.Millisecond})
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Anthropic: up.URL, Key: "k", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":300}`
			if path == "/v1/responses" {
				body = `{"model":"fake/m1","stream":true,"input":"hi"}`
			}
			s := New()
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
			row := lastUsage(t)
			n, ms := row.Decode()
			if w.Code != 200 || row.FirstReasoning != 0 || !row.ReasoningHidden || n != 0 || ms != 0 {
				t.Fatalf("mixed sealed reasoning timed: %d %+v", w.Code, row)
			}
		})
	}
}

func TestReasoningPacingEdges(t *testing.T) {
	thought := []byte(`data: {"choices":[{"delta":{"reasoning_content":"thought"}}]}` + "\n\n")
	tool := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}` + "\n\n")
	for _, c := range []struct {
		name string
		gap  time.Duration
		want bool
	}{
		{"below 100ms", 99 * time.Millisecond, false}, {"100ms", 100 * time.Millisecond, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := firstToken{start: time.Now().Add(-time.Second)}
			f.see(thought)
			f.start = time.Now().Add(-f.reasoning - c.gap)
			f.see(thought)
			if got := f.reasoningMs(provider.Chat) > 0; got != c.want {
				t.Fatalf("paced=%v; want %v", got, c.want)
			}
		})
	}
	f := firstToken{start: time.Now().Add(-time.Second)}
	f.see(tool)
	f.start = f.start.Add(-time.Second)
	f.see(thought)
	f.start = f.start.Add(-time.Second)
	f.see(thought)
	if f.reasoningMs(provider.Chat) != 0 {
		t.Fatal("a tool-first turn acquired reasoning timing")
	}
	// Two writes after a long quiet start still form a burst below the decode minimum.
	f = firstToken{start: time.Now().Add(-14 * time.Second)}
	f.see(thought)
	f.see(thought)
	if f.reasoningMs(provider.Chat) != 0 {
		t.Fatal("buffered reasoning acquired timing")
	}
}

func TestSlowReleasedReasoningIsUntimed(t *testing.T) {
	f := firstToken{start: time.Now().Add(-time.Second)}
	thought := []byte(`data: {"choices":[{"delta":{"reasoning_content":"one visible thought"}}]}` + "\n\n")
	f.see(thought)
	first := f.reasoning
	for i := 0; i < 40; i++ {
		f.start = time.Now().Add(-first - 10*time.Second - time.Duration(i)*6*time.Millisecond)
		f.see(thought)
	}
	end := first.Milliseconds() + 10250
	n, w := usage.DecodeOf(500, 450, end, first.Milliseconds(), 0, f.flowFor(450), f.reasoningMs(provider.Chat))
	if n != 0 || w != 0 {
		t.Fatalf("held reasoning inflated speed: %d/%d flow=%d", n, w, f.flowFor(450))
	}
}

func TestEncryptedReasoningWithoutTokenDetails(t *testing.T) {
	f := firstToken{start: time.Now().Add(-time.Second)}
	f.see([]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"synthetic-seal"}}` + "\n\n"))
	if !f.hidden {
		t.Fatal("encrypted reasoning discarded its hidden marker")
	}
}

func TestGeminiSummaryWithoutThoughtCount(t *testing.T) {
	f := firstToken{start: time.Now().Add(-time.Second)}
	f.see([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"visible summary","thought":true}]}}]}` + "\n\n"))
	if !f.hidesReasoning(provider.Gemini, 0) || !f.hidesReasoning(provider.Gemini, 7) || f.reasoningMs(provider.Gemini) != 0 {
		t.Fatal("Gemini summary timed without its hidden thought count")
	}
}

func TestCodexBackendHiddenReasoningWithoutCount(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	p := &paced{events: []string{
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
		`~data: {"type":"response.reasoning_summary_text.delta","delta":"summary"}`,
		`~data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"synthetic-seal"}}`,
		`~data: {"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":120}}}`,
	}, gap: 150 * time.Millisecond}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { p.ServeHTTP(w, r) })
	if code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"hi"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	r := lastUsage(t)
	n, ms := r.Decode()
	if !r.ReasoningHidden || n != 0 || ms != 0 {
		t.Fatalf("Codex hidden reasoning timed with no count: %+v", r)
	}
}

func TestResponsesToolWithoutReasoningStillTimed(t *testing.T) {
	events := []string{
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"c1","name":"check"}}`,
		`~data: {"type":"response.function_call_arguments.delta","delta":"{\"value\":"}`,
		`~data: {"type":"response.function_call_arguments.delta","delta":"1290}"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":10}}}`,
	}
	fresh(t)
	up := httptest.NewServer(&paced{events: events, gap: 150 * time.Millisecond})
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Responses: up.URL + "/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	row := lastUsage(t)
	n, ms := row.Decode()
	if w.Code != 200 || row.ReasoningHidden || n != 10 || ms < usage.MinDecodeMs {
		t.Fatalf("tool-only response lost timing: %d %+v", w.Code, row)
	}
}

func TestMultipleVisibleReasoningBlocks(t *testing.T) {
	f := firstToken{start: time.Now().Add(-time.Second)}
	for i, ev := range []string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"second thought"}}`,
	} {
		f.start = time.Now().Add(-time.Second - time.Duration(i)*150*time.Millisecond)
		f.see([]byte(ev + "\n\n"))
	}
	if first := f.reasoningMs(provider.Anthropic); first <= 0 || first != f.first.Milliseconds() {
		t.Fatal("multiple visible blocks lost their first reasoning time")
	}
}

func TestLiveVisibleReasoningFixture(t *testing.T) {
	var sample struct {
		First  int64 `json:"first_reasoning_ms"`
		Events []struct {
			At    int64  `json:"at_ms"`
			Delta string `json:"delta"`
		} `json:"events"`
		Record struct {
			Out, Reasoning int
			Ms             int64
			TTFT           int64 `json:"ttft_ms"`
			Flow           int64 `json:"flow_ms"`
		} `json:"record"`
	}
	b, err := os.ReadFile("testdata/reasoning-speed-visible.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &sample); err != nil {
		t.Fatal(err)
	}
	// see reads the wall clock after start is set, so the first event lands
	// at its at_ms plus however long that took: a millisecond more on a busy
	// machine (6790/6789 on macOS CI), so the bound is what really passed.
	f := firstToken{}
	var slack int64
	for i, e := range sample.Events {
		delta, _ := json.Marshal(e.Delta)
		set := time.Now()
		f.start = set.Add(-time.Duration(e.At) * time.Millisecond)
		f.see([]byte(`data: {"choices":[{"delta":{"reasoning_content":` + string(delta) + `}}]}` + "\n\n"))
		if i == 0 {
			slack = time.Since(set).Milliseconds() + 1
		}
	}
	if got, at := f.reasoningMs(provider.Chat), sample.Events[0].At; got < at || got > at+slack {
		t.Fatalf("live reasoning fixture lost its start: %d/%d (+%d ms passing)", got, at, slack)
	}
	// its 731 ms came after a 6.8 s wait, which tells no speed: under a
	// second after a wait over four times longer reads as a reply let go
	// whole (HeldWindowMs, pluviophile714 on Discord), though this one was
	// streamed. The same turn after a short wait tells it from its first
	// reasoning.
	rec, d := sample.Record, int64(6500)
	if n, ms := usage.DecodeOf(rec.Out, rec.Reasoning, rec.Ms, rec.TTFT, 0, rec.Flow, sample.First); n != 0 || ms != 0 {
		t.Fatalf("live tool turn after a 6.8 s wait: speed %d/%d; want none", n, ms)
	}
	n, ms := usage.DecodeOf(rec.Out, rec.Reasoning, rec.Ms-d, rec.TTFT-d, 0, rec.Flow, sample.First-d)
	if n != 76 || ms != 731 {
		t.Fatalf("live tool turn speed %d/%d; want 76/731", n, ms)
	}
}
