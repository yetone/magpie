package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Schema keywords describe schemas, not the JSON data held by their
// default, const, enum or examples values, which may use the same names
func TestGeminiSchemaLiteralValuesStayData(t *testing.T) {
	for _, key := range []string{"default", "const", "enum", "examples"} {
		t.Run(key, func(t *testing.T) {
			const value = `{"type":"STRING","example":"literal","nullable":true,"propertyOrdering":["type"],"nested":{"type":"OBJECT"}}`
			data := value
			if key == "enum" || key == "examples" {
				data = "[" + value + "]"
			}
			raw := []byte(`{"type":"OBJECT","properties":{"type":{"type":"STRING"}},"` + key + `":` + data + `}`)
			want := []byte(`{"type":"object","properties":{"type":{"type":"string"}},"` + key + `":` + data + `}`)
			var gotValue, wantValue any
			if err := json.Unmarshal(jsonSchema(raw), &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("%s data was read as a schema: %s, want %s", key, jsonSchema(raw), want)
			}
		})
	}
}
