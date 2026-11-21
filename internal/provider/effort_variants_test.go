package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// elis_pina on X (after #6582): a relay added by its address listed one
// model once for each effort it serves it at, the effort in the id, and
// the provider editor's chips and every agent's picker had each as a model
// of its own. The list here is the one in the report's screenshot, by id.
var relayEffortList = []string{
	"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro",
	"gemini-3-flash", "gemini-3-flash-preview",
	"gemini-3.1-flash-lite", "gemini-3.1-flash-lite-preview",
	"gemini-3.1-pro", "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-low",
	"gemini-3.6-flash-tiered",
	"gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-3.8-flash-medium",
	"gemini-3.8-flash-tiered",
	"gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-argon-medium",
	"gemini-4-carbon-high", "gemini-4-carbon-low", "gemini-4-carbon-medium",
	"gemini-4-hydrogen-high", "gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-xhigh",
	"kimi-k2.7-code-highspeed", "kimi-k3", "kimi-k3.1-pro", "kimi-k3.1-pro-highspeed",
}

func saveRelayList(t *testing.T, pid string, ids ...string) {
	t.Helper()
	ms := make([]catalog.Model, len(ids))
	for i, id := range ids {
		ms[i] = catalog.Model{ID: id}
	}
	if err := catalog.SaveLive(pid, "https://"+pid+".example.invalid/v1", ms); err != nil {
		t.Fatal(err)
	}
}

func TestRelayEffortIDsAreOneModel(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "gem", Name: "Gemini", Key: "sk-x", Chat: "https://gem.example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	saveRelayList(t, "gem", relayEffortList...)
	p := findProvider(t, "gem")
	ms := p.Available()
	want := []string{
		"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro",
		"gemini-3-flash", "gemini-3-flash-preview",
		"gemini-3.1-flash-lite", "gemini-3.1-flash-lite-preview",
		"gemini-3.1-pro", "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-low",
		"gemini-3.6-flash-tiered",
		"gemini-3.8-flash",
		"gemini-3.8-flash-tiered",
		"gemini-4-argon", "gemini-4-carbon", "gemini-4-hydrogen",
		"kimi-k2.7-code-highspeed", "kimi-k3", "kimi-k3.1-pro", "kimi-k3.1-pro-highspeed",
	}
	if got := modelIDs(ms); !slices.Equal(got, want) {
		t.Fatalf("listed\n%v\nwant\n%v", got, want)
	}
	levels := map[string][]string{
		"gemini-3.8-flash":  {"low", "medium", "high"},
		"gemini-4-argon":    {"low", "medium", "high"},
		"gemini-4-carbon":   {"low", "medium", "high"},
		"gemini-4-hydrogen": {"low", "medium", "high", "xhigh"},
	}
	for _, m := range ms {
		if l, ok := levels[m.ID]; ok {
			if !slices.Equal(m.Efforts, l) {
				t.Errorf("%s: efforts %v, want %v", m.ID, m.Efforts, l)
			}
			if got := p.Efforts(m.ID); !slices.Equal(got, l) {
				t.Errorf("%s: Efforts() %v, want %v", m.ID, got, l)
			}
			// named as the family, not as the member it is built from
			if m.Name != m.ID {
				t.Errorf("%s is named %q", m.ID, m.Name)
			}
		}
	}

	// each effort goes as its own id; none asked, the medium; one the
	// relay lacks, its nearest
	for _, c := range []struct{ model, effort, want string }{
		{"gemini-4-argon", "low", "gemini-4-argon-low"},
		{"gemini-4-argon", "HIGH", "gemini-4-argon-high"},
		{"gemini-4-argon", "", "gemini-4-argon-medium"},
		{"gemini-4-argon", "xhigh", "gemini-4-argon-high"},
		{"gemini-4-argon", "max", "gemini-4-argon-high"},
		{"gemini-4-argon", "minimal", "gemini-4-argon-low"},
		{"gemini-4-argon", "none", "gemini-4-argon-low"},
		{"gemini-4-hydrogen", "max", "gemini-4-hydrogen-xhigh"},
		{"gemini-4-hydrogen", "xhigh", "gemini-4-hydrogen-xhigh"},
		{"gemini-3.8-flash", "", "gemini-3.8-flash-medium"},
		// an id of a member, picked before: as it is, at any effort
		{"gemini-4-argon-high", "low", "gemini-4-argon-high"},
		{"gemini-4-argon-low", "", "gemini-4-argon-low"},
		// the rest as they are
		{"gemini-3.8-flash-tiered", "high", "gemini-3.8-flash-tiered"},
		{"kimi-k2.7-code-highspeed", "low", "kimi-k2.7-code-highspeed"},
		{"gemini-3.1-pro-preview-low", "high", "gemini-3.1-pro-preview-low"},
	} {
		if got := p.SentIDAt(c.model, c.effort); got != c.want {
			t.Errorf("SentIDAt(%s, %q) = %s, want %s", c.model, c.effort, got, c.want)
		}
		if got := SentNameIn(nil, "gem", c.model, c.effort); got != c.want {
			t.Errorf("SentNameIn(%s, %q) = %s, want %s", c.model, c.effort, got, c.want)
		}
	}
	// the Test button and a key's check ask for the default's id
	if got := UpstreamName(p, "gemini-4-argon"); got != "gemini-4-argon-medium" {
		t.Errorf("UpstreamName = %s", got)
	}
	// a name the user gave every model of the provider is put on the id sent
	wires := map[string]string{"gem/*": "relay/*"}
	if got := SentNameIn(wires, "gem", "gemini-4-argon", "low"); got != "relay/gemini-4-argon-low" {
		t.Errorf("with a provider's wire: %s", got)
	}
	// a member's old id still resolves to the provider, as it was
	for _, ref := range []string{"gem/gemini-4-argon-high", "gem/gemini-4-argon"} {
		if q, m, ok := Resolve(ref); !ok || q.ID != "gem" || m != ref[len("gem/"):] {
			t.Errorf("Resolve(%s) = %v %s %v", ref, q.ID, m, ok)
		}
	}
	// a look at the family by its id finds its default's entry
	if got := p.liveByID("gemini-4-argon"); len(got) != 1 || got[0].ID != "gemini-4-argon" {
		t.Errorf("liveByID = %v", got)
	}
	// a pick of the family follows the list (dropGonePicks)
	if ids := liveIDs("gem"); !ids["gemini-4-argon"] || !ids["gemini-4-argon-high"] {
		t.Errorf("liveIDs lacks the family or a member: %v", ids)
	}
}

