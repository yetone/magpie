package agent

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Every one of Pi's levels is in the map, the model's own mapped and the
// rest null, so Pi's /thinking offers the model's levels alone (#243); off
// is the model's none, and left for Pi to offer on Anthropic's Messages
// API, where it turns thinking off. A model with no known levels gets none.
func TestPiThinkingLevels(t *testing.T) {
	for _, c := range []struct {
		efforts   []string
		anthropic bool
		want      map[string]any
	}{
		{[]string{"low", "high", "max"}, false, map[string]any{
			"off": nil, "minimal": nil, "low": "low", "medium": nil, "high": "high", "xhigh": nil, "max": "max"}},
		{[]string{"low", "medium", "high", "xhigh"}, false, map[string]any{
			"off": nil, "minimal": nil, "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": nil}},
		{[]string{"none", "low", "high", "xhigh", "max"}, false, map[string]any{
			"off": "none", "minimal": nil, "low": "low", "medium": nil, "high": "high", "xhigh": "xhigh", "max": "max"}},
		{[]string{"none", "minimal", "low", "medium", "high"}, false, map[string]any{
			"off": "none", "minimal": "minimal", "low": "low", "medium": "medium", "high": "high", "xhigh": nil, "max": nil}},
		// Claude keeps off (thinking disabled) and its xhigh and max
		{[]string{"low", "medium", "high", "xhigh", "max"}, true, map[string]any{
			"minimal": nil, "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"}},
		{[]string{"none", "high"}, true, map[string]any{
			"off": "none", "minimal": nil, "low": nil, "medium": nil, "high": "high", "xhigh": nil, "max": nil}},
		{nil, false, nil},
		{nil, true, nil},
	} {
		if got := piThinkingLevels(c.efforts, c.anthropic); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v (anthropic %v): got %v, want %v", c.efforts, c.anthropic, got, c.want)
		}
	}
}

// #699: ZCode's GLM-5.3-Flash, on Anthropic's Messages API, always thinks
// (its levels low, high and max, no none) and turns thinking disabled away
// ("该模型始终支持思考，不可关闭"); its entry left off out, which Pi offers,
// so off is null for it. A Claude there still has off left for Pi to offer,
// and a vendor's model that takes none maps off to it.
func TestPiOffOnMessagesAPI(t *testing.T) {
	for _, c := range []struct {
		id      string
		efforts []string
		off     any // the map's off; "left out" when it has none
		offered []string
	}{
		{"zcode/GLM-5.3-Flash", []string{"low", "high", "max"}, nil, []string{"low", "high", "max"}},
		{"zcode/GLM-5.2", []string{"none", "high", "max"}, "none", []string{"off", "high", "max"}},
		{"anth/claude-sonnet-5", []string{"low", "medium", "high", "xhigh", "max"}, "left out", []string{"off", "low", "medium", "high", "xhigh", "max"}},
		{"relay/opus-5.5", []string{"low", "high", "max"}, "left out", []string{"off", "low", "high", "max"}},
	} {
		m := catalog.Model{ID: c.id, Name: c.id, Efforts: c.efforts, APIs: []string{"anthropic"}}
		e := piModelJSON(m, "http://127.0.0.1:1", true)
		if e["api"] != "anthropic-messages" {
			t.Fatalf("%s on %v", c.id, e["api"])
		}
		levels := e["thinkingLevelMap"].(map[string]any)
		off, set := levels["off"]
		if !set {
			off = "left out"
		}
		if off != c.off {
			t.Errorf("%s: off is %v, want %v", c.id, off, c.off)
		}
		if got := piSupported(e); !reflect.DeepEqual(got, c.offered) {
			t.Errorf("%s: Pi offers %v, want %v", c.id, got, c.offered)
		}
	}
}

// #781: Pi weighs warming a model's cache by its cost and promptCache, and
// warms nothing without them. A Claude on Anthropic's Messages API keeps a
// cache 5 minutes or an hour, a GPT on the Responses API 5 minutes; one
// relayed through a subscription's own client (no api) or of an unknown
// vendor declares none, and a model without a known price no cost.
func TestPiCostAndPromptCache(t *testing.T) {
	price := &catalog.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	for _, c := range []struct {
		m     catalog.Model
		cost  any
		cache any
	}{
		{catalog.Model{ID: "anthropic/claude-sonnet-5", APIs: []string{"anthropic"}, Price: price},
			map[string]any{"input": 3.0, "output": 15.0, "cacheRead": 0.3, "cacheWrite": 3.75},
			map[string]any{"short": 300, "long": 3600}},
		{catalog.Model{ID: "openai/gpt-6-sol", APIs: []string{"responses"}},
			nil, map[string]any{"short": 300}},
		{catalog.Model{ID: "claude/claude-opus-5-5", Price: price},
			map[string]any{"input": 3.0, "output": 15.0, "cacheRead": 0.3, "cacheWrite": 3.75}, nil},
		{catalog.Model{ID: "deepseek/deepseek-flash", APIs: []string{"anthropic"}}, nil, nil},
	} {
		e := piModelJSON(c.m, "http://127.0.0.1:1", true)
		if !reflect.DeepEqual(e["cost"], c.cost) {
			t.Errorf("%s: cost %v, want %v", c.m.ID, e["cost"], c.cost)
		}
		if !reflect.DeepEqual(e["promptCache"], c.cache) {
			t.Errorf("%s: promptCache %v, want %v", c.m.ID, e["promptCache"], c.cache)
		}
	}
}
