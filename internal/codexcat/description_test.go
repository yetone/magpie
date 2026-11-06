package codexcat

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #5024: Codex 0.166 fails the whole model catalog on a
// supported_reasoning_levels entry without a description ("missing field
// `description`"), so every level of an entry magpie writes from a vendor's
// carries one: the vendor's own where it gave it, else the effort's display
// name. That is whether the user's efforts are the vendor's own, whose
// levels magpie keeps as it wrote them, or differ from them, whose missing
// ones magpie makes. A level of an entry magpie makes alone has said a
// description (an empty one) all along.
func TestCodexListedLevelsSayDescription(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[]}`)
	same := json.RawMessage(`{"slug":"glm-5.3","display_name":"GLM-5.3","description":"x","base_instructions":"y",` +
		`"supported_reasoning_levels":[{"effort":"low","description":"Fastest"},{"effort":"medium"},{"effort":"high"}],` +
		`"shell_type":"shell_command","visibility":"list"}`)
	// a vendor entry with no levels at all, for a model with no efforts
	bare := json.RawMessage(`{"slug":"plain","display_name":"Plain","description":"x","base_instructions":"y",` +
		`"shell_type":"shell_command","visibility":"list"}`)
	short := json.RawMessage(`{"slug":"kimi","display_name":"Kimi","description":"x","base_instructions":"y",` +
		`"supported_reasoning_levels":[{"effort":"low"}],` +
		`"shell_type":"shell_command","visibility":"list"}`)
	var got struct {
		Models []struct {
			Slug   string           `json:"slug"`
			Levels []map[string]any `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(Catalog([]catalog.Model{
		{ID: "relay/glm-5.3", Name: "GLM-5.3", Efforts: []string{"low", "medium", "high"}, Codex: same},
		{ID: "relay/kimi", Name: "Kimi", Efforts: []string{"low", "xhigh"}, Codex: short},
		{ID: "or/deepseek-v4", Name: "DeepSeek V4", Efforts: []string{"low", "max"}},
		{ID: "relay/plain", Name: "Plain", Codex: bare},
	}), &got); err != nil || len(got.Models) != 4 {
		t.Fatalf("%v %v", err, got)
	}
	want := map[string]map[string]string{
		// the vendor's own kept where it gave one, the name where it didn't
		"relay/glm-5.3": {"low": "Fastest", "medium": "Medium", "high": "High"},
		// one the vendor doesn't list, made for an effort of the user's
		"relay/kimi": {"low": "Low", "xhigh": "Extra High"},
		// magpie's own entry: a description said all along
		"or/deepseek-v4": {"low": "", "max": ""},
		// none listed and none asked for: nothing to describe, no panic
		"relay/plain": {},
	}
	for _, m := range got.Models {
		if len(m.Levels) != len(want[m.Slug]) {
			t.Fatalf("%s: levels %v", m.Slug, m.Levels)
		}
		for _, l := range m.Levels {
			ef, _ := l["effort"].(string)
			d, _ := l["description"].(string)
			if d != want[m.Slug][ef] {
				t.Errorf("%s: level %s says description %q, want %q", m.Slug, ef, d, want[m.Slug][ef])
			}
		}
	}
}
