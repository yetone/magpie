package provider

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tm is a model a template may be made of, as templateModels rates it.
func tm(pid, model string, out float64, maker bool, released string) rated {
	r := rated{e: Entry{ID: pid + "/" + model, Model: model, Provider: &Provider{ID: pid, Name: pid}, Released: released}, out: out, maker: maker}
	r.small, r.tiny = sizeOf(model)
	return r
}

func TestSizeOfReadsWords(t *testing.T) {
	for _, c := range []struct {
		model       string
		small, tiny bool
	}{
		{"gemini-3-pro", false, false}, // "gemini" is no "mini"
		{"gpt-6-mini", true, false},
		{"gpt-6-nano", true, true},
		{"claude-haiku-5", true, false},
		{"gemini-3-flash-lite", true, true},
		{"qwen3-8b", true, true},
		{"llama-4-70b", true, false},
		{"deepseek-v4-671b", false, false},
		{"glm-5-air", true, false},
		{"claude-opus-5.5", false, false},
	} {
		if small, tiny := sizeOf(c.model); small != c.small || tiny != c.tiny {
			t.Errorf("sizeOf(%q) = %v, %v; want %v, %v", c.model, small, tiny, c.small, c.tiny)
		}
	}
}

func TestTemplatesPickStrongAndCheap(t *testing.T) {
	ms := []rated{
		tm("relay", "obscure-lab-ultra", 60, false, "2026-09-01"), // no known maker
		tm("anthropic", "claude-opus-5.5", 50, true, "2026-08-01"),
		tm("openai", "gpt-6.1", 40, true, "2026-09-20"),
		tm("openai", "gpt-6.1-pro", 600, true, "2026-09-20"), // thinks for minutes: too dear
		tm("openai", "gpt-6-mini", 4, true, "2026-07-01"),
		tm("openai", "gpt-6-nano", 0.8, true, "2026-07-01"),
		tm("google", "gemini-3-flash", 3, true, "2026-06-01"),
		tm("openrouter", "openai/gpt-6-mini", 4, true, "2026-07-01"),
	}
	smart := templateOf(TemplateSmart, ms, nil)
	if smart.Why != "" || !slices.Equal(smart.Group.Members, []string{"openai/gpt-6.1", "openai/gpt-6-mini"}) {
		t.Fatalf("smart = %+v, want the newest flagship then the newest cheap model that isn't tiny", smart)
	}
	if smart.Group.Routing != Ordered || smart.Group.Classifier == "" {
		t.Errorf("smart = %+v, want ordered with a classifier", smart.Group)
	}
	var hard, easy int = -1, -1
	for i, r := range smart.Group.Rules {
		switch r.Intent {
		case IntentHard:
			hard = i
			if r.Use != "openai/gpt-6.1" {
				t.Errorf("hard turns go to %s", r.Use)
			}
		case IntentEasy:
			easy = i
			if r.Use != "openai/gpt-6-mini" {
				t.Errorf("easy turns go to %s", r.Use)
			}
		}
	}
	// an unclear turn is asked hard first, so it stays on the strong model
	if hard < 0 || easy < hard {
		t.Errorf("rules %+v: want the hard intent before the easy one", smart.Group.Rules)
	}
	if c := templateOf(TemplateSmart, ms, []Entry{{ID: "typesafe/jev"}}).Group.Classifier; c != "typesafe/jev" {
		t.Errorf("classifier %q, want the decision API when there is one", c)
	}
	if !slices.ContainsFunc(smart.Group.Rules, func(r Rule) bool { return r.Compact && r.Use == "openai/gpt-6-mini" }) {
		t.Errorf("rules %+v: want compaction on the cheap model", smart.Group.Rules)
	}

	thrifty := templateOf(TemplateThrifty, ms, nil)
	// the second cheap one is another model from another provider, not
	// the same mini through a router
	if want := []string{"openai/gpt-6-mini", "google/gemini-3-flash", "openai/gpt-6.1"}; !slices.Equal(thrifty.Group.Members, want) {
		t.Errorf("thrifty members = %v, want %v", thrifty.Group.Members, want)
	}
	if !slices.ContainsFunc(thrifty.Group.Rules, func(r Rule) bool { return r.Effort == "high" && r.Use == "openai/gpt-6.1" }) {
		t.Errorf("thrifty rules %+v: want high reasoning on the strong model", thrifty.Group.Rules)
	}

	steady := templateOf(TemplateSteady, ms, nil)
	if want := []string{"openai/gpt-6.1", "anthropic/claude-opus-5.5", "openrouter/openai/gpt-6-mini"}; !slices.Equal(steady.Group.Members, want) {
		t.Errorf("steady members = %v, want %v", steady.Group.Members, want)
	}
	for _, tp := range []Template{smart, thrifty, steady} {
		if slices.Contains(tp.Group.Members, "openai/gpt-6.1-pro") {
			t.Errorf("%s holds a model past tooDear: %v", tp.Kind, tp.Group.Members)
		}
	}
}

