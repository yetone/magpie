package provider

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A refusal for a model counted by a pool of some models only is that
// model's, not the account's: Cursor's Other Models used up leaves Auto in
// Cursor Models (Xiaopodev on X), Opus's own week leaves Sonnet; one of
// the whole account's windows used up is the account's.
func TestAllowancePooled(t *testing.T) {
	now := time.Now()
	month := now.Add(20 * 24 * time.Hour)
	pool := map[string]bool{"auto": true, "composer-2": true}
	q := quotaOfPlugin(SubscriptionQuota{}, plugin.Usage{Windows: []plugin.UsageWindow{
		{Name: "Cursor Models", Used: fptr(12), Models: []string{"auto", "composer-2"}},
		{Name: "Other Models", Used: fptr(100), NotModels: []string{"auto", "composer-2"}},
		{Name: "Total", Used: fptr(60), Aside: true},
	}})
	cursor := allowanceOf(q.Windows, now)
	for _, m := range []string{"auto", "composer-2", "claude-4.6-sonnet", "gpt-5.5"} {
		if !cursor.Pooled(m, 100, now) {
			t.Errorf("Cursor: %s refused isn't the model's alone", m)
		}
		if used, _ := cursor.For(m, now); (used == 100) == pool[m] {
			t.Errorf("Cursor: %s used %v", m, used)
		}
	}

	claude := Allowance{
		{Used: 100, Resets: month, Span: 7 * 24 * time.Hour, Model: "opus"},
		{Used: 40, Resets: month, Span: 7 * 24 * time.Hour},
	}
	if !claude.Pooled("claude-opus-4-7", 100, now) {
		t.Error("Opus's own week used up rests the account")
	}
	if claude.Pooled("claude-sonnet-4-6", 100, now) {
		t.Error("Sonnet, refused with no pool of its full, counts as pooled")
	}
	claude[1].Used = 100
	if claude.Pooled("claude-opus-4-7", 100, now) {
		t.Error("the account's week used up rests only Opus")
	}
	if (Allowance{}).Pooled("auto", 100, now) {
		t.Error("an allowance not known counts as pooled")
	}
}

// Will on Discord: Cursor's plugin lists Grok 4.7 at its context sizes
// alone (grok-4.7@256k, grok-4.7@500k), and a routing group still asks for
// bare cursor/grok-4.7, which the plugin runs at its default size. That is
// the Cursor Models pool's (84% left), not Other Models' (used up); the
// Routing page showed it 0% left and routing ranked it last. The lists are
// the ones cursor 0.2.3's usage() returns for such an account.
func TestPluginPoolCountsBareIDOfSizes(t *testing.T) {
	now := time.Now()
	pool := []string{"auto", "grok-4.7@256k", "grok-4.7@500k", "composer-2.5", "default"}
	q := quotaOfPlugin(SubscriptionQuota{}, plugin.Usage{Windows: []plugin.UsageWindow{
		{Name: "Cursor Models", Used: fptr(16), Models: pool},
		{Name: "Other Models", Used: fptr(100), NotModels: pool},
		{Name: "Total", Used: fptr(60), Aside: true},
	}})
	a := allowanceOf(q.Windows, now)
	for m, want := range map[string]float64{
		"grok-4.7":      16, // the reporter's: bare, its sizes listed
		"Grok-4.7":      16,
		"grok-4.7@256k": 16,
		"composer-2.5":  16,
		// distinct models stay in Other Models: another family member, a
		// suffix that isn't a size, a size of a model the pool doesn't list
		"grok-4":               100,
		"grok-4.7-trial":       100,
		"claude-opus-5-5":      100,
		"claude-opus-5-5@300k": 100,
		"composer-2.5-fast@1m": 100,
		"@256k":                100,
	} {
		if used, _ := a.For(m, now); used != want {
			t.Errorf("%s: used %v, want %v", m, used, want)
		}
	}
	if !q.Windows[0].Counts("grok-4.7") || q.Windows[1].Counts("grok-4.7") {
		t.Error("the usage page's holds count grok-4.7 in Other Models")
	}

	// the other way: the pool lists a bare id (Cursor's autoBucketModels),
	// and the model is asked for at a size
	q = quotaOfPlugin(SubscriptionQuota{}, plugin.Usage{Windows: []plugin.UsageWindow{
		{Name: "Cursor Models", Used: fptr(16), Models: []string{"auto", "grok-4.8"}},
		{Name: "Other Models", Used: fptr(100), NotModels: []string{"auto", "grok-4.8"}},
	}})
	a = allowanceOf(q.Windows, now)
	if used, _ := a.For("grok-4.8@1m", now); used != 16 {
		t.Errorf("grok-4.8@1m: used %v, want 16 (Cursor Models)", used)
	}
}

// A model no window counts, on an account whose every window is some
// models' only — Antigravity's Gemini models that fetchAvailableModels
// gives no quota (#3737) beside Claude's own — is refused alone: nothing
// says the account is out, and Claude used up never stopped Gemini 4
// either (runking on Discord). With a window of the whole account, its
// refusal is still the account's.
func TestAllowancePooledModelNoWindowCounts(t *testing.T) {
	now := time.Now()
	week := now.Add(5 * 24 * time.Hour)
	agy := allowanceOf([]QuotaWindow{
		{Name: "Claude Sonnet 4.6", Model: "claude-sonnet-4-6", Family: "Claude", Used: 100, ResetsAt: &week},
		{Name: "Gemini 3 Flash", Model: "gemini-3-flash", Family: "Gemini", Used: 8, ResetsAt: &week},
		{Name: "5 hours", Pool: "Gemini", Used: 8, Aside: true},
	}, now)
	for _, m := range []string{"gemini-4-argon-high", "claude-sonnet-4-6", "gemini-3-flash"} {
		if !agy.Pooled(m, 100, now) {
			t.Errorf("Antigravity: %s refused rests the account", m)
		}
	}
	whole := Allowance{{Used: 30, Resets: week, Span: 7 * 24 * time.Hour}, {Used: 10, Resets: week, Model: "opus"}}
	if whole.Pooled("gemini-4-argon-high", 100, now) {
		t.Error("a model the account's own week counts, refused, rests only the model")
	}
	if (Allowance{{Used: 10, Resets: week, Model: "opus"}, {Used: 20, Resets: week}}).Pooled("x", 100, now) {
		t.Error("with a window of the whole account, a model no window counts is refused alone")
	}
}
