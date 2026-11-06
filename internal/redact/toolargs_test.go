package redact

import (
	"strings"
	"testing"
)

// Whatever the model wrote for a tool to run is the tool's arguments, in
// every block or item type a protocol has for a tool call: the
// protocol's key names (data, url, *_id) keep nothing there. An
// mcp_tool_use's input was not counted as arguments, so a phone under
// "data", a card under "customer_id" and an Anthropic key in a "url" went
// out as written.
func TestEveryToolCallsArgumentsAreMasked(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	args := `{"data":"call 13800138000","url":"https://hooks.acme.io/?k=` + key + `","customer_id":"4111 1111 1111 1111","signature":"mail rina.k@fastmail.com"}`
	gone := []string{"13800138000", key, "4111 1111 1111 1111", "rina.k@fastmail.com"}
	anthropic := func(typ string) string {
		return `{"messages":[{"role":"assistant","content":[{"type":"` + typ + `","id":"toolu_1","name":"send","server_name":"acme","input":` + args + `}]}]}`
	}
	responses := func(typ, field string) string {
		return `{"input":[{"type":"` + typ + `","call_id":"call_1","name":"send","` + field + `":` + args + `}]}`
	}
	cases := map[string]string{
		"chat tool_calls":      `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"send","arguments":` + args + `}}]}]}`,
		"chat function_call":   `{"messages":[{"role":"assistant","function_call":{"name":"send","arguments":` + args + `}}]}`,
		"gemini functionCall":  `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"send","args":` + args + `}}]}]}`,
		"gemini function_call": `{"contents":[{"role":"model","parts":[{"function_call":{"name":"send","args":` + args + `}}]}]}`,
	}
	for _, typ := range []string{"tool_use", "server_tool_use", "mcp_tool_use"} {
		cases["anthropic "+typ] = anthropic(typ)
	}
	for typ, field := range map[string]string{
		"function_call": "arguments", "mcp_call": "arguments", "tool_search_call": "arguments",
		"custom_tool_call": "input", "local_shell_call": "action", "shell_call": "action",
		"computer_call": "action", "apply_patch_call": "operation",
	} {
		cases["responses "+typ] = responses(typ, field)
	}
	for name, body := range cases {
		out, _ := MaskJSON([]byte(body), o)
		for _, g := range gone {
			if strings.Contains(string(out), g) {
				t.Errorf("%s: %q went out: %s", name, g, out)
			}
		}
		for _, k := range []string{`"call_1"`, `"toolu_1"`, `"name":"send"`, `"server_name":"acme"`} {
			if strings.Contains(body, k) && !strings.Contains(string(out), k) {
				t.Errorf("%s: %s changed: %s", name, k, out)
			}
		}
	}
}
