package gateway

import (
	"github.com/yetone/magpie/internal/provider"
	"testing"

	"github.com/tidwall/gjson"
)

// displayItemsAsk is a Claude Code request with a tool whose display
// property is an untyped array of choices: items, with the choices as a
// string enum beside them. ferock on Discord sent such a request through
// a routing group of a remote magpie's Antigravity Gemini, and Google
// turned it away: 400 "GenerateContentRequest.tools[0].
// function_declarations[19].parameters.properties[display].items: field
// predicate failed: $type == Type.ARRAY". The tool's literal schema
// wasn't posted; this is the shape that error and the update that broke it
// (geminiToolTypes, which typed such a node from its enum) point to.
const displayItemsAsk = `{"model":"group/gemini-3-8-flash","max_tokens":32,"messages":[{"role":"user","content":"hi"}],
 "tools":[{"name":"show","description":"show rows","input_schema":{"type":"object","properties":{
   "display":{"description":"columns to display","items":{"type":"string"},"enum":["name","size","date"]},
   "filter":{"description":"a filter","properties":{"q":{"type":"string"}},"const":"x"},
   "limit":{"type":"integer"}},"required":["display"]}}]}`

// A Gemini model's untyped schema that has items is an array and one
// that has properties an object, whatever its enum or const says: typed
// from its values, the node went on with items on a string.
func TestGeminiToolTypesItemsBeforeEnum(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	out := geminiToolTypes([]byte(displayItemsAsk))
	props := gjson.GetBytes(out, "tools.0.input_schema.properties")
	if got := props.Get("display.type").String(); got != "array" {
		t.Errorf("display typed %q, want array: %s", got, props.Get("display").Raw)
	}
	if got := props.Get("filter.type").String(); got != "object" {
		t.Errorf("filter typed %q, want object: %s", got, props.Get("filter").Raw)
	}
	// an enum with no items or properties is still typed from its values
	enum := `{"model":"gemini-3.8-flash","tools":[{"name":"a","input_schema":{"type":"object","properties":{"m":{"enum":["a","b"]}}}}]}`
	if got := gjson.GetBytes(geminiToolTypes([]byte(enum)), "tools.0.input_schema.properties.m.type").String(); got != "string" {
		t.Errorf("enum typed %q, want string", got)
	}
}

// Every node Antigravity is sent has items only as an array and
// properties only as an object, on each route: the tool as Claude Code
// sent it, as this version's local magpie passes it on, and as a magpie
// from before passed it on (display typed "string").
func TestAntigravityItemsOnlyOnArrays(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	older := `{"model":"group/gemini-3-8-flash","max_tokens":32,"messages":[{"role":"user","content":"hi"}],
 "tools":[{"name":"show","description":"show rows","input_schema":{"type":"object","properties":{
   "display":{"type":"string","description":"columns to display","items":{"type":"string"},"enum":["name","size","date"]},
   "listed":{"type":["string","array","null"],"items":{"type":"string"}},
   "shaped":{"type":["string","object"],"properties":{"q":{"type":"string"}}},
   "word":{"type":"string","minItems":1,"properties":{"q":{"type":"string"}},"required":["q"]}}}}]}`
	for name, body := range map[string]string{
		"as sent":     displayItemsAsk,
		"local typed": string(geminiToolTypes([]byte(displayItemsAsk))),
		"older local": older,
	} {
		r, err := parseAnthropic([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		decls := gjson.ParseBytes(buildCodeAssist(r, "gemini-3.8-flash", "antigravity")).Get("request.tools.0.functionDeclarations")
		if !decls.IsArray() || len(decls.Array()) == 0 {
			t.Fatalf("%s: no declarations", name)
		}
		var walk func(path string, s gjson.Result)
		walk = func(path string, s gjson.Result) {
			typ := s.Get("type").String()
			for kw, want := range map[string]string{"items": "array", "minItems": "array", "properties": "object", "required": "object"} {
				if s.Get(kw).Exists() && typ != want {
					t.Errorf("%s: %s has %s on type %q: %s", name, path, kw, typ, s.Raw)
				}
			}
			s.Get("properties").ForEach(func(k, v gjson.Result) bool {
				walk(path+".properties."+k.String(), v)
				return true
			})
			if it := s.Get("items"); it.IsObject() {
				walk(path+".items", it)
			}
		}
		walk("parameters", decls.Get("0.parameters"))
		if name == "older local" {
			p := decls.Get("0.parameters.properties")
			if got := p.Get("listed.type").String(); got != "array" || !p.Get("listed.items").Exists() || !p.Get("listed.nullable").Bool() {
				t.Errorf("listed: %s", p.Get("listed").Raw)
			}
			if got := p.Get("shaped.type").String(); got != "object" || !p.Get("shaped.properties.q").Exists() {
				t.Errorf("shaped: %s", p.Get("shaped").Raw)
			}
			if got := p.Get("display.enum").Raw; got != `["name","size","date"]` {
				t.Errorf("display's enum: %s", p.Get("display").Raw)
			}
			continue
		}
		d := decls.Get("0.parameters.properties.display")
		if d.Get("type").String() != "array" || d.Get("items.type").String() != "string" {
			t.Errorf("%s: display %s", name, d.Raw)
		}
	}
}
