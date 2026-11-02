package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The ChatGPT backend too busy for a turn, said as a status, once Codex
// has been kept alive with the stream's headers, reaches it as the
// backend says an overload in a stream: response.failed with
// server_is_overloaded, which Codex shows as the model at capacity. As
// server_error, Codex took it for a dropped stream and sent the turn
// five more times ("reconnecting 5/5", wjh66 on Discord).
func TestCodexKeptAliveOverloadSaysServerIsOverloaded(t *testing.T) {
	for name, failed := range map[string]reply{
		"503 message": {503, "application/json", `{"message":"Our servers are currently overloaded. Please try again later."}`},
		"529":         {529, "application/json", overloadedStatus},
	} {
		t.Run(name, func(t *testing.T) {
			k, w := keepHeldAfter, watchEvery
			keepHeldAfter, watchEvery = time.Millisecond, time.Millisecond
			t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
			o := &overloadedBackend{replies: []reply{failed}}
			oneCodexAccount(t, o)
			retryPause = 20 * time.Millisecond // past keepHeldAfter: Codex is sent the headers
			code, body := codexPost(t, oneAccountTurn)
			if code != 200 || o.asked() != 1+lastRetries {
				t.Fatalf("%d %q, asked %d times", code, body, o.asked())
			}
			var got string
			for _, l := range strings.Split(body, "\n") {
				data, ok := strings.CutPrefix(l, "data: ")
				if !ok {
					continue
				}
				var ev struct {
					Type     string `json:"type"`
					Response struct {
						Error struct{ Code, Message string } `json:"error"`
					} `json:"response"`
				}
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Type == "response.failed" {
					got = ev.Response.Error.Code
					if !strings.Contains(ev.Response.Error.Message, "overloaded") {
						t.Errorf("message %q", ev.Response.Error.Message)
					}
				}
			}
			if got != "server_is_overloaded" {
				t.Fatalf("response.failed code %q, want server_is_overloaded:\n%s", got, body)
			}
		})
	}
}

// Only an overload is said as one: another failure told in a stream
// keeps server_error, which Codex retries.
func TestStreamErrorOverloadOnly(t *testing.T) {
	for _, c := range []struct {
		status int
		msg    string
		want   bool
	}{
		{529, "busy", true},
		{503, "Our servers are currently overloaded.", true},
		{503, "Service Unavailable", false},
		{502, "upstream connect error", false},
		{429, "overloaded with requests", false},
		{400, "overloaded", false},
	} {
		if got := saidOverloaded(c.status, c.msg); got != c.want {
			t.Errorf("%d %q: %v, want %v", c.status, c.msg, got, c.want)
		}
	}
}
