package gateway

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// mistralRefuse answers as Mistral's chat completions do a body with a
// field it doesn't know: 422 extra_forbidden, every such field listed.
func mistralRefuse(body []byte) (int, string) {
	known := []string{"model", "messages", "stream", "stream_options", "max_tokens", "temperature", "top_p", "tools", "tool_choice",
		"parallel_tool_calls", "response_format", "stop", "random_seed", "presence_penalty", "frequency_penalty", "n"}
	var m map[string]json.RawMessage
	json.Unmarshal(body, &m)
	var detail []string
	for k, v := range m {
		if !slices.Contains(known, k) {
			detail = append(detail, fmt.Sprintf(`{"type":"extra_forbidden","loc":["body",%q],"msg":"Extra inputs are not permitted","input":%s}`, k, v))
		}
	}
	if len(detail) == 0 {
		return 0, ""
	}
	return 422, `{"object":"error","message":{"detail":[` + strings.Join(detail, ",") + `]},"type":"invalid_request_error","param":null,"code":null,"raw_status_code":422}`
}

// OpenCode's store:false (and Kimi Code's thinking switch) to Mistral, which
// turns away fields it doesn't know (#393): asked again without them, and
// not sent them again.
func TestMistralRefusesStore(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`), refuse: mistralRefuse}
	setup(t, provider.Chat, f)
	srv := New()
	for _, c := range []struct {
		body  string
		calls int
	}{
		{`{"model":"m1","stream":true,"store":false,"messages":[{"role":"user","content":"hi"}],"max_tokens":20}`, 2},
		{`{"model":"m1","stream":true,"store":false,"messages":[{"role":"user","content":"hi"}],"max_tokens":20}`, 1},
		{`{"model":"m1","stream":true,"thinking":{"type":"enabled","keep":"all"},"messages":[{"role":"user","content":"hi"}]}`, 2},
		{`{"model":"m1","stream":true,"store":false,"thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`, 1},
	} {
		calls := f.calls
		code, body := postTo(t, srv, "/v1/chat/completions", c.body)
		if code != 200 || !strings.Contains(body, `"ok"`) {
			t.Fatalf("%s: %d %s", c.body, code, body)
		}
		if n := f.calls - calls; n != c.calls {
			t.Errorf("%s: %d upstream calls, want %d", c.body, n, c.calls)
		}
	}
	// anything else refused goes back to the client as it came
	f.refuse = func([]byte) (int, string) {
		return 422, `{"object":"error","message":{"detail":[{"type":"missing","loc":["body","messages"],"msg":"Field required"}]}}`
	}
	calls := f.calls
	if code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"m1","store":false,"messages":[]}`); code != 422 || f.calls-calls != 1 {
		t.Errorf("other refusal: %d after %d calls %s", code, f.calls-calls, body)
	}
}
