package gateway

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini schema's keywords are read where a schema has them, never as
// the names of an object's properties: a property named example, nullable
// or propertyOrdering is kept, and one named type has its own type
// lowercased like any other. nullable: true is said as JSON Schema says
// it, the type with "null" beside it (an anyOf's null branch where there
// is no type), not as a made-up x-nullable. An enum is kept as it came,
// as Google's own SDK says a nullable enum (Schema.json_schema), and a
// schema of no type, which takes null already, is left as it is. The same
// schema reaches a Chat upstream as a function's parameters and as the
// structured output's.
func TestGeminiSchemaKeywordsNotPropertyNames(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"{}"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	const schema = `{"type":"OBJECT","properties":{
		"example":{"type":"STRING","example":"e.g. this"},
		"nullable":{"type":"BOOLEAN"},
		"propertyOrdering":{"type":"STRING"},
		"type":{"type":"STRING","enum":["a","b"]},
		"note":{"type":"STRING","nullable":true},
		"list":{"type":"ARRAY","items":{"type":"INTEGER"},"nullable":true},
		"either":{"anyOf":[{"type":"STRING"},{"type":"INTEGER"}],"nullable":true},
		"plain":{"type":"STRING","nullable":false},
		"none":{"type":"NULL","nullable":true},
		"pick":{"type":"STRING","enum":["a","b"],"nullable":true},
		"loose":{"description":"anything","nullable":true}},
		"required":["type"],
		"propertyOrdering":["type","example","nullable","propertyOrdering","note","list","either","plain"]}`
	const want = `{"type":"object","properties":{
		"example":{"type":"string"},
		"nullable":{"type":"boolean"},
		"propertyOrdering":{"type":"string"},
		"type":{"type":"string","enum":["a","b"]},
		"note":{"type":["string","null"]},
		"list":{"type":["array","null"],"items":{"type":"integer"}},
		"either":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]},
		"plain":{"type":"string"},
		"none":{"type":"null"},
		"pick":{"type":["string","null"],"enum":["a","b"]},
		"loose":{"description":"anything"}},
		"required":["type"]}`
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"fill it"}]}],
		"tools":[{"functionDeclarations":[{"name":"fill","parameters":`+schema+`}]}],
		"generationConfig":{"responseMimeType":"application/json","responseSchema":`+schema+`}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{"tools.0.function.parameters", "response_format.json_schema.schema"} {
		var got any
		if err := json.Unmarshal([]byte(gjson.GetBytes(f.got, at).Raw), &got); err != nil || !reflect.DeepEqual(got, w) {
			t.Errorf("%s sent upstream:\n%s\nwant\n%s", at, gjson.GetBytes(f.got, at).Raw, want)
		}
	}
	// responseJsonSchema's $defs, draft 7's definitions and
	// patternProperties are schemas by name too
	const defs = `{"type":"object","properties":{"e":{"$ref":"#/$defs/example"}},"$defs":{"example":{"type":"string"},"nullable":{"type":"integer"}},` +
		`"definitions":{"example":{"type":"string"}},"patternProperties":{"nullable":{"type":"boolean"}}}`
	if code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"fill it"}]}],
		"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":`+defs+`}}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var got, wantDefs any
	json.Unmarshal([]byte(defs), &wantDefs)
	if sent := gjson.GetBytes(f.got, "response_format.json_schema.schema").Raw; json.Unmarshal([]byte(sent), &got) != nil || !reflect.DeepEqual(got, wantDefs) {
		t.Errorf("responseJsonSchema sent upstream:\n%s\nwant\n%s", sent, defs)
	}
}
