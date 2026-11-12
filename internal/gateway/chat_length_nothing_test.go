package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat reply cut at its length that said nothing — the whole budget
// spent on reasoning, no text and no call — is not a completed reply.
// qoder/dfmodel ended four replies of one dsh turn so (#2972): 48,000
// output tokens each, every one of them reasoning, and the agent, told
// the output limit was reached, went on at the user's "continue" and
// burned the same void again. The void fails at once, as the empty reply
// (#667's) does but asked nowhere: dsh speaks openai-completions and the
// qoder plugin serves Chat, so its four voids were relayed as they came —
// the relay fails them too. One that said something still goes as it
// came, and a short probe's answer — a request capped at a probe's worth,
// or a reply that spent no more than one — is no void, so a title cut
// short keeps the response.incomplete Codex asks again after (#1466) and
// a recorded ClinePass stream (16 tokens, all reasoning) is relayed as it
// was.
//
// Every test here reads its calls counter to hold the fix to its word:
// the upstream is asked once. A void is not asked again in place, for a
// 100%-reasoning cut at 48,000 tokens isn't the coin flip a Gemini empty
// STOP is — asking again would burn the budget twice over before the
// error (#2972 review).

// The void, on the translated paths: a Messages client and a Responses
// one in front of a Chat upstream. The error in the client's own
// protocol, at once, and the upstream asked once — never a reply ended
// as it should with nothing in it, and never a second ask.
func TestChatLengthVoidFailsAtOnce(t *testing.T) {
	fresh(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000,"completion_tokens_details":{"reasoning_tokens":48000}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	for _, a := range []struct{ name, path, body, ended string }{
		{"anthropic", "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"max_tokens"`},
		{"responses", "/v1/responses", `{"model":"qo/dfmodel","stream":true,"input":"hi"}`, `response.incomplete`},
	} {
		calls = 0
		code, body := post(t, a.path, a.body)
		if code != 200 || !strings.Contains(body, "an empty reply") || !strings.Contains(body, "cut at its length") ||
			strings.Contains(body, a.ended) || calls != 1 {
			t.Errorf("%s: a void cut answered %d after %d calls, not the length-void error at once: %s", a.name, code, calls, body)
		}
	}

	calls = 0
	code, body := post(t, "/v1/messages", `{"model":"qo/dfmodel","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 || !strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a void cut, not streamed: %d %s (after %d calls)", code, body, calls)
	}
	// the error names the limit the request itself set, and what to do
	if !strings.Contains(body, "limit was 128000 tokens") || !strings.Contains(body, "raise max_tokens") {
		t.Errorf("a void cut's error doesn't name the request's limit: %s", body)
	}
}

// The void, on the relay: a Chat client in front of a Chat upstream, as
// dsh's speaks and the qoder plugin serves (#2972). Streamed, the reply's
// end is held once its finish_reason is seen and nothing has been said in
// it: the client is told the error, not a reply that ended as it should
// with nothing in it, and [DONE] ends the stream. Not streamed, the whole
// reply fails with the error in the relay's place.
func TestChatLengthVoidFailsOnTheRelay(t *testing.T) {
	fresh(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		if !strings.Contains(string(b), `"stream":true`) {
			// asked whole, answered whole: a reply cut at its length that
			// said nothing, as one JSON body
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000,"completion_tokens_details":{"reasoning_tokens":48000}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000,"completion_tokens_details":{"reasoning_tokens":48000}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	calls = 0
	code, body := post(t, "/v1/chat/completions", `{"model":"qo/dfmodel","stream":true,"max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "an empty reply") || strings.Contains(body, `"finish_reason":"length"`) ||
		!strings.Contains(body, "data: [DONE]") || calls != 1 {
		t.Errorf("a void cut relayed: %d after %d calls, not the length-void error at once: %s", code, calls, body)
	}

	calls = 0
	code, body = post(t, "/v1/chat/completions", `{"model":"qo/dfmodel","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 || !strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a void cut relayed whole: %d %s (after %d calls)", code, body, calls)
	}
}

// A short probe's answer is no void: a request capped at a probe's worth
// (ClinePass's 16 tokens under a 100 cap, a title's) that said nothing
// goes as it came, on the relay and translated alike (#2972 review).
func TestChatLengthSmallProbeIsRelayed(t *testing.T) {
	fresh(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16,"completion_tokens_details":{"reasoning_tokens":16}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ name, path, body, ended string }{
		{"relay", "/v1/chat/completions", `{"model":"qo/dfmodel","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, `"finish_reason":"length"`},
		{"anthropic", "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"max_tokens"`},
	} {
		calls = 0
		code, body := post(t, a.path, a.body)
		if code != 200 || !strings.Contains(body, a.ended) || strings.Contains(body, "an empty reply") || calls != 1 {
			t.Errorf("%s: a short probe's void answered %d after %d calls, not relayed as it came: %s", a.name, code, calls, body)
		}
	}
}

// The size gate: a request that named no reply limit is not a probe's
// word, and a probe's cap doesn't spare a big void — an upstream that
// spent far past a small cap on reasoning and said nothing fails all the
// same (#2972 review: "only above some size or when the request didn't
// set a small max_tokens").
func TestChatLengthVoidNeedsSize(t *testing.T) {
	fresh(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000,"completion_tokens_details":{"reasoning_tokens":48000}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	// no limit named: the relay fails the void (the cap gate's arm)
	calls = 0
	code, body := post(t, "/v1/chat/completions", `{"model":"qo/dfmodel","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a void cut with no limit named: %d after %d calls, not the length-void error: %s", code, calls, body)
	}

	// a probe's cap ignored by the upstream: the spent gate's arm
	calls = 0
	code, body = post(t, "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a void cut past a probe's cap: %d after %d calls, not the length-void error: %s", code, calls, body)
	}
}

// A length cut that said something is the truncation it always was, told
// to the client as such.
func TestChatLengthWithTextIsTruncation(t *testing.T) {
	fresh(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Half an ans"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ name, path, body, ended string }{
		{"anthropic", "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"max_tokens"`},
		{"responses", "/v1/responses", `{"model":"qo/dfmodel","stream":true,"input":"hi"}`, `response.incomplete`},
	} {
		calls = 0
		code, body := post(t, a.path, a.body)
		if code != 200 || !strings.Contains(body, "Half an ans") || !strings.Contains(body, a.ended) ||
			strings.Contains(body, "an empty reply") || calls != 1 {
			t.Errorf("%s: a cut that said something answered %d after %d calls: %s", a.name, code, calls, body)
		}
	}

	calls = 0
	code, body := post(t, "/v1/messages", `{"model":"qo/dfmodel","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "Half an ans") || !strings.Contains(body, `"stop_reason":"max_tokens"`) ||
		strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a cut that said something, not streamed: %d after %d calls: %s", code, calls, body)
	}
}
