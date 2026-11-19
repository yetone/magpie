package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func strictTool(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Each rule codexToolStrict holds a tool to, as the ChatGPT backend
// answered it on 2026-11-19 (codex_strict.go).
func TestCodexToolStrict(t *testing.T) {
	obj := func(props, req string) string {
		return `{"type":"object","additionalProperties":false,"properties":` + props + `,"required":` + req + `}`
	}
	for _, c := range []struct {
		name, tool string
		strict     bool
		params     string // "" for as sent
	}{
		{"left out", `{"type":"function","name":"f","parameters":` + obj(`{"q":{"oneOf":[{"type":"string"},{"type":"number"}]}}`, `["q"]`) + `}`, false, ""},
		{"not a bool", `{"type":"function","name":"f","strict":"yes","parameters":` + obj(`{"q":{"type":"string"}}`, `["q"]`) + `}`, false, ""},
		{"false kept", `{"type":"function","name":"f","strict":false,"parameters":{"type":"object","properties":{"q":{"allOf":[{"type":"string"}]}}}}`, false, ""},
		{"true fits", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"anyOf":[{"type":"string"},{"type":"null"}]},"n":{"type":["string","number"]}}`, `["q","n"]`) + `}`, true, ""},
		{"true, oneOf as anyOf", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"oneOf":[{"type":"string"},{"type":"number"}]},"oneOf":{"type":"string"}}`, `["q","oneOf"]`) + `}`,
			true, obj(`{"q":{"anyOf":[{"type":"string"},{"type":"number"}]},"oneOf":{"type":"string"}}`, `["q","oneOf"]`)},
		{"true, oneOf in items and $defs", `{"type":"function","name":"f","strict":true,"parameters":{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"array","items":{"oneOf":[{"$ref":"#/$defs/x"},{"type":"null"}]}}},"$defs":{"x":{"oneOf":[{"type":"integer","const":1},{"type":"integer","const":2}]}}}}`,
			true, `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"array","items":{"anyOf":[{"$ref":"#/$defs/x"},{"type":"null"}]}}},"$defs":{"x":{"anyOf":[{"type":"integer","const":1},{"type":"integer","const":2}]}}}`},
		{"true, oneOf and an optional property", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"oneOf":[{"type":"string"},{"type":"number"}]},"p":{"type":"number"}}`, `["q"]`) + `}`, false, ""},
		{"true, allOf", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"allOf":[{"type":"string"}]}}`, `["q"]`) + `}`, false, ""},
		{"true, if", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"type":"string","if":{"const":"a"}}}`, `["q"]`) + `}`, false, ""},
		{"true, not", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"not":{"type":"null"}}}`, `["q"]`) + `}`, false, ""},
		{"true, uniqueItems", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"type":"array","items":{"type":"string"},"uniqueItems":true}}`, `["q"]`) + `}`, false, ""},
		{"true, lookaround", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"type":"string","pattern":"^(?!/etc)"}}`, `["q"]`) + `}`, false, ""},
		{"true, more allowed", `{"type":"function","name":"f","strict":true,"parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}`, false, ""},
		{"true, open object inside", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"type":"object"}}`, `["q"]`) + `}`, false, ""},
		{"true, a const alone", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"oneOf":[{"const":"a"},{"const":"b"}]}}`, `["q"]`) + `}`, false, ""},
		{"true, an enum alone", `{"type":"function","name":"f","strict":true,"parameters":` + obj(`{"q":{"oneOf":[{"enum":["a"]},{"type":"number"}]}}`, `["q"]`) + `}`,
			true, obj(`{"q":{"anyOf":[{"enum":["a"]},{"type":"number"}]}}`, `["q"]`)},
		{"true, root anyOf", `{"type":"function","name":"f","strict":true,"parameters":{"anyOf":[` + obj(`{"q":{"type":"string"}}`, `["q"]`) + `]}}`, false, ""},
		{"true, no parameters", `{"type":"function","name":"f","strict":true}`, false, ""},
	} {
		tool := strictTool(t, c.tool)
		before := asJSON(tool["parameters"])
		codexStrictTool(tool)
		if tool["strict"] != c.strict {
			t.Errorf("%s: strict %v, want %v", c.name, tool["strict"], c.strict)
		}
		want := before
		if c.params != "" {
			want = asJSON(strictTool(t, c.params))
		}
		if got := asJSON(tool["parameters"]); got != want {
			t.Errorf("%s: parameters\n %s\nwant\n %s", c.name, got, want)
		}
	}

	// every place a Codex request holds function tools
	m := strictTool(t, `{"tools":[{"type":"function","name":"a"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"b"}]},{"type":"web_search"}],
	  "input":[{"type":"additional_tools","tools":[{"type":"function","name":"c"}]},{"type":"tool_search_output","tools":[{"type":"function","name":"d"}]},{"type":"message","role":"user","content":"x"}]}`)
	if !codexToolStrict(m) {
		t.Fatal("nothing changed")
	}
	got := asJSON(m)
	for _, want := range []string{`{"name":"a","strict":false,"type":"function"}`, `{"name":"b","strict":false,"type":"function"}`,
		`{"name":"c","strict":false,"type":"function"}`, `{"name":"d","strict":false,"type":"function"}`, `{"type":"web_search"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s in %s", want, got)
		}
	}
	if codexToolStrict(m) {
		t.Error("a second pass changed something")
	}
}
