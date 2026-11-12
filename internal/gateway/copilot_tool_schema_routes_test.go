package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// the schemas below are #3931's and #4374's literal bytes: a Pydantic v2
// Optional[str] (basic-memory's project) and Codex Desktop's
// move_thread_to_project.projectId and automation_update (a root oneOf
// through $defs whose branches hold a union themselves)
const (
	pydanticParams   = `{"type":"object","properties":{"project":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null,"description":"Project name"}}}`
	projectIDParams  = `{"type":"object","properties":{"projectId":{"anyOf":[{"type":"string"},{"type":"null"}],"description":"Destination from list_projects, or null to remove a local Codex task or ChatGPT conversation from its project."}}}`
	automationParams = `{"oneOf":[{"$ref":"#/$defs/update"},{"$ref":"#/$defs/create"}],"$defs":{
	  "update":{"type":"object","properties":{"mode":{"type":"string","enum":["update"]},"id":{"type":"string"},
	     "schedule":{"oneOf":[{"type":"object","properties":{"kind":{"type":"string","enum":["cron"]},"cron":{"type":"string"}},"required":["kind","cron"]},{"type":"object","properties":{"kind":{"type":"string","enum":["once"]},"at":{"type":"string"}},"required":["kind","at"]}]}},"required":["mode","id"]},
	  "create":{"type":"object","properties":{"mode":{"type":"string","enum":["create"]},"name":{"type":"string"},
	     "target":{"anyOf":[{"type":"object","properties":{"type":{"type":"string","enum":["thread"]}},"required":["type"]},{"type":"object","properties":{"type":{"type":"string","enum":["project"]},"id":{"type":"string"}},"required":["type","id"]}]}},"required":["mode","name"]}}}`
)

// refusesNestedUnion is Copilot's Grok: a union at a root, or a union in a
// union's branch, is a bare 400.
func refusesNestedUnion(b []byte) bool {
	var req map[string]any
	if json.Unmarshal(b, &req) != nil {
		return false
	}
	bad := false
	var walk func(v any, inUnion bool)
	walk = func(v any, inUnion bool) {
		switch x := v.(type) {
		case map[string]any:
			_, a := x["anyOf"]
			_, o := x["oneOf"]
			if (a || o) && inUnion {
				bad = true
			}
			for k, c := range x {
				walk(c, inUnion || ((a || o) && (k == "anyOf" || k == "oneOf")))
			}
		case []any:
			for _, c := range x {
				walk(c, inUnion)
			}
		}
	}
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		ps := tm["parameters"]
		if f, ok := tm["function"].(map[string]any); ok {
			ps = f["parameters"]
		}
		if pm, ok := ps.(map[string]any); ok {
			if _, a := pm["anyOf"]; a {
				bad = true
			}
			if _, o := pm["oneOf"]; o {
				bad = true
			}
			// a ref'd branch is a union inside the union when it holds one
			if d, ok := pm["$defs"].(map[string]any); ok {
				for _, b := range d {
					walk(b, false)
				}
			}
		}
		walk(ps, false)
	}
	return bad
}

func TestCopilotToolSchemasOnEveryClientRoute(t *testing.T) {
	var sent []string
	copilotTurnFake(t, map[string]string{
		"gemini-3.8-flash": `["/chat/completions"]`,
		"grok-4.7":         `["/responses"]`,
	}, func(m string, b []byte) (int, string) {
		if m == "grok-4.7" && (refusesNestedUnion(b) || strings.Contains(string(b), `"type":"namespace"`)) {
			return http.StatusBadRequest, `{"error":{"message":"Bad Request","type":"invalid_request_error"}}`
		}
		return 0, ""
	}, func(m string, b []byte) bool {
		var req map[string]any
		json.Unmarshal(b, &req)
		bad := false
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				_, a := x["anyOf"]
				_, o := x["oneOf"]
				if _, ty := x["type"]; (a || o) && ty {
					bad = true
				}
				for _, c := range x {
					walk(c)
				}
			case []any:
				for _, c := range x {
					walk(c)
				}
			}
		}
		// a tool's root is exempt: #4374's full request kept
		// automation_update's typed root union and was 200
		tools, _ := req["tools"].([]any)
		for _, tl := range tools {
			tm, _ := tl.(map[string]any)
			ps := tm["parameters"]
			if f, ok := tm["function"].(map[string]any); ok {
				ps = f["parameters"]
			}
			if pm, ok := ps.(map[string]any); ok {
				for k, c := range pm {
					if k != "type" {
						walk(c)
					}
				}
			}
		}
		return bad
	}, "invalid request body", http.StatusBadRequest, &sent)

	chat := func(model, params string) string {
		return fmt.Sprintf(`{"model":"copilot/%s","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":%s}}]}`, model, params)
	}
	msgs := func(model, params string) string {
		return fmt.Sprintf(`{"model":"copilot/%s","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","description":"d","input_schema":%s}]}`, model, params)
	}
	resp := func(model, params string) string {
		return fmt.Sprintf(`{"model":"copilot/%s","stream":true,"input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"f","description":"d","parameters":%s}]}`, model, params)
	}
	cases := []struct{ name, path, body string }{}
	for _, model := range []string{"gemini-3.8-flash", "grok-4.7"} {
		for _, params := range []struct{ n, p string }{{"pydantic", pydanticParams}, {"projectId", projectIDParams}, {"automation", automationParams}} {
			for _, route := range []struct {
				n, path string
				f       func(string, string) string
			}{{"chat", "/v1/chat/completions", chat}, {"messages", "/v1/messages", msgs}, {"responses", "/v1/responses", resp}} {
				cases = append(cases, struct{ name, path, body string }{model + "/" + params.n + "/" + route.n, route.path, route.f(model, params.p)})
			}
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
