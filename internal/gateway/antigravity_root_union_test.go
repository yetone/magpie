package gateway

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/provider"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

// rgSchema is Philo's rg tool as #4046 (imuxin) gave it: an object root
// with its properties and a sibling oneOf that only says which of them
// may be given together.
const rgSchema = `{"type":"object","properties":{"pattern":{"type":"string"},"cursor":{"type":"string"}},"oneOf":[{"required":["pattern"],"not":{"required":["cursor"]}},{"required":["cursor"],"not":{"required":["pattern"]}}]}`

// #4046's own reproduction: the oneOf's first branch, which has no type,
// stood in for the whole root, so the tool went to Antigravity as
// {"type":"string"} and Claude there answered 400 "tools.4.custom.
// input_schema.type: Input should be 'object'".
func TestAntigravityRootOneOfKeepsObject(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	got := plainSchema(json.RawMessage(rgSchema))
	var parameters map[string]any
	if err := json.Unmarshal(got, &parameters); err != nil {
		t.Fatal(err)
	}
	props, _ := parameters["properties"].(map[string]any)
	if parameters["type"] != "object" || props["pattern"] == nil || props["cursor"] == nil {
		t.Fatalf("got %s; want an object retaining pattern and cursor", got)
	}
}

// The same tool through the route the reporter took: an OpenAI Chat
// declaration, parseChat, then the Antigravity body.
func TestAntigravityChatToolRootOneOfKeepsObject(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	r, err := parseChat([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"rg","description":"search","parameters":` + rgSchema + `}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ps := gjson.ParseBytes(buildCodeAssist(r, "claude-sonnet-4-5", "antigravity")).Get("request.tools.0.functionDeclarations.0.parameters")
	want := `{"type":"object","properties":{"pattern":{"type":"string"},"cursor":{"type":"string"}}}`
	var got, exp any
	json.Unmarshal([]byte(ps.Raw), &got)
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("parameters %s", ps.Raw)
	}
}

// A schema with its own type or properties keeps them beside any union,
// at the root or under a property, whichever of anyOf, oneOf or allOf it
// is. Fields only a branch declares join the object's; a field is
// required when every branch that isn't null requires it, or an allOf
// member does; a null branch makes it nullable. A union with nothing of
// its own beside it is still read as its first branch that isn't null.
func TestPlainSchemaUnionKeepsOwnShape(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, c := range []struct{ name, in, want string }{
		{"root oneOf", rgSchema,
			`{"type":"object","properties":{"pattern":{"type":"string"},"cursor":{"type":"string"}}}`},
		{"root anyOf", `{"type":"object","properties":{"a":{"type":"string"}},"anyOf":[{"required":["a"]},{"not":{"required":["a"]}}]}`,
			`{"type":"object","properties":{"a":{"type":"string"}}}`},
		{"properties only, no type", `{"properties":{"a":{"type":"string"}},"oneOf":[{"required":["a"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
		{"nested oneOf", `{"type":"object","properties":{"q":{"type":"object","description":"query","properties":{"pattern":{"type":"string"},"cursor":{"type":"string"}},"oneOf":[{"required":["pattern"],"not":{"required":["cursor"]}},{"required":["cursor"],"not":{"required":["pattern"]}}]}},"required":["q"]}`,
			`{"type":"object","properties":{"q":{"type":"object","description":"query","properties":{"pattern":{"type":"string"},"cursor":{"type":"string"}}}},"required":["q"]}`},
		{"nested anyOf", `{"type":"object","properties":{"q":{"type":"object","properties":{"a":{"type":"integer"}},"anyOf":[{"required":["a"]},{"type":"null"}]}}}`,
			`{"type":"object","properties":{"q":{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"nullable":true}}}`},
		{"branches add fields", `{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"],"oneOf":[{"properties":{"kind":{"const":"a"},"x":{"type":"string"}},"required":["kind","x"]},{"properties":{"kind":{"const":"b"},"y":{"type":"number"}},"required":["kind","y"]}]}`,
			`{"type":"object","properties":{"kind":{"type":"string"},"x":{"type":"string"},"y":{"type":"number"}},"required":["kind"]}`},
		{"every branch requires it", `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"anyOf":[{"required":["a","b"]},{"required":["a"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`},
		{"root allOf", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"allOf":[{"properties":{"b":{"type":"string"}},"required":["b"]},{"not":{"required":["c"]}}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}`},
		{"nested allOf", `{"type":"object","properties":{"q":{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"required":["a"]}]}}}`,
			`{"type":"object","properties":{"q":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}}`},
		{"allOf beside oneOf", `{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"properties":{"b":{"type":"string"}}}],"oneOf":[{"required":["a"]},{"required":["b"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`},
		{"typed scalar", `{"type":"string","description":"d","anyOf":[{"format":"date"},{"format":"uuid"}]}`,
			`{"type":"string","description":"d"}`},
		{"bare union still its first branch", `{"type":"object","properties":{"v":{"description":"value","anyOf":[{"type":"null"},{"type":"integer"},{"type":"string"}]}}}`,
			`{"type":"object","properties":{"v":{"type":"integer","description":"value","nullable":true}}}`},
	} {
		var got, exp any
		if err := json.Unmarshal(plainSchema(json.RawMessage(c.in)), &got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := json.Unmarshal([]byte(c.want), &exp); err != nil {
			t.Fatalf("%s: want: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, exp) {
			b, _ := json.Marshal(got)
			t.Errorf("%s: got %s\nwant %s", c.name, b, c.want)
		}
	}
}

// The other converter that takes a union off a tool's root,
// provider.ObjectRoot (Anthropic's objectSchema, and the fold for xAI and
// the upstreams that refuse a root union), keeps the same tool an object
// with both its fields: its constraint-only branches are passed over.
func TestObjectSchemaRootOneOfKeepsObject(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	ps := gjson.ParseBytes(objectSchema(json.RawMessage(rgSchema)))
	if ps.Get("type").String() != "object" || !ps.Get("properties.pattern").Exists() || !ps.Get("properties.cursor").Exists() || ps.Get("oneOf").Exists() || ps.Get("required").Exists() {
		t.Fatalf("got %s", ps.Raw)
	}
}
