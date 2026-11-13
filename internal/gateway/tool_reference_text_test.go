package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// textOnlyResults answers as emilochen's DeepSeek V4.1 behind an
// Anthropic-compatible URL answered Claude Code (on X, 11-13): a
// tool_reference block anywhere in a tool_result's content is a 400
// "DeepSeek V4.1 supports text and image content only; got
// 'tool_reference'". said is the refusal's body, for a vendor that words
// it otherwise.
type textOnlyResults struct {
	said   string
	mu     sync.Mutex
	bodies []string
}

const deepseekV41RefsRefused = `{"type":"error","error":{"type":"invalid_request_error","message":"DeepSeek V4.1 supports text and image content only; got 'tool_reference'"}}`

func (v *textOnlyResults) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.bodies = append(v.bodies, string(b))
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if hasResultRefs(b) {
		w.WriteHeader(400)
		io.WriteString(w, v.said)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		io.WriteString(w, `{"input_tokens":12}`)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4.1","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *textOnlyResults) sent() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.bodies...)
}

// Claude Code's turn after ToolSearch loaded a deferred tool: the tool on
// the list with defer_loading, the answer a tool_reference beside text.
func toolSearchTurn(model string) string {
	return `{"model":"` + model + `","max_tokens":64,
		"tools":[
			{"name":"ToolSearch","description":"Find a tool.","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"name":"WebFetch","description":"Fetch a page.","input_schema":{"type":"object","properties":{}},"defer_loading":true},
			{"name":"NotebookEdit","description":"Edit a notebook.","input_schema":{"type":"object","properties":{}},"defer_loading":true}],
		"messages":[
			{"role":"user","content":"fetch it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ToolSearch","input":{"query":"select:WebFetch"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"Found 1 tool."},{"type":"tool_reference","tool_name":"WebFetch"}]}]}]}`
}

