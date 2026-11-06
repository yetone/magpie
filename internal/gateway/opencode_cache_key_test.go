package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// OpenCode 1 (opencode-ai 1.19.5) with its provider's setCacheKey writes
// the session's key into a Chat body as camelCase promptCacheKey, as a
// real run recorded it (#5468): the upstream is sent prompt_cache_key,
// the field every Chat API reads, and no promptCacheKey it doesn't know.
// A prompt_cache_key the body has already is the one sent.
func TestOpenCodeCamelCacheKey(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	for _, c := range []struct{ keys, want string }{
		{`"promptCacheKey":"ses_1a2b"`, "ses_1a2b"},
		{`"promptCacheKey":"ses_1a2b","prompt_cache_key":"own"`, "own"},
		{`"prompt_cache_key":"own"`, "own"},
	} {
		f.got = nil
		body := `{"model":"fake/m1","stream":true,` + c.keys + `,"messages":[{"role":"user","content":"a <b> & c"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+Token)
		req.Header.Set("User-Agent", "opencode/1.19.5 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.5")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.keys, rec.Code, rec.Body.String())
		}
		if got := gjson.GetBytes(f.got, "prompt_cache_key").String(); got != c.want {
			t.Fatalf("%s: upstream prompt_cache_key %q, want %q: %s", c.keys, got, c.want, f.got)
		}
		if gjson.GetBytes(f.got, "promptCacheKey").Exists() {
			t.Fatalf("%s: promptCacheKey went upstream: %s", c.keys, f.got)
		}
		if got := gjson.GetBytes(f.got, "messages.0.content").String(); got != "a <b> & c" {
			t.Fatalf("%s: message %q", c.keys, got)
		}
	}
}
