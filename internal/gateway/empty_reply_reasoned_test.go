package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Copilot's gemini-4-hydrogen ended a reply with no text, call or
// finish_reason having counted 2351 reasoning tokens of 2359, and the
// error said only "an empty reply … try again"; trying again six times
// ended the same way (yuushabu_buyinn on X). The error now says what the
// usage counted, and what to do when trying again doesn't help, relayed
// and translated alike; a reply that counted no output says what it did.
func TestEmptyReplySaysWhatItSpent(t *testing.T) {
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	role := `data: {"choices":[{"index":0,"delta":{"content":null,"role":"assistant"}}],"created":1795440000,"id":"c1","model":"gemini-4-hydrogen"}`
	spent := `data: {"choices":[],"created":1795440000,"id":"c1","usage":{"completion_tokens":2359,"prompt_tokens":91000,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":93359,"reasoning_tokens":2351},"model":"gemini-4-hydrogen"}`
	nothing := `data: {"choices":[],"created":1795440000,"id":"c1","usage":{"completion_tokens":0,"prompt_tokens":91000,"total_tokens":91000},"model":"gemini-4-hydrogen"}`
	const told = "the model reasoned for 2351 of the 2359 output tokens it counted, and its reply carried no text or tool call; if trying again ends the same way, switch to another model or compact the conversation"
	for _, x := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"cp/gemini-4-hydrogen","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"cp/gemini-4-hydrogen","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"cp/gemini-4-hydrogen","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"cp/gemini-4-hydrogen","stream":true,"input":"hi"}`},
	} {
		t.Run(x.path, func(t *testing.T) {
			fresh(t)
			reply := ""
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, reply)
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "cp", Name: "Copilot", Key: "k", Models: []string{"gemini-4-hydrogen"}, Chat: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			reply = sse(role, spent, `data: [DONE]`)
			if _, body := post(t, x.path, x.body); !strings.Contains(body, "Copilot: "+emptyReply+" — "+told) {
				t.Errorf("a reply that reasoned and said nothing: %q", body)
			}
			reply = sse(role, nothing, `data: [DONE]`)
			if _, body := post(t, x.path, x.body); !strings.Contains(body, emptyReply) || strings.Contains(body, "output tokens") || strings.Contains(body, "compact") {
				t.Errorf("a reply that counted no output: %q", body)
			}
		})
	}
}

func TestEmptyReplyFailedWords(t *testing.T) {
	for _, c := range []struct {
		u    Usage
		want string
	}{
		{Usage{}, "X: " + emptyReply},
		{Usage{Input: 9}, "X: " + emptyReply},
		{Usage{Output: 40}, "X: " + emptyReply + " — the model counted 40 output tokens, and its reply carried no text or tool call; if trying again ends the same way, switch to another model or compact the conversation"},
		{Usage{Output: 10, Reasoning: 30}, "X: " + emptyReply + " — the model counted 30 reasoning tokens, and its reply carried no text or tool call; if trying again ends the same way, switch to another model or compact the conversation"},
	} {
		if got := emptyReplyFailed("X", c.u); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.u, got, c.want)
		}
	}
}
