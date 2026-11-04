package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// freshRates starts a test with no rate learned, and leaves none after.
func freshRates(t *testing.T) {
	t.Helper()
	resetRates()
	t.Cleanup(resetRates)
}

// liveFor is a counter for a request in flight whose route is rt.
func liveFor(rt *Route, proto provider.Protocol) *liveRow {
	f := &inflight{tr: &trace{}}
	if rt != nil {
		f.route.Store(rt)
	}
	return liveBegin(f, "m", proto)
}

func liveChatEvent(s string) []byte {
	return []byte(`data: {"choices":[{"delta":{"content":"` + s + `"}}]}` + "\n\n")
}

// The bytes are every byte the agent got, the content only the words, and
// a finish counted twice (an encoder's last write after serve) changes
// nothing.
func TestLiveCountsBytesAndContent(t *testing.T) {
	freshRates(t)
	live := liveFor(nil, provider.Chat)
	if b, c, at := live.totals(); b != 0 || c != 0 || at != 0 {
		t.Fatalf("a request with no byte yet: %d bytes, %d content, content at %d", b, c, at)
	}
	first := liveChatEvent("hello")
	live.observe(first)
	b, c, at := live.totals()
	if b != int64(len(first)) || c != 5 || at == 0 {
		t.Fatalf("after %q: %d bytes, %d content, content at %d", first, b, c, at)
	}
	live.finish(false, 42)
	live.finish(false, 42)
	if _, c, _ := live.totals(); c != 5 {
		t.Fatalf("content after it ended twice: %d, want 5", c)
	}
}

// The gateway's keepalive adds bytes, but neither content nor a newer
// content time: a stuck reply must look still.
func TestLiveHeartbeatIsBytesNotContent(t *testing.T) {
	live := liveFor(nil, provider.Chat)
	live.observe(liveChatEvent("a"))
	b0, c0, at0 := live.totals()
	time.Sleep(5 * time.Millisecond)
	beat := []byte(": keepalive\n")
	live.observe(beat)
	b, c, at := live.totals()
	if b != b0+int64(len(beat)) || c != c0 || at != at0 {
		t.Fatalf("a keepalive: bytes %d→%d, content %d→%d, content at %d→%d", b0, b, c0, c, at0, at)
	}
	live.observe(liveChatEvent("b"))
	if _, _, back := live.totals(); back <= at0 {
		t.Fatalf("content didn't move the time forward: %d then %d", at0, back)
	}
}

