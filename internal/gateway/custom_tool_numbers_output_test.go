package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The common encoder and renderer keep custom input intact before Responses
// turns it back into a custom call, including when a model missed an escape.
func TestCustomToolNumbersReachResponses(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
	}{
		{"unescaped quotes", `{"input":"x = "2.0"; v = "1.0.2"; n = 3.0"}`, `{"input":"x = "2.0"; v = "1.0.2"; n = 3.0"}`},
		{"one quote", `{"input":"echo "release 1.0 ready"}`, `{"input":"echo "release 1.0 ready"}`},
		{"escaped quotes", `{"input":"x = \"2.0\"; v = \"1.0.2\"; n = 3.0"}`, `x = "2.0"; v = "1.0.2"; n = 3.0`},
	} {
		for _, namespace := range []string{"", "functions"} {
			t.Run(tc.name+"/"+namespace, func(t *testing.T) {
				customName, functionName := "exec", "wait"
				tools := `[{"type":"custom","name":"exec"},{"type":"function","name":"wait","parameters":{"type":"object"}}]`
				if namespace != "" {
					tools = `[{"type":"namespace","name":"functions","tools":` + tools + `}]`
					customName, functionName = "functions__exec", "functions__wait"
				}
				req, err := parseResponses([]byte(`{"model":"fake/m1","input":"run it","tools":` + tools + `}`))
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				enc := encoder(provider.Responses, newSSEWriter(rec), req)
				enc.event(Event{Kind: KToolStart, ID: "custom", Name: customName})
				for start := 0; start < len(tc.args); start += 3 {
					enc.event(Event{Kind: KToolArgs, Text: tc.args[start:min(start+3, len(tc.args))]})
				}
				enc.event(Event{Kind: KToolStart, ID: "function", Name: functionName})
				enc.event(Event{Kind: KToolArgs, Text: `{"ms":2500.0}`})
				enc.finish()
				var streamed []map[string]any
				for _, ev := range events(rec.Body.String()) {
					if ev["type"] == "response.output_item.done" {
						streamed = append(streamed, ev["item"].(map[string]any))
					}
				}
				res := Result{Parts: []Part{
					{Kind: ToolCall, ID: "custom", Name: customName, Args: json.RawMessage(tc.args)},
					{Kind: ToolCall, ID: "function", Name: functionName, Args: json.RawMessage(`{"ms":2500.0}`)},
				}}
				var whole struct {
					Output []map[string]any `json:"output"`
				}
				body := render(provider.Responses, res, req)
				if err := json.Unmarshal(body, &whole); err != nil {
					t.Fatalf("render: %v: %s", err, body)
				}
				for name, items := range map[string][]map[string]any{"stream": streamed, "whole": whole.Output} {
					if len(items) != 2 {
						t.Fatalf("%s: got %d items: %+v", name, len(items), items)
					}
					if custom := items[0]; custom["type"] != "custom_tool_call" || custom["input"] != tc.want || custom["name"] != "exec" {
						t.Errorf("%s custom input changed: %+v; want %q", name, custom, tc.want)
					}
					if function := items[1]; function["type"] != "function_call" || function["arguments"] != `{"ms":2500}` {
						t.Errorf("%s function lost integer compatibility: %+v", name, function)
					}
				}
				if namespace != "" && !strings.Contains(rec.Body.String(), `"namespace":"functions"`) {
					t.Error("custom call lost its namespace")
				}
			})
		}
	}
}
