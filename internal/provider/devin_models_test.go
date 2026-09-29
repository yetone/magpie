package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// devinListed is a CLI list the way Devin gives it: families whose
// variants are the family at an effort, in either spelling, and variants
// that are no effort at all.
var devinListed = []DevinFamily{
	{UID: "swe-2", Label: "SWE-2", Aliases: []string{"swe"}, Models: []catalog.Model{
		{ID: "swe-2-low", Name: "SWE-2 Low", Provider: "devin", Context: 262000, Output: 128000},
		{ID: "swe-2-medium", Name: "SWE-2 Medium", Provider: "devin", Context: 262000, Output: 128000},
		{ID: "swe-2-high", Name: "SWE-2 High", Provider: "devin", Context: 262000, Output: 128000},
		{ID: "swe-2-max", Name: "SWE-2 Max", Provider: "devin", Context: 262000, Output: 128000},
		{ID: "swe-2-low-fast", Name: "SWE-2 Low Fast", Provider: "devin", Context: 262000, Output: 128000},
		{ID: "swe-2-high-fast", Name: "SWE-2 High Fast", Provider: "devin", Context: 262000, Output: 128000},
	}},
	{UID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", Models: []catalog.Model{
		{ID: "MODEL_CLAUDE_SONNET_5_5_MEDIUM", Name: "Claude Sonnet 5.5 Medium", Provider: "devin", Context: 1000000, Output: 64000},
		{ID: "MODEL_CLAUDE_SONNET_5_5_HIGH", Name: "Claude Sonnet 5.5 High", Provider: "devin", Context: 1000000, Output: 64000},
		{ID: "claude-sonnet-5-5-high-fast", Name: "Claude Sonnet 5.5 High Fast", Provider: "devin"},
	}},
	{UID: "glm-5.2", Label: "GLM-5.2", Models: []catalog.Model{
		{ID: "glm-5-2", Name: "GLM-5.2", Provider: "devin", Context: 200000},
		{ID: "glm-5-2-1m", Name: "GLM-5.2 1M", Provider: "devin", Context: 1000000},
	}},
	{UID: "kimi-k3", Label: "Kimi K3", Models: []catalog.Model{{ID: "kimi-k3-0901", Name: "Kimi K3", Provider: "devin"}}},
}

func devinOffered(ms []catalog.Model) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID+"|"+strings.Join(m.Efforts, ","))
	}
	return strings.Join(out, " ")
}

// Devin's list is offered as its families, each with the efforts its
// variants are at, so the effort is picked as any model's is; a variant
// that is no effort (a 1M window) stays a model of its own, and a family's
// fast variants are one more model, the family's fast run.
func TestDevinModelsOfferFamilies(t *testing.T) {
	got := devinOffered(devinModels(devinListed))
	want := "swe-2|low,medium,high,max swe-2-fast|low,high claude-sonnet-5-5|medium,high claude-sonnet-5-5-fast|high glm-5.2| glm-5-2| glm-5-2-1m| kimi-k3|"
	if got != want {
		t.Fatalf("offered\n %s\nwant\n %s", got, want)
	}
	for _, m := range devinModels(devinListed) {
		if m.ID == "swe-2" && (m.Name != "SWE-2" || m.Context != 262000) {
			t.Fatalf("a family keeps its name and window: %+v", m)
		}
		if m.ID == "swe-2-fast" && (m.Name != "SWE-2 Fast" || m.Context != 262000 || m.Output != 128000) {
			t.Fatalf("a fast run is named for its family, with its window: %+v", m)
		}
	}
}

// A list saved flat, before the families were one model, is collapsed
// the same way once the CLI list is read, but for the variants the user
// picked: those stay, at the one effort each id is at.
func TestDevinCollapseKeepsPickedVariants(t *testing.T) {
	flat := devinModelsFlatten(devinListed)
	got := devinOffered(devinCollapse(flat, devinListed, []string{"swe-2-medium", "MODEL_CLAUDE_SONNET_5_5_MEDIUM", "claude-sonnet-5-5-high-fast"}))
	want := "swe-2|low,medium,high,max swe-2-fast|low,high swe-2-medium|medium claude-sonnet-5-5|medium,high claude-sonnet-5-5-fast|high MODEL_CLAUDE_SONNET_5_5_MEDIUM|medium claude-sonnet-5-5-high-fast|high glm-5.2| glm-5-2| glm-5-2-1m| kimi-k3|"
	if got != want {
		t.Fatalf("collapsed\n %s\nwant\n %s", got, want)
	}
	// a list already collapsed, with no CLI list read: a picked variant is
	// added back at its effort, one nobody knows of isn't
	got = devinOffered(devinCollapse(devinModels(devinListed), nil, []string{"swe-2-high", "made-up"}))
	if !strings.HasSuffix(got, " kimi-k3| swe-2-high|high") {
		t.Fatalf("picked back: %s", got)
	}
}