func TestCommentOnlyTellsHeartbeatsApart(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{": keepalive\n", true},
		{": keepalive\n\n", true},
		{"\n: ping\r\n\n", true},
		{"", false},
		{"\n\n", false},
		{"data: {\"a\":1}\n\n", false},
		{": keepalive\ndata: {\"a\":1}\n\n", false},
		{"data: {\"a\":\":\"}\n", false},
		{`{"a":1}`, false},
	} {
		if got := commentOnly([]byte(c.in)); got != c.want {
			t.Errorf("commentOnly(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// What counts as content in each protocol, from events as the vendors
// write them.
func TestContentBytesPerProtocol(t *testing.T) {
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		body  string
		want  int
	}{
		{"chat text", provider.Chat, `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`, 5},
		{"chat reasoning", provider.Chat, `{"choices":[{"delta":{"reasoning_content":"hm"}}]}`, 2},
		{"chat tool name", provider.Chat, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`, 4},
		{"chat tool args", provider.Chat, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}`, 7},
		{"chat usage", provider.Chat, `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5}}`, 0},
		{"responses delta", provider.Responses, `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"abc"}`, 3},
		{"responses done", provider.Responses, `{"type":"response.output_text.done","text":"abc"}`, 0},
		{"responses call", provider.Responses, `{"type":"response.output_item.added","item":{"type":"function_call","name":"shell","arguments":""}}`, 5},
		{"anthropic text", provider.Anthropic, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"abc"}}`, 3},
		{"anthropic thinking", provider.Anthropic, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"de"}}`, 2},
		{"anthropic tool", provider.Anthropic, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"grep","input":{}}}`, 4},
		{"anthropic input", provider.Anthropic, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`, 5},
		{"gemini text", provider.Gemini, `{"candidates":[{"content":{"parts":[{"text":"abc"}],"role":"model"}}]}`, 3},
		{"gemini call", provider.Gemini, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"ls","args":{}}}]}}]}`, 4},
		{"done", provider.Chat, `[DONE]`, 0},
		{"empty", provider.Chat, `{}`, 0},
	} {
		if got := liveContentOf(c.proto, []byte(c.body)); got != c.want {
			t.Errorf("%s: %s gave %d, want %d", c.name, c.body, got, c.want)
		}
	}
}

// A stream cut into writes of any size counts the same as written whole.
func TestContentCounterSurvivesSplitWrites(t *testing.T) {
	stream := string(liveChatEvent("hello")) + ": keepalive\n" + string(liveChatEvent(" wörld")) + "data: [DONE]\n\n"
	for _, size := range []int{1, 2, 3, 7, 13, len(stream)} {
		c := &liveContentCounter{proto: provider.Chat}
		for i := 0; i < len(stream); i += size {
			c.add([]byte(stream[i:min(i+size, len(stream))]))
		}
		c.flush()
		if c.total != 12 {
			t.Errorf("written in %d-byte pieces: %d content bytes, want 12", size, c.total)
		}
	}
	// a stream ending without its blank line still counts its last event
	c := &liveContentCounter{proto: provider.Chat}
	c.add([]byte(strings.TrimSuffix(string(liveChatEvent("abc")), "\n\n")))
	c.flush()
	if c.total != 3 {
		t.Errorf("a last event without its blank line: %d, want 3", c.total)
	}
}

// A whole JSON reply is not counted (nor copied): it arrives at once and
// shows nothing while it is out, and it is not learned from.
func TestContentCounterLeavesAWholeReply(t *testing.T) {
	freshRates(t)
	for i := 0; i < liveRateMin+1; i++ {
		live := liveFor(nil, provider.Chat)
		live.routed("vendor", "m")
		live.observe([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"completion_tokens":2}}`))
		if live.counter.sse || len(live.counter.part) != 0 || len(live.counter.data) != 0 {
			t.Fatalf("a whole reply was read as a stream or held: %+v", live.counter)
		}
		live.finish(false, 2)
	}
	if got := liveRates.estimate("vendor", "m", 100); got != 0 {
		t.Fatalf("whole replies were learned from: estimate %d", got)
	}
}

// A line longer than liveLineMost is passed over, not held.
func TestContentCounterDropsAnEndlessLine(t *testing.T) {
	c := &liveContentCounter{proto: provider.Chat}
	c.add([]byte("data: \""))
	chunk := []byte(strings.Repeat("x", 64<<10))
	for i := 0; i < 2*liveLineMost/len(chunk); i++ {
		c.add(chunk)
	}
	if len(c.part) > liveLineMost {
		t.Fatalf("held %d bytes of one line", len(c.part))
	}
	c.add([]byte("\"\n\n"))
	c.add(liveChatEvent("ok"))
	c.flush()
	if c.total != 2 {
		t.Fatalf("after an endless line: %d content bytes, want 2", c.total)
	}
}

// Three replies learned before an estimate, at their summed ratio, per
// provider and model, rounded down.
func TestRatesLearnThenEstimate(t *testing.T) {
	freshRates(t)
	liveRates.learn("vendor", "m", 100, 10)
	if got := liveRates.estimate("vendor", "m", 500); got != 0 {
		t.Fatalf("one sample estimated %d", got)
	}
	liveRates.learn("vendor", "m", 100, 10)
	liveRates.learn("vendor", "m", 200, 20)
	if got := liveRates.estimate("vendor", "m", 959); got != 95 {
		t.Fatalf("959 content bytes at 10:1 is 95 tokens, got %d", got)
	}
	if got := liveRates.estimate("other", "m", 950); got != 0 {
		t.Fatalf("another provider was estimated from this one's samples: %d", got)
	}
	// sums past what an int64 product holds still estimate
	for i := 0; i < 3; i++ {
		liveRates.learn("big", "m", 1<<40, 1<<38)
	}
	if got := liveRates.estimate("big", "m", 1<<30); got != 1<<28 {
		t.Fatalf("large sums estimated %d, want %d", got, 1<<28)
	}
}