// emilochen on X: a provider that refuses a tool_reference in a tool_result
// is asked again at once with each reference as text naming the tool, the
// tool it names offered without defer_loading, and is sent it that way
// from then on (count_tokens too). Other deferred tools stay deferred, and
// the tool_use/tool_result pairing stays. A provider that takes the
// references keeps getting them.
func TestToolReferenceRefusedGoesAsText(t *testing.T) {
	fresh(t)
	ds := &textOnlyResults{said: deepseekV41RefsRefused}
	dsSrv := httptest.NewServer(ds)
	t.Cleanup(dsSrv.Close)
	takes := &openDesignVendor{takes: true}
	tkSrv := httptest.NewServer(takes)
	t.Cleanup(tkSrv.Close)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Anthropic: dsSrv.URL, Models: []string{"deepseek-v4.1"}},
		{ID: "other", Name: "Other", Key: "k", Anthropic: tkSrv.URL, Models: []string{"deepseek-v4.1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
	asText := func(t *testing.T, got string) {
		t.Helper()
		if hasResultRefs([]byte(got)) {
			t.Fatalf("a tool_reference was sent again: %s", got)
		}
		var m struct {
			Tools []struct {
				Name  string          `json:"name"`
				Defer json.RawMessage `json:"defer_loading"`
			} `json:"tools"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(got), &m); err != nil || len(m.Messages) != 3 || len(m.Tools) != 3 {
			t.Fatalf("sent %d messages, %d tools (%v): %s", len(m.Messages), len(m.Tools), err, got)
		}
		for _, tl := range m.Tools {
			if deferred := tl.Defer != nil; deferred != (tl.Name == "NotebookEdit") {
				t.Errorf("%s went with defer_loading %s", tl.Name, tl.Defer)
			}
		}
		var res []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		json.Unmarshal(m.Messages[2].Content, &res)
		if len(res) != 1 || res[0].Type != "tool_result" || res[0].ToolUseID != "toolu_1" || len(res[0].Content) != 2 ||
			res[0].Content[0].Text != "Found 1 tool." ||
			res[0].Content[1].Type != "text" || res[0].Content[1].Text != "Tool WebFetch is loaded and can be called now." {
			t.Fatalf("the ToolSearch answer went as %s", m.Messages[2].Content)
		}
	}

	post("/v1/messages", toolSearchTurn("relay/deepseek-v4.1"))
	sent := ds.sent()
	if len(sent) != 2 || !hasResultRefs([]byte(sent[0])) {
		t.Fatalf("the relay was asked %d times, want the references once and then text: %q", len(sent), sent)
	}
	asText(t, sent[1])

	// learned: counting tokens, and the next turn, go as text at once
	post("/v1/messages/count_tokens", toolSearchTurn("relay/deepseek-v4.1"))
	post("/v1/messages", toolSearchTurn("relay/deepseek-v4.1"))
	if sent = ds.sent(); len(sent) != 4 {
		t.Fatalf("after the refusal the relay was asked %d times, want 4", len(sent))
	}
	asText(t, sent[2])
	asText(t, sent[3])

	post("/v1/messages", toolSearchTurn("other/deepseek-v4.1"))
	if got := takes.sent(); len(got) != 1 || !hasResultRefs([]byte(got[0])) || strings.Contains(got[0], "is loaded and can be called") {
		t.Fatalf("a provider that takes the references was sent %q", got)
	}
}

// A 400 that names tool_reference only as what the endpoint expected
// (DeepSeek's refusal of a tool_definition), or only inside a request it
// echoes back, is not a refusal of the references: nothing is learned and
// the refusal reaches the client.
func TestToolReferenceNamedOtherwiseLearnsNothing(t *testing.T) {
	for _, said := range []string{
		"{\"error\":{\"message\":\"messages[2].content[0].content: unknown variant `tool_definition`, expected `tool_reference`\"}}",
		`{"detail":[{"msg":"Extra inputs are not permitted","input":{"type":"tool_reference","tool_name":"WebFetch"}}]}`,
		`{"error":{"message":"max_tokens: invalid value"}}`,
	} {
		if refsRefused([]byte(said)) {
			t.Errorf("read as a refusal of tool_reference: %s", said)
		}
	}
	for _, said := range []string{
		deepseekV41RefsRefused,
		"{\"error\":{\"message\":\"messages[2].content[0].content[1]: unknown variant `tool_reference`, expected `text` or `image`\"}}",
		`{"error":{"message":"content type tool_reference is not supported"}}`,
	} {
		if !refsRefused([]byte(said)) {
			t.Errorf("not read as a refusal of tool_reference: %s", said)
		}
	}

	fresh(t)
	ds := &textOnlyResults{said: "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"unknown variant `tool_definition`, expected `tool_reference`\"}}"}
	srv := httptest.NewServer(ds)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL, Models: []string{"deepseek-v4.1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(toolSearchTurn("relay/deepseek-v4.1"))))
	if rec.Code != 400 || len(ds.sent()) != 1 || !s.fits("relay", shapeRefs, provider.Anthropic) {
		t.Fatalf("status %d after %d asks, refs fit %v: %s", rec.Code, len(ds.sent()), s.fits("relay", shapeRefs, provider.Anthropic), rec.Body)
	}
}

// withRefsAsText leaves a body without references as it was, and drops a
// reference that names no tool.
func TestRefsAsTextLeavesOthers(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"done"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"x"}]}]}]}`,
		`{"messages":[{"role":"user","content":"hi"}]}`,
	} {
		if got := string(withRefsAsText([]byte(body))); got != body {
			t.Errorf("%s\nwent as %s", body, got)
		}
	}
	for body, loaded := range map[string]int{
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"tool_reference","tool_name":""},{"type":"tool_reference","tool_name":"Read"}]}]}]}`: 1,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"tool_reference"}]}]}]}`:                                                             0,
	} {
		if got := string(withRefsAsText([]byte(body))); strings.Contains(got, "tool_reference") || strings.Count(got, "is loaded") != loaded {
			t.Errorf("%s\nwent as %s", body, got)
		}
	}
}