// A family is asked for as its variant at the effort asked for, or the
// nearest it has; a variant's own id goes as it is, whatever the effort.
func TestDevinVariantIn(t *testing.T) {
	for _, c := range []struct{ model, effort, want string }{
		{"swe-2", "", "swe-2-low"},
		{"swe-2", "medium", "swe-2-medium"},
		{"swe", "high", "swe-2-high"},
		{"swe-2", "xhigh", "swe-2-max"}, // a tie goes up
		{"swe-2", "minimal", "swe-2-low"},
		{"claude-sonnet-5-5", "max", "MODEL_CLAUDE_SONNET_5_5_HIGH"},
		{"claude-sonnet-5-5", "low", "MODEL_CLAUDE_SONNET_5_5_MEDIUM"},
		{"glm-5.2", "high", "glm-5-2"},
		// the id the user picked is the effort it runs at (蓝猫 on Discord)
		{"swe-2-medium", "max", "swe-2-medium"},
		{"MODEL_CLAUDE_SONNET_5_5_MEDIUM", "max", "MODEL_CLAUDE_SONNET_5_5_MEDIUM"},
		{"claude-sonnet-5-5-high-fast", "low", "claude-sonnet-5-5-high-fast"},
		{"swe-2-high-fast", "low", "swe-2-high-fast"},
		// a family's fast run is its fast variant at the effort, the
		// nearest it has, or at the family's default effort
		{"swe-2-fast", "", "swe-2-low-fast"},
		{"swe-2-fast", "high", "swe-2-high-fast"},
		{"swe-2-fast", "medium", "swe-2-high-fast"}, // a tie goes up
		{"swe-2-fast", "max", "swe-2-high-fast"},
		{"swe-fast", "low", "swe-2-low-fast"},
		{"claude-sonnet-5-5-fast", "", "claude-sonnet-5-5-high-fast"},
		{"claude-sonnet-5-5-fast", "low", "claude-sonnet-5-5-high-fast"},
		{"glm-5.2-fast", "high", "glm-5.2-fast"},
		{"unknown", "high", "unknown"},
	} {
		if got := devinVariantIn(devinListed, c.model, c.effort); got != c.want {
			t.Errorf("%s at %q: %s, want %s", c.model, c.effort, got, c.want)
		}
	}
}

// Through the provider: a family takes its variants' efforts, a picked
// variant and one an agent was set to (not in the list) take the one
// their id is at, so an effort asked of them is fitted to it.
func TestDevinEfforts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	devinFamiliesCached(devinListed)
	t.Cleanup(func() { devinFamiliesCached(nil) })
	if err := catalog.SaveLive("devin", "", devinModels(devinListed)); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "devin", Models: []string{"swe-2", "swe-2-medium"}}
	for model, want := range map[string]string{
		"swe-2": "low,medium,high,max", "claude-sonnet-5-5": "medium,high",
		"swe-2-medium": "medium", "MODEL_CLAUDE_SONNET_5_5_HIGH": "high", "glm-5-2-1m": "",
		"swe-2-fast": "low,high", "claude-sonnet-5-5-fast": "high", "swe-2-high-fast": "high",
	} {
		if got := strings.Join(p.Efforts(model), ","); got != want {
			t.Errorf("%s: %q, want %q", model, got, want)
		}
	}
	ex := p.Exposed()
	if len(ex) != 2 || ex[1].ID != "swe-2-medium" || ex[1].Context != 262000 || strings.Join(ex[1].Efforts, ",") != "medium" {
		t.Fatalf("exposed %+v", ex)
	}
}

