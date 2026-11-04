package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// Antigravity refuses an enum with a blank value ("enum[3]: cannot be
// empty", #5235): Portainer's StatusMessage.operationStatus, as the
// reporter's dsh session recorded it, under allOf, goes without its "".
// An enum of blanks alone, and a blank const, go without their enum; a
// number in an enum still drops it whole.
func TestPlainSchemaDropsBlankEnumValues(t *testing.T) {
	in := `{"type":"object","properties":{"StatusMessage":{"type":"object","properties":{"operationStatus":{"allOf":[{"enum":["processing","warning","error",""],"type":"string","x-enum-varnames":["EndpointOperationStatusProcessing","EndpointOperationStatusWarning","EndpointOperationStatusError","EndpointOperationStatusDone"]}],"description":"'', 'error', 'processing'","example":"error"}}},` +
		`"blank":{"type":"string","enum":[""," "]},"c":{"type":"string","const":""},"n":{"enum":["a",1,""]}}}`
	var got struct {
		Properties map[string]struct {
			Enum       []any `json:"enum"`
			Properties map[string]struct {
				Type string `json:"type"`
				Enum []any  `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
	}
	out := plainSchema(json.RawMessage(in))
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	op := got.Properties["StatusMessage"].Properties["operationStatus"]
	if op.Type != "string" || len(op.Enum) != 3 || op.Enum[0] != "processing" || op.Enum[2] != "error" {
		t.Fatalf("operationStatus: %+v in %s", op, out)
	}
	for _, k := range []string{"blank", "c", "n"} {
		if e := got.Properties[k].Enum; e != nil {
			t.Fatalf("%s kept enum %v: %s", k, e, out)
		}
	}
	if strings.Contains(string(out), `""`) {
		t.Fatalf("a blank value went: %s", out)
	}
}
