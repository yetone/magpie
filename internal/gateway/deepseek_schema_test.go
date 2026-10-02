package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestUnicodeNullEscape(t *testing.T) {
	for _, tc := range [][2]string{
		{`^[^\0]*$`, `^[^\u0000]*$`},
		{`\\0`, `\\0`},
		{`\01`, `\01`},
		{`\u0000`, `\u0000`},
		{`^https?://`, `^https?://`},
		{`\\\0`, `\\\u0000`},
	} {
		if got := unicodeNullEscape(tc[0]); got != tc[1] {
			t.Errorf("%q: got %q, want %q", tc[0], got, tc[1])
		}
	}
}

func TestDeepSeekToolPatternsOnlySchemas(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"\\0"}],"tools":[{"name":"Artifact","input_schema":{"type":"object","properties":{"content":{"type":"string","pattern":"^[^\\0]*$","default":{"pattern":"\\0"}}},"const":{"pattern":"\\0"}}},{"type":"function","function":{"name":"other","parameters":{"anyOf":[{"pattern":"\\0"}]}}}]}`)
	p := provider.Provider{Preset: "deepseek"}
	out := deepseekToolPatterns(p, provider.Anthropic, input)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tools := got["tools"].([]any)
	schema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	field := schema["properties"].(map[string]any)["content"].(map[string]any)
	if field["pattern"] != `^[^\u0000]*$` {
		t.Fatal("Artifact pattern not normalized")
	}
	if field["default"].(map[string]any)["pattern"] != `\0` || schema["const"].(map[string]any)["pattern"] != `\0` {
		t.Fatal("schema data changed")
	}
	if got["messages"].([]any)[0].(map[string]any)["content"] != `\0` {
		t.Fatal("user message changed")
	}
	other := tools[1].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if other["anyOf"].([]any)[0].(map[string]any)["pattern"] != `\u0000` {
		t.Fatal("Chat function schema not normalized")
	}
	unchanged := []byte(` {"tools":[{"input_schema":{"pattern":"^https?://"}}]} `)
	if !bytes.Equal(deepseekToolPatterns(p, provider.Anthropic, unchanged), unchanged) {
		t.Fatal("unchanged request was re-encoded")
	}
}

func TestDeepSeekToolPatternsByteIdentity(t *testing.T) {
	p := provider.Provider{Preset: "deepseek"}

	// 1. A body with nothing to fix passes through byte for byte.
	nothingToFix := []byte(`{
		"z_order": 1,
		"a_order": 2,
		"html_description": "User description with <special> & characters",
		"tools": [
			{"name": "test", "description": "<escapes> & unescaped", "input_schema": {"type": "object", "properties": {"url": {"pattern": "^https?://.*$"}}}}
		]
	}`)
	if got := deepseekToolPatterns(p, provider.Anthropic, nothingToFix); !bytes.Equal(got, nothingToFix) {
		t.Fatalf("body with nothing to fix did not pass through byte for byte:\ngot : %s\nwant: %s", got, nothingToFix)
	}

	// 2. A body with a pattern to fix differs ONLY at the pattern, preserving
	// key order, whitespaces, and <, >, & characters in tool descriptions.
	fixedBodyInput := []byte(`{
		"b_key": "val",
		"a_key": "val",
		"tools": [
			{
				"name": "Artifact",
				"description": "Writes files with <tags> & &amp; entities",
				"input_schema": {
					"type": "object",
					"properties": {
						"content": {"type": "string", "pattern": "^[^\\0]*$"}
					}
				}
			}
		],
		"messages": [{"role": "user", "content": "hello <world> & fun"}]
	}`)
	wantFixedBody := []byte(`{
		"b_key": "val",
		"a_key": "val",
		"tools": [
			{
				"name": "Artifact",
				"description": "Writes files with <tags> & &amp; entities",
				"input_schema": {
					"type": "object",
					"properties": {
						"content": {"type": "string", "pattern": "^[^\\u0000]*$"}
					}
				}
			}
		],
		"messages": [{"role": "user", "content": "hello <world> & fun"}]
	}`)

	gotFixed := deepseekToolPatterns(p, provider.Anthropic, fixedBodyInput)
	if !bytes.Equal(gotFixed, wantFixedBody) {
		t.Fatalf("fixed body differed by more than the pattern:\ngot : %s\nwant: %s", gotFixed, wantFixedBody)
	}
}

func TestDeepSeekToolPatternsResponsesAndInvalidBodies(t *testing.T) {
	input := []byte(`{"id":9007199254740993,"tools":[{"type":"function","name":"Artifact","parameters":{"$defs":{"content":{"pattern":"^[^\\0]*$"}},"properties":{"value":{"items":{"pattern":"\\0"}}}}}]}`)
	p := provider.Provider{Preset: "deepseek"}
	out := deepseekToolPatterns(p, provider.Anthropic, input)
	if bytes.Contains(out, []byte(`\\0`)) || !bytes.Contains(out, []byte(`9007199254740993`)) || !bytes.Contains(out, []byte(`\\u0000`)) {
		t.Fatalf("flattened schema or number not preserved: %s", out)
	}
	for _, in := range [][]byte{[]byte(`{"tools":`), []byte(`null`), []byte(`{"tools":[null]}`)} {
		if !bytes.Equal(deepseekToolPatterns(p, provider.Anthropic, in), in) {
			t.Fatal("invalid or unchanged request modified")
		}
	}
}

func TestDeepSeekForwardNormalizesArtifact(t *testing.T) {
	var captured []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer up.Close()
	body := []byte(`{"tools":[{"name":"Artifact","input_schema":{"pattern":"^[^\\0]*$"}}]}`)

	cases := []struct {
		name     string
		p        provider.Provider
		wantNorm bool
	}{
		{"preset deepseek", provider.Provider{Preset: "deepseek", Anthropic: up.URL, Key: "test"}, true},
		{"custom deepseek host", provider.Provider{Anthropic: "https://api.deepseek.com", Key: "test"}, true},
		{"other provider", provider.Provider{Preset: "anthropic", Anthropic: up.URL, Key: "test"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := deepseekToolPatterns(c.p, provider.Anthropic, body)
			if c.wantNorm && !bytes.Contains(res, []byte(`\\u0000`)) {
				t.Fatalf("%s should be normalized: %s", c.name, res)
			}
			if !c.wantNorm && !bytes.Equal(res, body) {
				t.Fatalf("%s should be untouched: %s", c.name, res)
			}
		})
	}

	// End-to-end forward test via Server.forwardOnce
	s := New()
	p := provider.Provider{Preset: "deepseek", Anthropic: up.URL, Key: "test"}
	resp, err := s.forwardOnce(context.Background(), p, provider.Anthropic, "/v1/messages", body, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !bytes.Contains(captured, []byte(`\\u0000`)) {
		t.Fatalf("forwardOnce did not normalize pattern: %s", captured)
	}
}

func TestDeepSeekToolPatternsSpecialPropertyNames(t *testing.T) {
	p := provider.Provider{Preset: "deepseek"}
	// Test special property names like a\b, :x, @prefix, |pipe
	input := []byte(`{
		"tools": [
			{
				"name": "SpecialTools",
				"input_schema": {
					"type": "object",
					"properties": {
						"a\\b": {
							"type": "string",
							"pattern": "^[^\\0]*$"
						},
						":x": {
							"type": "string",
							"pattern": "\\0"
						},
						"@special": {
							"type": "string",
							"pattern": "\\0"
						},
						"a|b": {
							"type": "string",
							"pattern": "\\0"
						}
					}
				}
			}
		]
	}`)

	want := []byte(`{
		"tools": [
			{
				"name": "SpecialTools",
				"input_schema": {
					"type": "object",
					"properties": {
						"a\\b": {
							"type": "string",
							"pattern": "^[^\\u0000]*$"
						},
						":x": {
							"type": "string",
							"pattern": "\\u0000"
						},
						"@special": {
							"type": "string",
							"pattern": "\\u0000"
						},
						"a|b": {
							"type": "string",
							"pattern": "\\u0000"
						}
					}
				}
			}
		]
	}`)

	got := deepseekToolPatterns(p, provider.Anthropic, input)
	if !bytes.Equal(got, want) {
		t.Fatalf("special property names not preserved byte-for-byte:\ngot : %s\nwant: %s", got, want)
	}
}

func TestDeepSeekToolPatternsPreservesCallerSpareCapacity(t *testing.T) {
	p := provider.Provider{Preset: "deepseek"}
	src := []byte(`{"tools":[{"name":"Artifact","input_schema":{"pattern":"^[^\\0]*$"}}]}`)

	// Allocate a slice with extra capacity, typical of io.ReadAll or append
	body := make([]byte, len(src), len(src)+4096)
	copy(body, src)

	out := deepseekToolPatterns(p, provider.Anthropic, body)
	if bytes.Equal(body, out) {
		t.Fatal("expected body and out to differ")
	}
	if !bytes.Equal(body, src) {
		t.Fatalf("caller's body slice was mutated in-place:\ngot : %s\nwant: %s", body, src)
	}
	if !bytes.Contains(out, []byte(`\\u0000`)) {
		t.Fatalf("output does not contain normalized pattern: %s", out)
	}
}
