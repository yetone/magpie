package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #374: Codex offering web search to a Responses provider goes through
// magpie's search, rebuilt; the request its provider gets still carries
// Codex's client_metadata, which a relay may check, and its text (an
// answer's schema), as a request passed through does.
func TestCodexSearchKeepsClientMetadata(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"ok\":true}"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":2,"total_tokens":11}}}`)}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, `{"model":"fake/m1","instructions":"You are Codex","stream":true,"store":false,
	  "include":["reasoning.encrypted_content"],"prompt_cache_key":"thread-1","reasoning":{"effort":"medium"},
	  "client_metadata":{"x-codex-installation-id":"inst-1","x-codex-window-id":"thread-1:0"},
	  "text":{"verbosity":"low","format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{"type":"object"}}},
	  "tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather?"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var q struct {
		Meta map[string]string `json:"client_metadata"`
		Text struct {
			Verbosity string `json:"verbosity"`
			Format    struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"format"`
		} `json:"text"`
	}
	json.Unmarshal(f.got, &q)
	if q.Meta["x-codex-installation-id"] != "inst-1" || q.Meta["x-codex-window-id"] != "thread-1:0" ||
		q.Text.Verbosity != "low" || q.Text.Format.Type != "json_schema" || q.Text.Format.Name != "codex_output_schema" {
		t.Errorf("provider got %s", f.got)
	}
}

// Neither goes to an API of another kind: Chat and Anthropic turn away a
// field they don't know.
func TestClientMetadataOnlyToResponses(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":"hi","client_metadata":{"a":"b"},"text":{"verbosity":"low"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"chat": buildChat(r, "m", "relay.example", false), "anthropic": buildAnthropic(r, "m")} {
		var m map[string]any
		json.Unmarshal(b, &m)
		if _, ok := m["client_metadata"]; ok {
			t.Errorf("%s got client_metadata: %s", name, b)
		}
		if _, ok := m["text"]; ok {
			t.Errorf("%s got text: %s", name, b)
		}
	}
	r, _ = parseResponses([]byte(`{"model":"m","input":"hi","client_metadata":null}`))
	var m map[string]any
	json.Unmarshal(buildResponses(r, "m", "relay.example", false), &m)
	if _, ok := m["client_metadata"]; ok {
		t.Errorf("a null client_metadata went on: %v", m)
	}
}
