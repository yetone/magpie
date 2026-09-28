package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseResponsesAllowedTools(t *testing.T) {
	cases := []struct {
		name   string
		choice any
		want   []string
		mode   string
		web    bool
	}{
		{"required subset", map[string]any{"type": "allowed_tools", "mode": "required", "tools": []any{map[string]any{"type": "function", "name": "safeA"}, map[string]any{"type": "function", "name": "safeB"}}}, []string{"safeA", "safeB"}, "required", false},
		{"auto one", map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{map[string]any{"type": "function", "name": "safeB"}}}, []string{"safeB"}, "auto", false},
		{"required empty", map[string]any{"type": "allowed_tools", "mode": "required", "tools": []any{}}, nil, "required", false},
		{"allowed web search", map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{map[string]any{"type": "web_search_preview"}}}, nil, "auto", true},
		{"string control", "required", []string{"safeA", "forbidden", "safeB"}, "required", true},
		{"named control", map[string]any{"type": "function", "name": "safeA"}, []string{"safeA", "forbidden", "safeB"}, "name:safeA", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model": "m", "input": "hello",
				"tools": []any{
					map[string]any{"type": "function", "name": "safeA"},
					map[string]any{"type": "function", "name": "forbidden"},
					map[string]any{"type": "function", "name": "safeB"},
					map[string]any{"type": "web_search_preview"},
				},
				"tool_choice": tc.choice,
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := parseResponses(body)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range r.Tools {
				names = append(names, tool.Name)
			}
			if !reflect.DeepEqual(names, tc.want) || r.ToolChoice != tc.mode || r.WebSearch != tc.web {
				t.Fatalf("tools = %v, choice = %q, web = %v; want %v, %q, %v", names, r.ToolChoice, r.WebSearch, tc.want, tc.mode, tc.web)
			}
			for _, path := range []struct {
				name string
				body []byte
			}{
				{"chat", buildChat(r, "upstream", "example.com", false)},
				{"responses", buildResponses(r, "upstream", "example.com", false)},
				{"anthropic", buildAnthropic(r, "upstream")},
				{"codeassist", buildCodeAssist(r, "upstream", "gemini")},
			} {
				t.Run(path.name, func(t *testing.T) {
					var raw map[string]any
					if err := json.Unmarshal(path.body, &raw); err != nil {
						t.Fatal(err)
					}
					if nested, ok := raw["request"].(map[string]any); ok {
						raw = nested
					}
					var forwarded []string
					if tools, ok := raw["tools"].([]any); ok {
						for _, item := range tools {
							tool := item.(map[string]any)
							if decls, ok := tool["functionDeclarations"].([]any); ok {
								for _, d := range decls {
									forwarded = append(forwarded, d.(map[string]any)["name"].(string))
								}
							} else if f, ok := tool["function"].(map[string]any); ok {
								forwarded = append(forwarded, f["name"].(string))
							} else if tool["input_schema"] != nil || tool["type"] == "function" {
								forwarded = append(forwarded, tool["name"].(string))
							}
						}
					}
					want := tc.want
					if path.name == "codeassist" && tc.mode == "none" {
						want = nil
					}
					if !reflect.DeepEqual(forwarded, want) {
						t.Fatalf("forwarded = %v, want %v in %s", forwarded, want, path.body)
					}
				})
			}
		})
	}
}

func TestParseResponsesAllowedNamespacedTool(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi",` + namespacedTools + `,"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","namespace":"collaboration","name":"spawn_agent"}]}}`)
	r, err := parseResponses(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 1 || r.Tools[0].Name != "collaboration__spawn_agent" || len(r.Namespaced) != 1 {
		t.Fatalf("tools = %v, namespaced = %v", r.Tools, r.Namespaced)
	}
}
