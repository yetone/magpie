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
