package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #378: Codex's compaction summary goes without its tools; xAI's API turns
// away the tool_choice left beside none, so it goes without that too.
func TestCodexCompactsThroughXAI(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_x1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_x1","status":"completed","output":[]}}`)}
	f.refuse = func(body []byte) (int, string) {
		var q map[string]any
		json.Unmarshal(body, &q)
		if tools, _ := q["tools"].([]any); len(tools) == 0 && q["tool_choice"] != nil {
			return 400, `{"code":"Client specified an invalid argument","error":"Invalid request content: A tool_choice was set on the request but no tools were specified."}`
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: "http://api.x.ai/v1"}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"fake/m1","stream":true,"store":false,
	  "tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"low"},
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"remember the word ping"}]},{"type":"compaction_trigger"}]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"compaction"`) {
		t.Fatalf("%d %s\nxAI got %s", rec.Code, rec.Body.String(), f.got)
	}
	if strings.Contains(string(f.got), `"tool_choice"`) || !strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("xAI got %s", f.got)
	}

	// a request with its tools keeps its tool_choice
	f.reply = sse(`data: {"type":"response.completed","response":{"id":"resp_x2","status":"completed","output":[]}}`)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fake/m1","stream":true,"input":"hi","tool_choice":"auto",
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(string(f.got), `"tool_choice":"auto"`) {
		t.Errorf("%d %s\nxAI got %s", rec.Code, rec.Body.String(), f.got)
	}
}
