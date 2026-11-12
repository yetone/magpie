package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// badGeminiShape names the shape of a tool schema Copilot's Gemini turns
// away 400 (ProgCZ and tmhyt1 on X), "" for a schema it takes.
func badGeminiShape(node any, root bool) string {
	switch v := node.(type) {
	case []any:
		for _, e := range v {
			if s := badGeminiShape(e, false); s != "" {
				return s
			}
		}
	case map[string]any:
		if _, ok := v["prefixItems"]; ok {
			return "prefixItems"
		}
		if _, ok := v["enum"]; ok && v["type"] == "array" {
			return "array enum"
		}
		if root && (v["oneOf"] != nil || v["anyOf"] != nil) {
			return "root union"
		}
		if v["type"] == "object" && v["properties"] == nil {
			return "object without properties"
		}
		if alts, ok := v["anyOf"].([]any); ok {
			types := map[string]bool{}
			for _, a := range alts {
				m, _ := a.(map[string]any)
				t, _ := m["type"].(string)
				if t == "" {
					return "untyped branch"
				}
				if t != "null" {
					types[t] = true
				}
			}
			n := 0
			for _, a := range alts {
				if m, _ := a.(map[string]any); m["type"] != "null" {
					n++
				}
			}
			if len(types) == 1 && n > 1 {
				return "same type"
			}
		}
		for _, e := range v {
			if s := badGeminiShape(e, false); s != "" {
				return s
			}
		}
	}
	return ""
}

func TestCopilotGeminiTakesToolShapes(t *testing.T) {
	cases := []struct{ name, schema, want string }{
		{"object without properties", `{"type":"object","properties":{"opts":{"type":"object","description":"o"}}}`, `"opts":{"description":"o","properties":{},"type":"object"}`},
		{"object at the root without properties", `{"type":"object"}`, `"parameters":{"properties":{},"type":"object"}`},
		{"anyOf of one type beside its type", `{"type":"object","properties":{"v":{"description":"d","type":"string","anyOf":[{"type":"string"},{"type":"string"}]}}}`, `"v":{"description":"d","type":"string"}`},
		{"anyOf of one type", `{"type":"object","properties":{"v":{"anyOf":[{"type":"string","enum":["a"]},{"type":"string","enum":["b"]}]}}}`, `"v":{"enum":["a","b"],"type":"string"}`},
		{"anyOf with untyped branches", `{"type":"object","properties":{"v":{"description":"d","anyOf":[{"description":"x"},{"type":"string"}]}}}`, `"v":{"type":"string","description":"d"}`},
		{"enum on an array", `{"type":"object","properties":{"v":{"type":"array","enum":["a","b"]}}}`, `"v":{"items":{"enum":["a","b"],"type":"string"},"type":"array"}`},
		{"oneOf at the root", `{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"number"}},"required":["b"]}]}`, `{"properties":{"a":{"type":"string"},"b":{"type":"number"}},"type":"object"}`},
		{"prefixItems", `{"type":"object","properties":{"p":{"type":"array","prefixItems":[{"type":"number"},{"type":"number"}]}}}`, `"p":{"items":{"type":"number"},"type":"array"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent []string
			var refused string
			copilotTurnFake(t, map[string]string{"gemini-3.8-flash": `["/chat/completions"]`}, nil,
				func(m string, b []byte) bool {
					var body struct {
						Tools []struct {
							Function struct {
								Parameters any `json:"parameters"`
							} `json:"function"`
						} `json:"tools"`
					}
					json.Unmarshal(b, &body)
					for _, tool := range body.Tools {
						if s := badGeminiShape(tool.Function.Parameters, true); s != "" {
							refused = s
							return true
						}
					}
					return false
				}, "invalid request body", http.StatusBadRequest, &sent)
			req := `{"model":"copilot/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"t","parameters":` + c.schema + `}}]}`
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(req)))
			if rec.Code != 200 {
				t.Fatalf("%d (%s) %s", rec.Code, refused, rec.Body.String())
			}
			if len(sent) != 1 || !strings.Contains(sent[0], c.want) {
				t.Errorf("want %s in %v", c.want, sent)
			}
		})
	}
}

// A schema with none of the six shapes goes as it came, byte for byte, and
// another vendor's body is not touched.
func TestCopilotGeminiToolShapesLeavesFitSchemas(t *testing.T) {
	fit := `{"model":"gemini-3.8-flash","tools":[{"type":"function","function":{"name":"t","parameters":{"type":"object","properties":{"n":{"anyOf":[{"type":"string"},{"type":"null"}]},"u":{"oneOf":[{"type":"string"},{"type":"number"}]},"a":{"type":"array","items":{"type":"string"}}}}}}]}`
	if got := string(copilotGeminiToolShapes(provider.Provider{Account: &provider.Account{Agent: "copilot"}}, provider.Chat, []byte(fit))); got != fit {
		t.Errorf("changed a fit schema: %s", got)
	}
}

// Antigravity's cut-down schema had the same array-level enum: Google takes
// enum on a string, so it belongs to the items.
func TestAntigravitySchemaArrayEnumMovesToItems(t *testing.T) {
	var got struct {
		Properties map[string]struct {
			Enum  []string `json:"enum"`
			Items struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"items"`
		} `json:"properties"`
	}
	in := `{"type":"object","properties":{"v":{"type":"array","enum":["a","b"],"items":{"type":"string"}}}}`
	if err := json.Unmarshal(plainSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	v := got.Properties["v"]
	if len(v.Enum) != 0 || len(v.Items.Enum) != 2 {
		t.Errorf("%+v", v)
	}
}
