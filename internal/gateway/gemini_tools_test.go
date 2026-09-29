package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseGeminiAllowedFunctionNames(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		allowed []string
		want    []string
		choice  string
	}{
		{"any subset", "ANY", []string{"safeA", "safeB"}, []string{"safeA", "safeB"}, "required"},
		{"any one", "ANY", []string{"safeB"}, []string{"safeB"}, "name:safeB"},
		{"any all", "ANY", nil, []string{"safeA", "forbidden", "safeB"}, "required"},
		{"validated subset", "VALIDATED", []string{"safeA", "safeB"}, []string{"safeA", "safeB"}, "auto"},
		{"validated one", "VALIDATED", []string{"safeB"}, []string{"safeB"}, "auto"},
		{"validated no list", "VALIDATED", nil, []string{"safeA", "forbidden", "safeB"}, "auto"},
		{"auto ignores list", "AUTO", []string{"safeA"}, []string{"safeA", "forbidden", "safeB"}, "auto"},
		{"none ignores list", "NONE", []string{"safeA"}, []string{"safeA", "forbidden", "safeB"}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model": "gemini-2.5-pro",
				"tools": []any{
					map[string]any{"functionDeclarations": []any{
						map[string]any{"name": "safeA", "parameters": map[string]any{"type": "object"}},
						map[string]any{"name": "forbidden", "parameters": map[string]any{"type": "object"}},
					}},
					map[string]any{"functionDeclarations": []any{map[string]any{"name": "safeB"}}},
				},
				"toolConfig": map[string]any{"functionCallingConfig": map[string]any{
					"mode": tc.mode, "allowedFunctionNames": tc.allowed,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := parseGemini(body)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tool := range r.Tools {
				got = append(got, tool.Name)
			}
			if !reflect.DeepEqual(got, tc.want) || r.ToolChoice != tc.choice {
				t.Fatalf("tools = %v, choice = %q; want %v, %q", got, r.ToolChoice, tc.want, tc.choice)
			}
			// Every upstream renderer must see only the allowed declarations.
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
					if request, ok := raw["request"].(map[string]any); ok {
						raw = request
					}
					var names []string
					if tools, ok := raw["tools"].([]any); ok {
						for _, item := range tools {
							tool := item.(map[string]any)
							if decls, ok := tool["functionDeclarations"].([]any); ok {
								for _, d := range decls {
									names = append(names, d.(map[string]any)["name"].(string))
								}
							} else if function, ok := tool["function"].(map[string]any); ok {
								names = append(names, function["name"].(string))
							} else if name, ok := tool["name"].(string); ok {
								names = append(names, name)
							}
						}
					}
					want := tc.want
					if path.name == "codeassist" && tc.choice == "none" {
						want = nil
					}
					if !reflect.DeepEqual(names, want) {
						t.Fatalf("forwarded tools = %v; want %v in %s", names, want, path.body)
					}
				})
			}

		})
	}
}