// Only a streamed reply that didn't fail and whose vendor said its output
// is learned from; an agent gone after the last content still is.
func TestRatesLearnFromRepliesTheVendorFinished(t *testing.T) {
	freshRates(t)
	for _, c := range []struct {
		vendor string
		failed bool
		output int
		learn  bool
	}{
		{"v-failed", true, 10, false},
		{"v-nousage", false, 0, false},
		{"v-ok", false, 10, true},
	} {
		live := liveFor(nil, provider.Chat)
		live.routed(c.vendor, "m")
		live.observe(liveChatEvent("hello"))
		live.finish(c.failed, c.output)
		liveRates.mu.Lock()
		got := liveRates.m[c.vendor+"/m"]
		liveRates.mu.Unlock()
		if c.learn != (got.seen == 1) || c.learn && (got.bytes != 5 || got.output != 10) {
			t.Errorf("%s: learned %+v, want learned=%v", c.vendor, got, c.learn)
		}
	}
}

// The gateway's own requests are not counted, and every method is safe on
// the nil they get.
func TestLiveIsOptional(t *testing.T) {
	if l := liveBegin(nil, "m", provider.Chat); l != nil {
		t.Fatal("a request not in flight was counted")
	}
	var live *liveRow
	live.observe([]byte("x"))
	live.routed("p", "m")
	live.finish(true, 0)
	if liveOfInflight(nil) != nil || liveOfInflight(&inflight{}) != nil {
		t.Fatal("a request with no counter has one")
	}
	f := &inflight{}
	if l := liveBegin(f, "m", provider.Chat); liveOfInflight(f) != l {
		t.Fatal("the counter begun isn't the request's")
	}
}

// Writes reach the request's route: a keepalive its bytes and not its
// content time, and a route begun after the first write the running total.
func TestLiveWritesReachTheRoute(t *testing.T) {
	freshRates(t)
	rt := &Route{Agent: "a", Model: "m", Time: time.Now()}
	live := liveFor(rt, provider.Chat)
	first := liveChatEvent("hello")
	live.observe(first)
	if rt.Bytes != int64(len(first)) || rt.ContentAt == 0 || rt.Seq == 0 {
		t.Fatalf("the route didn't hear the first bytes: %+v", rt)
	}
	at, seq := rt.ContentAt, rt.Seq
	beat := []byte(": keepalive\n")
	live.observe(beat)
	if rt.Bytes != int64(len(first)+len(beat)) || rt.ContentAt != at || rt.Seq <= seq {
		t.Fatalf("after a keepalive the route has %d bytes, content at %d (was %d), seq %d (was %d)", rt.Bytes, rt.ContentAt, at, rt.Seq, seq)
	}

	f := &inflight{tr: &trace{}}
	late := liveBegin(f, "m", provider.Chat)
	late.observe(first)
	lateRoute := &Route{Agent: "a", Model: "m", Time: time.Now()}
	f.route.Store(lateRoute)
	late.observe(first)
	if want := int64(2 * len(first)); lateRoute.Bytes != want {
		t.Fatalf("a route begun late got %d bytes, want the running total %d", lateRoute.Bytes, want)
	}
}

// The route gets an estimate once its provider and model learned a rate,
// and none before.
func TestRouteGetsTheEstimateOnlyOnceItIsCalibrated(t *testing.T) {
	freshRates(t)
	rt := &Route{Agent: "a", Model: "m", Time: time.Now()}
	live := liveFor(rt, provider.Chat)
	live.routed("vendor", "m")
	live.observe(liveChatEvent("hello"))
	if rt.EstTok != 0 {
		t.Fatalf("estimated %d tokens with no sample", rt.EstTok)
	}
	for i := 0; i < liveRateMin; i++ {
		done := liveFor(nil, provider.Chat)
		done.routed("vendor", "m")
		done.observe(liveChatEvent("hello"))
		done.finish(false, 2)
	}
	live.observe(liveChatEvent("hello"))
	// 10 content bytes at 15 bytes for 6 tokens
	if rt.EstTok != 4 {
		t.Fatalf("10 content bytes at 2.5 bytes a token is 4; got %d", rt.EstTok)
	}
}