// The relay lists the model's own id too: that is what a request with no
// effort asks for, and the effort ids are its levels.
func TestRelayEffortFamilyWithItsOwnID(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "gem", Name: "Gemini", Key: "sk-x", Chat: "https://gem.example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	saveRelayList(t, "gem", "gemini-4-argon-low", "gemini-4-argon", "gemini-4-argon-High", "foo-9-high", "foo-8-low", "mistral-medium", "mistral-large", "vision-high", "vision-low", "gpt-5.1-codex-max", "gpt-5.1-codex-high")
	p := findProvider(t, "gem")
	want := []string{"gemini-4-argon", "foo-9-high", "foo-8-low", "mistral-medium", "mistral-large", "vision-high", "vision-low", "gpt-5.1-codex-max", "gpt-5.1-codex-high"}
	if got := modelIDs(p.Available()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if got := p.Efforts("gemini-4-argon"); !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("efforts %v", got)
	}
	for effort, want := range map[string]string{"": "gemini-4-argon", "low": "gemini-4-argon-low", "high": "gemini-4-argon-High", "medium": "gemini-4-argon-High"} {
		if got := p.SentIDAt("gemini-4-argon", effort); got != want {
			t.Errorf("at %q: %s, want %s", effort, got, want)
		}
	}
}

// A family whose member the user listed by an id of theirs, or whose id
// the user gave another model, stays as the relay lists it: their word
// on the list stands.
func TestRelayEffortFamilyKeepsTheUsersIDs(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "gem", Name: "Gemini", Key: "sk-x", Chat: "https://gem.example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	saveRelayList(t, "gem", "gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-carbon-high", "gemini-4-carbon-low")
	// named before the family was one model (SetModelID checks the
	// provider serves it, and a member of a family isn't listed)
	st := settings.Load()
	st.ModelIDs = map[string]string{"gem/carbon-lo": "gemini-4-carbon-low"}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	p := findProvider(t, "gem")
	want := []string{"gemini-4-argon", "gemini-4-carbon-high", "carbon-lo"}
	if got := modelIDs(p.Available()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if got := p.SentIDAt("gemini-4-carbon", "low"); got != "gemini-4-carbon" {
		t.Errorf("an unfolded family's id went as %s", got)
	}
}

// A provider magpie knows the vendor of (a preset's) lists as its vendor
// says, effort ids and all: only a relay the user added by its address
// is read for families.
func TestRelayEffortFamiliesOnlyOnTheUsersRelays(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "openrouter", Name: "OpenRouter", Preset: "openrouter", Key: "k", Chat: "https://openrouter.ai/api/v1"}); err != nil {
		t.Fatal(err)
	}
	saveRelayList(t, "openrouter", "gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-argon-medium")
	p := findProvider(t, "openrouter")
	if p.foldsEfforts() {
		t.Fatal("a preset's provider folds")
	}
	if got := modelIDs(p.Available()); !slices.Equal(got, []string{"gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-argon-medium"}) {
		t.Fatalf("listed %v", got)
	}
	if got := SentNameIn(nil, "openrouter", "gemini-4-argon", "high"); got != "gemini-4-argon" {
		t.Errorf("sent %s", got)
	}
}

func TestSplitRelayEffort(t *testing.T) {
	for id, want := range map[string]string{
		"gemini-4-argon-High":      "gemini-4-argon high",
		"gemini-4-hydrogen-xhigh":  "gemini-4-hydrogen xhigh",
		"vendor/glm-5-minimal":     "vendor/glm-5 minimal",
		"kimi-k2.7-code-highspeed": "",
		"gemini-3.5-flash-lite":    "",
		"gemini-3.8-flash-tiered":  "",
		"gemini-3.1-pro-preview":   "",
		"gpt-5.1-codex-max":        "",
		"qwen3.8-none":             "",
		"mistral-medium":           "",
		"vendor9/vision-high":      "",
		"high":                     "",
		"-high":                    "",
	} {
		b, l, ok := splitRelayEffort(id)
		got := ""
		if ok {
			got = b + " " + l
		}
		if got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}
