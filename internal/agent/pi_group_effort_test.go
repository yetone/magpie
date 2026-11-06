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
// Pi clamped to off, sending no reasoning_effort. The group now gets the
// levels verified for TokenHub's model, so Pi and magpie offer the same
// levels instead of borrowing DeepSeek's unverified off level.
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
	if got := piSupported(entry); !reflect.DeepEqual(got, []string{"low", "high", "max"}) {
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
	if got := values(effort.Options(map[string]string{"model": "magpie/group/deepseek"})); !reflect.DeepEqual(got, []string{"low", "high", "max"}) {
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

// #5037: a routing group has no api of its own — a Messages-only group
// stays on Chat (provider.GroupAPI) — so its entry went out with no promptCache
// and Pi's cacheWarming stopped with "cache lifetime unavailable". Whatever
// Claude member the group picks, the gateway's own translation marks the
// request ephemeral for five minutes, so the group declares that short
// lifetime; long stays out, the gateway marks an hour only where a
// session's turns are often far apart (addedMark), so it is not always true.
func TestPiGroupPromptCache(t *testing.T) {
	syncHome(t)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "ap": {"models": {"claude-opus-5-5": {"id":"claude-opus-5-5","name":"Claude Opus 5.5","limit":{"context":200000}}}},
	  "zai": {"models": {"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	if err := provider.Save(provider.Provider{ID: "ap", Name: "Anthropic Plugin", Key: "k", Anthropic: "http://127.0.0.1:2", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "auto", Name: "Auto", Members: []string{"ap/claude-opus-5-5", "relay/glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	ms := magpieProviderJSON("pi").(map[string]any)["models"].([]map[string]any)
	var group bool
	for _, e := range ms {
		switch e["id"] {
		case "group/auto":
			group = true
			if _, set := e["api"]; set {
				t.Errorf("group api %v, want none (Chat)", e["api"])
			}
			if !reflect.DeepEqual(e["promptCache"], map[string]any{"short": 300}) {
				t.Errorf("group/auto: promptCache %v, want {short: 300}", e["promptCache"])
			}
		case "ap/claude-opus-5-5":
			if e["api"] != "anthropic-messages" {
				t.Fatalf("member on %v, want anthropic-messages", e["api"])
			}
			if !reflect.DeepEqual(e["promptCache"], map[string]any{"short": 300, "long": 3600}) {
				t.Errorf("member: promptCache %v, want short and long", e["promptCache"])
			}
		case "relay/glm-4.6":
			if _, set := e["promptCache"]; set {
				t.Errorf("relay/glm-4.6: promptCache %v, want none", e["promptCache"])
			}
		}
	}
	if !group {
		t.Fatalf("no group/auto entry among %d", len(ms))
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
