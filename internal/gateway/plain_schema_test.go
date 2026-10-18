package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A $ref that points inside properties ("#/properties/…"), as dsh's tool
// parameters carry one, is a local JSON pointer like any other: resolved
// where it names, not written out as a bare object (#4561), and a
// self-reference stops at the target's type alone.
func TestPlainSchemaResolvesPointerIntoProperties(t *testing.T) {
	in := `{"type":"object",
	"properties":{
		"func":{"type":"object","properties":{
			"name":{"type":"string"},
			"triggers":{"type":"array","items":{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["command","prompt"]},
					"self":{"$ref":"#/properties/func/properties/triggers/items"}
				},
				"required":["kind"]
			}}
		}},
		"again":{"$ref":"#/properties/func/properties/triggers/items"}
	}}`
	want := `{"type":"object",
	"properties":{
		"func":{"type":"object","properties":{
			"name":{"type":"string"},
			"triggers":{"type":"array","items":{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["command","prompt"]},
					"self":{"type":"object"}
				},
				"required":["kind"]
			}}
		}},
		"again":{"type":"object","properties":{
			"kind":{"type":"string","enum":["command","prompt"]},
			"self":{"type":"object"}
		},"required":["kind"]}
	}}`
	var got, exp any
	if err := json.Unmarshal(plainSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		b, _ := json.Marshal(got)
		t.Errorf("got %s", b)
	}
}

// A self-reference is judged by whole pointer segments: a ref to
// "#/properties/a" inside /properties/ab is a ref to a sibling, resolved
// whole, not stopped at its type. And a pointer that names nothing where
// it points still finds its last segment among $defs, as it did before
// pointers were walked (#4561 review).
func TestPlainSchemaSelfRefBySegment(t *testing.T) {
	in := `{"type":"object",
	"$defs":{"Mode":{"type":"string","enum":["x","y"]}},
	"properties":{
		"a":{"type":"object","properties":{"n":{"type":"integer"}}},
		"ab":{"type":"object","properties":{
			"inner":{"$ref":"#/properties/a"},
			"mode":{"$ref":"#/components/schemas/Mode"}
		}}
	}}`
	want := `{"type":"object",
	"properties":{
		"a":{"type":"object","properties":{"n":{"type":"integer"}}},
		"ab":{"type":"object","properties":{
			"inner":{"type":"object","properties":{"n":{"type":"integer"}}},
			"mode":{"type":"string","enum":["x","y"]}
		}}
	}}`
	var got, exp any
	if err := json.Unmarshal(plainSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		b, _ := json.Marshal(got)
		t.Errorf("got %s", b)
	}
}

// Antigravity refuses a function declaration over any keyword outside its
// schema subset (#187: exclusiveMinimum from zcode's and dsh's tools).
func TestPlainSchemaKeepsOnlyWhatAntigravityTakes(t *testing.T) {
	in := `{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":false,
	"properties":{
		"limit":{"type":"integer","exclusiveMinimum":0,"maximum":100,"description":"how many"},
		"offset":{"type":"integer","exclusiveMinimum":0,"minimum":1},
		"ratio":{"type":"number","exclusiveMaximum":1},
		"url":{"type":"string","format":"uri","title":"URL","minLength":1,"pattern":"^https?://"},
		"tags":{"type":"array","items":{"type":"string","uniqueItems":true},"minItems":1,"uniqueItems":true,"contains":{"type":"string"}},
		"opts":{"type":"object","properties":{"deep":{"type":["boolean","null"],"default":false}},"minProperties":1,"dependentRequired":{"a":["b"]},"not":{"type":"null"}}
	},"required":["limit","gone"]}`
	want := `{"type":"object",
	"properties":{
		"limit":{"type":"integer","minimum":0,"maximum":100,"description":"how many"},
		"offset":{"type":"integer","minimum":1},
		"ratio":{"type":"number","maximum":1},
		"url":{"type":"string","minLength":1,"pattern":"^https?://"},
		"tags":{"type":"array","items":{"type":"string"},"minItems":1},
		"opts":{"type":"object","properties":{"deep":{"type":"boolean","nullable":true}},"minProperties":1}
	},"required":["limit"]}`
	var got, exp any
	if err := json.Unmarshal(plainSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		b, _ := json.Marshal(got)
		t.Errorf("got %s", b)
	}
}

// Google refuses a whole request over an array schema that says no items,
// at any depth ("...properties[where].items.items: missing field", #2826),
// and takes an items that is one object, not a tuple. Claude Code's tools
// (ArtifactData's query.where) say an array of arrays in JSON Schema
// 2020-12 tuples (prefixItems), outside what Antigravity's subset takes;
// draft 07 says a tuple as an items array. Members alike are said once;
// members that differ are any item, as one member's schema would tell the
// model every item is that kind.
func TestPlainSchemaArrayAlwaysHasItems(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"a tuple whose members differ is any item", `{"type":"object","properties":{"query":{"type":"object","properties":{"where":{"type":"array","maxItems":10,
			"items":{"type":"array","prefixItems":[{"type":"string"},{"type":"string","enum":["eq","ne","in"]},{}]}}}}},"required":["query"]}`,
			`{"type":"object","properties":{"query":{"type":"object","properties":{"where":{"type":"array","maxItems":10,
			"items":{"type":"array","items":{}}}}}},"required":["query"]}`},
		{"a prefixItems tuple of members alike is said once", `{"type":"array","prefixItems":[{"type":"number","description":"x"},{},{"type":"number","description":"x"}]}`,
			`{"type":"array","items":{"type":"number","description":"x"}}`},
		{"an array that never said items", `{"type":"array","maxItems":3}`,
			`{"type":"array","maxItems":3,"items":{}}`},
		{"a draft 07 tuple of members that differ is one object", `{"type":"array","items":[{"type":"number"},{"type":"boolean"},{}]}`,
			`{"type":"array","items":{}}`},
		{"a draft 07 tuple of members alike is said once", `{"type":"array","items":[{"type":"string"},{"type":"string"}]}`,
			`{"type":"array","items":{"type":"string"}}`},
		{"a tuple of nothing gets the default", `{"type":"array","prefixItems":[{},{}]}`,
			`{"type":"array","items":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got, exp any
			if err := json.Unmarshal(plainSchema(json.RawMessage(tc.in)), &got); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal([]byte(tc.want), &exp)
			if !reflect.DeepEqual(got, exp) {
				b, _ := json.Marshal(got)
				t.Errorf("got %s", b)
			}
		})
	}
}