// The routing recipes (plan first, fastest first, spread evenly) are the
// strongest models set to a routing the engine already has.
func TestRecipeTemplates(t *testing.T) {
	sub := tm("sub", "claude-opus-5.5", 50, true, "2026-08-01")
	sub.e.Provider.Billing = BillingPlan
	key := tm("openai", "gpt-6.1", 40, true, "2026-09-20")
	key.e.Provider.Billing = BillingPAYG
	relay := tm("relay", "gpt-6.1", 40, true, "2026-09-20")
	ms := []rated{key, relay, sub, tm("openai", "gpt-6-mini", 4, true, "2026-07-01")}

	// the plan's model first, though the key's model is newer
	plan := templateOf(TemplatePlan, ms, nil)
	if want := []string{"sub/claude-opus-5.5", "openai/gpt-6.1", "relay/gpt-6.1"}; plan.Why != "" || !slices.Equal(plan.Group.Members, want) || plan.Group.Routing != Ordered {
		t.Errorf("plan = %+v, want %v in order", plan, want)
	}
	// nothing on a plan: nothing to put first
	if tp := templateOf(TemplatePlan, []rated{key, relay}, nil); tp.Need != NeedPlan || len(tp.Group.Members) > 0 {
		t.Errorf("plan without a plan: %+v", tp)
	}
	fast := templateOf(TemplateFast, ms, nil)
	if fast.Group.Routing != Fastest || len(fast.Group.Members) != 3 {
		t.Errorf("fast = %+v", fast)
	}
	if tp := templateOf(TemplateFast, []rated{key, ms[3]}, nil); tp.Need != NeedProviders {
		t.Errorf("fast with one provider: %+v", tp)
	}
	// one model, every provider that serves it; the other model stays out
	even := templateOf(TemplateEven, ms, nil)
	if even.Group.Routing != Rotate || !slices.Equal(even.Group.Members, []string{"openai/gpt-6.1", "relay/gpt-6.1"}) {
		t.Errorf("even = %+v, want the one strongest model from both providers", even)
	}
	if even = templateOf(TemplateEven, []rated{sub, ms[3]}, nil); !slices.Equal(even.Group.Members, []string{"sub/claude-opus-5.5"}) {
		t.Errorf("even with one provider = %+v, want it alone", even)
	}
	for _, k := range TemplateKinds {
		if TemplateNames[k] == "" || TemplateIDs[k] == "" {
			t.Errorf("template %s has no name or id", k)
		}
	}
}

func TestTemplatesSayWhatIsMissing(t *testing.T) {
	if tp := templateOf(TemplateSmart, nil, nil); tp.Need != NeedProvider || len(tp.Group.Members) > 0 {
		t.Errorf("no models: %+v", tp)
	}
	one := []rated{tm("openai", "gpt-6.1", 40, true, ""), tm("openai", "gpt-6-mini", 4, true, "")}
	if tp := templateOf(TemplateSteady, one, nil); tp.Need != NeedProviders || tp.Of != "openai" || len(tp.Group.Members) > 0 {
		t.Errorf("one provider: %+v", tp)
	}
	if tp := templateOf(TemplateSmart, one, nil); tp.Why != "" {
		t.Errorf("one provider is enough for smart: %+v", tp)
	}
	dear := []rated{tm("openai", "gpt-6.1", 40, true, ""), tm("anthropic", "claude-opus-5.5", 50, true, "")}
	if tp := templateOf(TemplateSmart, dear, nil); tp.Need != NeedCheap || !strings.Contains(tp.Why, "cheap") {
		t.Errorf("no cheap model: %+v", tp)
	}
	// with no prices, a small model by its id is the cheap one
	unpriced := []rated{tm("sub", "big-model", 0, false, ""), tm("sub", "big-model-mini", 0, false, "")}
	if tp := templateOf(TemplateSmart, unpriced, nil); !slices.Equal(tp.Group.Members, []string{"sub/big-model", "sub/big-model-mini"}) {
		t.Errorf("unpriced: %+v", tp)
	}
}

// A template added twice, or under a name in Chinese, is saved beside the
// group there is, never over it.
func TestTemplateNameNeverReplacesAGroup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	all := []Group{{ID: "smart-split", Name: "Hard to strong, easy to cheap"}, {ID: "smart-split-2", Name: "难题给强模型"}}
	if name, id := freeGroupName(all, "Hard to strong, easy to cheap", "smart-split"); name != "Hard to strong, easy to cheap 2" || id != "smart-split-3" {
		t.Errorf("again: %q %q", name, id)
	}
	if name, id := freeGroupName(all, "难题给强模型", "smart-split"); name != "难题给强模型 2" || id != "smart-split-3" {
		t.Errorf("in Chinese: %q %q", name, id)
	}
	if name, id := freeGroupName(nil, "难题给强模型", "smart-split"); name != "难题给强模型" || id != "smart-split" {
		t.Errorf("first: %q %q", name, id)
	}
	// a copy is named as before: its id made of its name
	if _, id := freeGroupName(all, "Mine copy", ""); id != "mine-copy" {
		t.Errorf("copy: %q", id)
	}
}
