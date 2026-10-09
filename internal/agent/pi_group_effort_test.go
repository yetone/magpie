package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #597: Pi on a routing group of DeepSeek V4 Pro (TokenHub's, and the Token
// Plan's deepseek-v4-pro-202606 that nothing magpie reads knows) was
// written the group without reasoning, so its /thinking offered off alone,
// while magpie's thinking field offered every level and showed max — which
// Pi clamped to off, sending no reasoning_effort. The group now has its
// known member's levels, Pi's entry offers them, and magpie's field offers
// what Pi offers for the model and shows the level Pi runs it at.
func TestPiGroupThinkingIsWhatPiRuns(t *testing.T) {
	home := syncHome(t)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "deepseek": {"models": {"deepseek-v4-pro": {"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","reasoning":true,
	    "reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"limit":{"context":1000000}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	for _, p := range []provider.Provider{
		{ID: "th", Name: "TokenHub", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "tencent-tokenhub", Models: []string{"deepseek-v4-pro"}},
		{ID: "tp", Name: "Token Plan", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4-pro-202606"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "deepseek", Name: "腾讯DeepSeek", Members: []string{"tp/deepseek-v4-pro-202606", "th/deepseek-v4-pro"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "plan", Name: "Plan", Members: []string{"tp/deepseek-v4-pro-202606"}}); err != nil {
		t.Fatal(err)
	}
	a := pi(home)
	if err := a.Field("model").Set("magpie/group/deepseek"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("max"); err != nil {
		t.Fatal(err)
	}

	var file map[string]any
	if err := json.Unmarshal([]byte(readFile(filepath.Join(home, ".pi", "agent", "models.json"))), &file); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	for _, raw := range file["providers"].(map[string]any)["magpie"].(map[string]any)["models"].([]any) {
		if m := raw.(map[string]any); m["id"] == "group/deepseek" {
			entry = m
		}
	}
	if entry == nil || entry["reasoning"] != true {
		t.Fatalf("group entry: %v", entry)
	}
	if got := piSupported(entry); !reflect.DeepEqual(got, []string{"off", "low", "high", "max"}) {
		t.Fatalf("Pi offers %v for the group: %v", got, entry["thinkingLevelMap"])
	}

	values := func(os []Option) []string {
		var out []string
		for _, o := range os {
			out = append(out, o.Value)
		}
		return out
	}
	effort := a.Field("effort")
	if got := values(effort.Options(map[string]string{"model": "magpie/group/deepseek"})); !reflect.DeepEqual(got, []string{"off", "low", "high", "max"}) {
		t.Fatalf("offered %v", got)
	}
	if got := effort.Get(); got != "max" {
		t.Fatalf("shown %q, want max", got)
	}

	// on a group Pi can't reason with, max is shown as the off Pi runs at
	if err := a.Field("model").Set("magpie/group/plan"); err != nil {
		t.Fatal(err)
	}
	if got := values(effort.Options(map[string]string{"model": "magpie/group/plan"})); !reflect.DeepEqual(got, []string{"off"}) {
		t.Fatalf("plan: offered %v", got)
	}
	if got := effort.Get(); got != "off" {
		t.Fatalf("plan: shown %q, want off", got)
	}
	// a model of Pi's own keeps every level
	if got := values(effort.Options(map[string]string{"model": "anthropic/claude-x"})); !reflect.DeepEqual(got, piLevels) {
		t.Fatalf("own model: offered %v", got)
	}
}

// Pi's clamp: the level itself, else the nearest above, else below.
func TestPiClamp(t *testing.T) {
	for _, c := range []struct {
		level   string
		offered []string
		want    string
	}{
		{"max", []string{"off"}, "off"},
		{"max", []string{"low", "high", "max"}, "max"},
		{"medium", []string{"low", "high", "max"}, "high"},
		{"xhigh", []string{"off", "low", "medium", "high"}, "high"},
		{"minimal", []string{"low", "high"}, "low"},
	} {
		if got := piClamp(c.level, c.offered); got != c.want {
			t.Errorf("%s of %v: %s, want %s", c.level, c.offered, got, c.want)
		}
	}
}
