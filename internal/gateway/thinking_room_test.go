package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// WaiooT on X (Tencent Cloud Token Plan, deepseek-v4.1-flash): a memory tool
// capped its replies at max_tokens 4096 and named no reasoning. TokenHub runs
// DeepSeek's thinking by default (thinking "enabled", reasoning_effort
// "high"), the thinking spent all 4096 and every reply was cut at its length
// with no answer. A small reply limit sent to Tencent's DeepSeek with no
// reasoning asked has room to think above it; a limit with reasoning asked,
// one the user set as Max output, a limit already large, and the same request
// to another vendor's DeepSeek go as they came.
func TestTencentDeepSeekCapGetsThinkingRoom(t *testing.T) {
	fresh(t)
	u := &ownOutputUpstream{}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	for _, p := range []provider.Provider{
		{ID: "tencent", Name: "Tencent", Key: "k", Models: []string{"deepseek/deepseek-flash"},
			Chat: "http://tokenhub.tencentmaas.com/plan/v3"},
		{ID: "relay", Name: "Relay", Key: "k", Models: []string{"deepseek/deepseek-flash"},
			Chat: "http://relay.example.com/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	const msg = `"messages":[{"role":"user","content":"hi"}]`
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"4096 and no reasoning", `{"model":"tencent/deepseek/deepseek-flash","max_tokens":4096,` + msg + `}`, 4096 + 24000},
		{"max_completion_tokens", `{"model":"tencent/deepseek/deepseek-flash","max_completion_tokens":2000,` + msg + `}`, 0},
		{"effort asked", `{"model":"tencent/deepseek/deepseek-flash","max_tokens":4096,"reasoning_effort":"low",` + msg + `}`, 4096},
		{"thinking off", `{"model":"tencent/deepseek/deepseek-flash","max_tokens":4096,"thinking":{"type":"disabled"},` + msg + `}`, 4096},
		{"a large limit", `{"model":"tencent/deepseek/deepseek-flash","max_tokens":32000,` + msg + `}`, 32000},
		{"no limit", `{"model":"tencent/deepseek/deepseek-flash",` + msg + `}`, -1},
		{"another vendor's", `{"model":"relay/deepseek/deepseek-flash","max_tokens":4096,` + msg + `}`, 4096},
	} {
		if code, raw := postJSON(t, s, "/v1/chat/completions", c.body); code != 200 {
			t.Fatalf("%s: %d %s", c.name, code, raw)
		}
		got := u.take()
		if len(got) != 1 {
			t.Fatalf("%s: %d requests upstream", c.name, len(got))
		}
		field := "max_tokens"
		if _, ok := got[0]["max_completion_tokens"]; ok {
			field = "max_completion_tokens"
		}
		n, sent := got[0][field].(float64)
		switch {
		case c.want == 0 && (!sent || int(n) != 2000+24000):
			t.Errorf("%s: %s %v, want %d", c.name, field, n, 2000+24000)
		case c.want == -1 && sent:
			t.Errorf("%s: %s %v sent, want none", c.name, field, n)
		case c.want > 0 && int(n) != c.want:
			t.Errorf("%s: %s %v, want %d", c.name, field, n, c.want)
		}
	}
}
