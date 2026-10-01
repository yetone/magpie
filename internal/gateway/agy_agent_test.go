package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Antigravity CLI (agy) asks as Google's Gemini SDK, its User-Agent
// google-genai-sdk/… with nothing of agy's own, and sends its key as
// x-goog-api-key; the key magpie starts it with names it (TokenFor), so its
// calls are recorded as agy's, as Gemini's ?key= names it too. A command
// copied before, with the plain token, still gets an answer, recorded as
// the SDK's as it was.
func TestAgyAgent(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	const ua = "google-genai-sdk/1.71.0 gl-go/go1.28-20260721-RC03 cl/951519500"
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	for i, c := range []struct{ header, query, want string }{
		{TokenFor("agy"), "", "agy"},
		{"", TokenFor("agy"), "agy"},
		{Token, "", "google-genai-sdk"},
	} {
		path := "/v1beta/models/fake/m1:streamGenerateContent?alt=sse"
		if c.query != "" {
			path += "&key=" + c.query
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("User-Agent", ua)
		if c.header != "" {
			req.Header.Set("x-goog-api-key", c.header)
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "OK") {
			t.Fatalf("%+v: %d %s", c, rec.Code, rec.Body.String())
		}
		var got []usage.Record
		for range 100 {
			if got = usage.Load(time.Time{}); len(got) > i {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(got) <= i || got[i].Agent != c.want {
			t.Fatalf("%+v: records %+v, want the last %s's", c, got, c.want)
		}
	}
}
