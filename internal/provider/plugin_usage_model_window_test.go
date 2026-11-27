package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// Google AI Studio's plugin (@dorokuma/magpie-gemini-web 0.15.3,
// lib/studio.mjs accountUsage) tells an account's cooldowns as windows
// named after the model (the backend's model_id), used 100 until the
// cooldown ends, with no model said. AI Studio limits each model's day on
// its own, so gemini-4-hydrogen cooling leaves argon in its own; only the
// window it names "所有模型" (a cooldown with no model_id) is the whole
// account's (WangYeruo on X).
func TestPluginWindowNamedByModelCountsThatModel(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	models := []plugin.Model{{ID: "gemini-4-hydrogen"}, {ID: "gemini-4-argon"}}
	read := func(body string) Allowance {
		t.Helper()
		var u plugin.Usage
		if err := json.Unmarshal([]byte(body), &u); err != nil {
			t.Fatal(err)
		}
		q := namedModelWindows(quotaOfPlugin(SubscriptionQuota{Provider: "ai-studio", User: "a@gmail.com"}, u), models)
		return allowanceOf(q.Windows, now)
	}

	// the plugin's own bytes for one model cooling (accountUsage's window)
	a := read(`{"user":"a@gmail.com","windows":[{"name":"gemini-4-hydrogen","used":100,"resetsAt":"2026-11-18T08:00:00.000Z","display":"冷却中：每日限额"}]}`)
	if a.Full("gemini-4-hydrogen", 100, now).IsZero() {
		t.Error("hydrogen's own cooldown doesn't hold hydrogen")
	}
	if got := a.Full("gemini-4-argon", 100, now); !got.IsZero() {
		t.Errorf("hydrogen's cooldown holds argon till %v", got)
	}
	if !a.Pooled("gemini-4-hydrogen", 100, now) {
		t.Error("a 429 of hydrogen rests the account, not the model")
	}

	// a cooldown with no model is every model's, and rests the account
	whole := read(`{"user":"a@gmail.com","windows":[{"name":"所有模型","used":100,"resetsAt":"2026-11-18T08:00:00.000Z","display":"冷却中"}]}`)
	if whole.Full("gemini-4-argon", 100, now).IsZero() {
		t.Error("所有模型 cooling doesn't hold argon")
	}
	if whole.Pooled("gemini-4-hydrogen", 100, now) {
		t.Error("所有模型 cooling rests the model only")
	}

	// a window named for no model the provider serves stays the account's
	other := read(`{"windows":[{"name":"gemini-4-neon","used":100,"resetsAt":"2026-11-18T08:00:00.000Z"}]}`)
	if other.Full("gemini-4-argon", 100, now).IsZero() {
		t.Error("a window naming no served model was scoped")
	}
}

func TestClinePluginUsageWindowsOnlyCountClinePassModels(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var u plugin.Usage
	if err := json.Unmarshal([]byte(`{"windows":[{"name":"Weekly","used":80,"resetsAt":"2026-12-01T00:00:00Z"}]}`), &u); err != nil {
		t.Fatal(err)
	}
	q := scopePluginWindows("cline", quotaOfPlugin(SubscriptionQuota{Provider: "cline-plugin", User: "u"}, u))
	a := allowanceOf(q.Windows, now)
	if used, _ := a.For("cline-pass/mythomax-l2-13b", now); used != 80 {
		t.Fatalf("ClinePass model used %v, want 80", used)
	}
	for _, model := range []string{"cline-free/deepseek-v4.1-flash", "openai/gpt-5.5"} {
		if used, _ := a.For(model, now); used != 0 {
			t.Errorf("%s counted against ClinePass window: %v", model, used)
		}
	}
	other := scopePluginWindows("other", quotaOfPlugin(SubscriptionQuota{Provider: "other-plugin", User: "u"}, u))
	if used, _ := allowanceOf(other.Windows, now).For("openai/gpt-5.5", now); used != 80 {
		t.Fatalf("non-Cline plugin window was unexpectedly scoped: %v", used)
	}
}
