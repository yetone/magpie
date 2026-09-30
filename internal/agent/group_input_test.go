package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestGroupDeclaredInputsReachAgentCatalogs(t *testing.T) {
	syncHome(t)
	for _, input := range [][]string{nil, {"text"}, {"text", "image"}} {
		if err := provider.SaveGroup(provider.Group{ID: "declared", Members: []string{"relay/glm-4.6"}, Input: input}); err != nil {
			t.Fatal(err)
		}
		for _, shape := range []string{"pi", "opencode"} {
			b, _ := json.Marshal(magpieProviderJSON(shape))
			var doc map[string]any
			json.Unmarshal(b, &doc)
			var model map[string]any
			if shape == "pi" {
				for _, v := range doc["models"].([]any) {
					m := v.(map[string]any)
					if m["id"] == "group/declared" {
						model = m
					}
				}
			} else {
				model = doc["models"].(map[string]any)["group/declared"].(map[string]any)
			}
			if input == nil {
				continue
			}
			want := make([]any, len(input))
			for i, v := range input {
				want[i] = v
			}
			if shape == "pi" {
				if !reflect.DeepEqual(model["input"], want) {
					t.Fatalf("Pi %v: %s", input, b)
				}
			} else {
				modalities := model["modalities"].(map[string]any)
				if !reflect.DeepEqual(modalities["input"], want) || model["attachment"] != (len(input) == 2) {
					t.Fatalf("OpenCode %v: %s", input, b)
				}
			}
		}
	}
}
