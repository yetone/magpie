package gateway

import (
	"encoding/json"
	"slices"
	"testing"
)

// Lee931018 on X: through the Claude bridge, Codex desktop's browser tool
// mcp__cua_repl__js, whose inputSchema is a oneOf of objects at its root,
// never reached the model, since Claude Code leaves out a tool whose root
// Anthropic's API refuses. The tools.json the run's Claude Code is handed
// has every schema with an object root, its variants' fields merged; one
// that already has one goes as it came.
func TestClaudeBridgeToolsHaveObjectRoots(t *testing.T) {
	js := json.RawMessage(`{"oneOf":[` +
		`{"type":"object","properties":{"code":{"type":"string"},"timeout_ms":{"type":"number"}},"required":["code"],"additionalProperties":false},` +
		`{"type":"object","properties":{"code":{"type":"string"},"title":{"type":"string"}},"required":["code","title"],"additionalProperties":false}` +
		`],"$schema":"http://json-schema.org/draft-07/schema#"}`)
	for _, root := range []string{"oneOf", "anyOf", "allOf"} {
		schema := json.RawMessage(string(js[:2]) + root + string(js[7:]))
		plain := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		req := &Request{Tools: []Tool{{Name: "mcp__cua_repl__js", Schema: schema}, {Name: "mcp__cua_repl__js_reset", Schema: plain}}}
		offer := bridgeTools(req)
		if len(offer) != 2 {
			t.Fatalf("%s: offered %d tools, want 2", root, len(offer))
		}
		var m map[string]any
		if err := json.Unmarshal(offer[0].InputSchema, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"oneOf", "anyOf", "allOf"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s: the js tool's schema keeps %s at its root: %s", root, k, offer[0].InputSchema)
			}
		}
		if m["type"] != "object" {
			t.Errorf("%s: the js tool's root type is %v, want object: %s", root, m["type"], offer[0].InputSchema)
		}
		props, _ := m["properties"].(map[string]any)
		for _, k := range []string{"code", "timeout_ms", "title"} {
			if _, ok := props[k]; !ok {
				t.Errorf("%s: the js tool lost %s: %s", root, k, offer[0].InputSchema)
			}
		}
		var req1 []string
		rs, _ := m["required"].([]any)
		for _, r := range rs {
			req1 = append(req1, r.(string))
		}
		slices.Sort(req1)
		want := []string{"code"}
		if root == "allOf" {
			want = []string{"code", "title"}
		}
		if !slices.Equal(req1, want) {
			t.Errorf("%s: required %v, want %v", root, req1, want)
		}
		if string(offer[1].InputSchema) != string(plain) {
			t.Errorf("%s: a schema with an object root changed: %s", root, offer[1].InputSchema)
		}
	}
}
