package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const (
	commitSchema = `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`
	prSchema     = `{"type":"object","properties":{"title":{"type":"string","maxLength":120},"body":{"type":"string"}},"required":["title","body"],"additionalProperties":false}`
)

// The Codex app's "generate commit message" and pull request text are
// ephemeral turns whose answer must be the JSON its schema asks for
// ({"message"}, {"title","body"}). A model behind the Chat API never sees
// the schema and answers in plain text, which the app turned down (makenxis
// on X), so magpie hands the text back in the schema's shape.
func TestCodexCommitMessageOnMagpieProvider(t *testing.T) {
	for _, c := range []struct{ source, schema, said, want string }{
		{"commit_message", commitSchema, `Fix login redirect loop`, `{"message":"Fix login redirect loop"}`},
		{"commit_message", commitSchema, "```\\nFix login redirect loop\\n\\nKeep the next URL.\\n```", `{"message":"Fix login redirect loop\n\nKeep the next URL."}`},
		{"commit_message", commitSchema, `{\"message\":\"Own JSON\"}`, `{"message":"Own JSON"}`},
		{"pull_request_message", prSchema, "Fix login loop\\n\\n## Summary\\n- redirect", `{"body":"## Summary\n- redirect","title":"Fix login loop"}`},
	} {
		f := &fake{t: t, reply: sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"`+c.said+`"}}]}`,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
			`data: [DONE]`)}
		setup(t, provider.Chat, f)
		s := New()
		body := `{"model":"fake/m1","stream":true,"input":"Using the supplied git context below, generate a git commit message.","text":{"format":{"type":"json_schema","schema":` + c.schema + `}}}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+c.source+`"}`)
		s.Handler().ServeHTTP(rec, req)
		text, _ := outputText(t, rec.Body.String())
		if rec.Code != 200 || text != c.want || f.calls != 1 {
			t.Errorf("%s %q: %d %q, want %q, calls %d", c.source, c.said, rec.Code, text, c.want, f.calls)
		}
	}
}

func TestCommitJSON(t *testing.T) {
	var both titleShape
	json.Unmarshal([]byte(`{"properties":{"message":{"type":"string"},"title":{"type":"string","maxLength":10},"body":{"type":"string"}},"required":["message","title","body"]}`), &both)
	long := strings.Repeat("x", 80)
	for _, c := range []struct {
		in    string
		shape titleShape
		want  string
	}{
		{"Add tests", titleShape{}, `{"message":"Add tests"}`},
		{"message: Add tests", titleShape{}, `{"message":"Add tests"}`},
		{long + "\n\nbody", titleShape{}, `{"message":"` + long[:72] + `\n\nbody"}`},
		{"Add the tests\n\nlong body", both, `{"body":"long body","message":"Add the tests\n\nlong body","title":"Add the te"}`},
		{"", titleShape{}, ""},
		{"{broken", titleShape{}, ""},
	} {
		if got := commitJSON(c.in, c.shape); got != c.want {
			t.Errorf("commitJSON(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