// Devin's list as it is: a family's fast and priority variants are a model
// each (claude-opus-5-5-fast, gpt-6-sol-priority), while an id that ends in
// fast with no effort before it (swe-1-6-fast, its own family) stays itself
// and isn't taken for swe-1.6's fast run.
func TestDevinTiers(t *testing.T) {
	families := []DevinFamily{
		{UID: "claude-opus-5-5", Label: "Claude Opus 5.5", Models: []catalog.Model{
			{ID: "claude-opus-5-5-medium"}, {ID: "claude-opus-5-5-low"}, {ID: "claude-opus-5-5-high"},
			{ID: "claude-opus-5-5-low-fast"}, {ID: "claude-opus-5-5-medium-fast"}, {ID: "claude-opus-5-5-max-fast"},
		}},
		{UID: "gpt-6-sol", Label: "GPT-6 Sol", Models: []catalog.Model{
			{ID: "gpt-6-sol-medium"}, {ID: "gpt-6-sol-none"}, {ID: "gpt-6-sol-high"},
			{ID: "gpt-6-sol-none-priority"}, {ID: "gpt-6-sol-high-priority"},
		}},
		{UID: "swe-1.6", Label: "SWE-1.6", Models: []catalog.Model{{ID: "swe-1-6"}}},
		{UID: "swe-1.6-fast", Label: "SWE-1.6 Fast", Models: []catalog.Model{{ID: "swe-1-6-fast"}}},
	}
	got := devinOffered(devinModels(families))
	want := "claude-opus-5-5|low,medium,high claude-opus-5-5-fast|low,medium,max gpt-6-sol|none,medium,high gpt-6-sol-priority|none,high swe-1.6| swe-1.6-fast|"
	if got != want {
		t.Fatalf("offered\n %s\nwant\n %s", got, want)
	}
	for _, c := range []struct{ model, effort, want string }{
		{"claude-opus-5-5-fast", "", "claude-opus-5-5-medium-fast"},
		{"claude-opus-5-5-fast", "xhigh", "claude-opus-5-5-max-fast"},
		{"claude-opus-5-5-fast", "high", "claude-opus-5-5-medium-fast"},
		{"gpt-6-sol-priority", "minimal", "gpt-6-sol-none-priority"},
		{"gpt-6-sol-priority", "", "gpt-6-sol-high-priority"}, // medium, the family default
		{"swe-1.6-fast", "high", "swe-1-6-fast"},
	} {
		if got := devinVariantIn(families, c.model, c.effort); got != c.want {
			t.Errorf("%s at %q: %s, want %s", c.model, c.effort, got, c.want)
		}
	}
	// an id Devin's config holds, as the picker shows it, and back
	for _, c := range []struct{ id, model, effort string }{
		{"claude-opus-5-5-high", "claude-opus-5-5", "high"},
		{"claude-opus-5-5-max-fast", "claude-opus-5-5-fast", "max"},
		{"gpt-6-sol-none-priority", "gpt-6-sol-priority", "none"},
		{"claude-opus-5-5", "claude-opus-5-5", ""},
		{"swe-1-6-fast", "swe-1-6-fast", ""},
		{"gone-model-high", "gone-model-high", ""},
	} {
		m, e := DevinSplit(families, c.id)
		if m != c.model || e != c.effort {
			t.Errorf("split %s: %s at %q, want %s at %q", c.id, m, e, c.model, c.effort)
		}
		if back := DevinPick(families, m, e); back != c.id {
			t.Errorf("pick %s at %q: %s, want %s", m, e, back, c.id)
		}
	}
	if got := DevinPick(families, "claude-opus-5-5-fast", ""); got != "claude-opus-5-5-medium-fast" {
		t.Errorf("a fast run with no effort is its variant at the family's default: %s", got)
	}
	// the agent's picker has Adaptive and Fusion too, which Devin's own
	// agent runs, as one model each
	withOwn := append([]DevinFamily{{UID: "Adaptive", Label: "Adaptive", Models: []catalog.Model{{ID: "adaptive"}}}}, families...)
	if got := devinOffered(DevinOffered(withOwn)); !strings.HasPrefix(got, "Adaptive| claude-opus-5-5|") {
		t.Errorf("offered to the agent: %s", got)
	}
}
