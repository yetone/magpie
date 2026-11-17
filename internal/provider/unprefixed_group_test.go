package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// unprefixedHome is a sandbox HOME with models.dev's Kimi entries
// (testdata/kimi_code_catalog.json) as the catalog, and ps saved in order.
func unprefixedHome(t *testing.T, ps ...Provider) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b, err := os.ReadFile(filepath.Join("testdata", "kimi_code_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// a relay's catalog listing a k3.1-pro of its own, which models.dev
	// says nothing more of
	m["relay"] = map[string]any{"id": "relay", "models": map[string]any{
		"k3.1-pro": map[string]any{"id": "k3.1-pro", "name": "K3.1 Pro"}}}
	b, _ = json.Marshal(m)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range ps {
		p.Name, p.Key, p.Chat = p.ID, "k", "http://127.0.0.1:1/v1"
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func membersOf(t *testing.T, id string) []string {
	t.Helper()
	for _, g := range Groups() {
		if g.ID == id {
			return g.Members
		}
	}
	return nil
}

// Kimi Code lists Kimi K3.1 Pro as k3.1-pro, and OpenCode Go and Devin
// serve it as kimi-k3.1-pro: the group magpie found for the model left
// Kimi Code out until it was added by hand (tmr11235 on X, v0.1.4334).
// models.dev's entry for Kimi Code's k3.1-pro names moonshotai/kimi-k3.1-pro
// as the model, so it is the group's. Its other entries name a model that
// is not theirs (k3.1-pro-highspeed's is kimi-k3.1-pro, k3-256k's kimi-k3),
// so only an id that is the model's with the maker's name taken off counts:
// k3.1-pro-highspeed joins kimi-k3.1-pro-highspeed's group, never K3.1
// Pro's, k3-256k stays its own, and no K3.1 or K3 takes K3.1 Pro.
func TestFoundGroupTakesAModelListedWithoutItsMaker(t *testing.T) {
	unprefixedHome(t,
		Provider{ID: "kc", Catalog: "kimi-code-plan-global", Models: []string{"k3", "k3-256k", "k3.1-pro", "k3.1-pro-highspeed", "kimi-for-coding"}},
		Provider{ID: "ocg", Models: []string{"kimi-k3", "kimi-k3.1", "kimi-k3.1-pro", "kimi-k3.1-pro-highspeed", "kimi-k3-256k-preview"}},
		Provider{ID: "dv", Models: []string{"kimi-k3.1-pro"}},
		// a relay listing its own k3.1-pro, whose catalog says nothing of
		// what it is: no group takes it on a guess
		Provider{ID: "rl", Catalog: "relay", Models: []string{"k3.1-pro"}},
	)
	for gid, want := range map[string][]string{
		"auto-kimi-k3-1-pro":           {"kc/k3.1-pro", "ocg/kimi-k3.1-pro", "dv/kimi-k3.1-pro"},
		"auto-kimi-k3":                 {"kc/k3", "ocg/kimi-k3"},
		"auto-kimi-k3-1-pro-highspeed": {"kc/k3.1-pro-highspeed", "ocg/kimi-k3.1-pro-highspeed"},
	} {
		got := membersOf(t, gid)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: members %v, want %v", gid, got, want)
		}
	}
	if ms := membersOf(t, "auto-k3-1-pro"); ms != nil {
		t.Errorf("the relay's k3.1-pro and Kimi Code's form a group of their own: %v", ms)
	}
	g := foundGroup(t, "auto-kimi-k3-1-pro")
	if g.Ref() != "auto/kimi-k3.1-pro" || g.Name != "Kimi K3.1 Pro" {
		t.Errorf("the group is %q named %q", g.Ref(), g.Name)
	}
	for model, want := range map[string]string{
		"k3.1-pro": "auto-kimi-k3-1-pro", "K3.1-Pro": "auto-kimi-k3-1-pro", "k3": "auto-kimi-k3",
		"k3-256k": "auto-k3-256k", "k3.1-pro-highspeed": "auto-kimi-k3-1-pro-highspeed",
		"kimi-for-coding": "auto-kimi-for-coding",
	} {
		if got := AutoGroupOf("kc", model); got != want {
			t.Errorf("AutoGroupOf(kc, %s) = %s, want %s", model, got, want)
		}
	}
	if got := AutoGroupOf("rl", "k3.1-pro"); got != "auto-k3-1-pro" {
		t.Errorf("AutoGroupOf(rl, k3.1-pro) = %s: a catalog that says nothing moved it", got)
	}
}

// Kimi Code and Kimi Code (China) alone, both listing k3.1-pro: the group
// they make is still listed by the model's id, auto/kimi-k3.1-pro.
func TestFoundGroupOfUnprefixedIDsAloneIsTheModels(t *testing.T) {
	unprefixedHome(t,
		Provider{ID: "kc", Catalog: "kimi-code-plan-global", Models: []string{"k3.1-pro"}},
		Provider{ID: "kccn", Catalog: "kimi-code-plan-cn", Models: []string{"k3.1-pro"}},
	)
	g := foundGroup(t, "auto-kimi-k3-1-pro")
	if g.Ref() != "auto/kimi-k3.1-pro" {
		t.Errorf("the group is %q", g.Ref())
	}
	// the id the two made a group by before, which an agent may be set to,
	// is still served, by this group
	for _, id := range []string{"auto/k3.1-pro", "group/auto-k3-1-pro", "auto/kimi-k3.1-pro"} {
		p, m, ok := Resolve(id)
		if !ok || p.ID == "" {
			t.Errorf("%s isn't served", id)
			continue
		}
		if m != "k3.1-pro" {
			t.Errorf("%s goes to %s/%s", id, p.ID, m)
		}
	}
}

// The same with Kimi Code as magpie adds it, from its preset.
func TestFoundGroupTakesKimiCodesPreset(t *testing.T) {
	for _, preset := range []string{"kimi-code", "kimi-code-cn"} {
		t.Run(preset, func(t *testing.T) {
			kc, err := FromPreset(preset)
			if err != nil {
				t.Fatal(err)
			}
			kc.Models = []string{"k3.1-pro", "k3"}
			unprefixedHome(t, kc, Provider{ID: "ocg", Models: []string{"kimi-k3.1-pro", "kimi-k3.1"}})
			got := membersOf(t, "auto-kimi-k3-1-pro")
			if want := []string{preset + "/k3.1-pro", "ocg/kimi-k3.1-pro"}; !slices.Equal(got, want) {
				t.Errorf("members %v, want %v", got, want)
			}
		})
	}
}
