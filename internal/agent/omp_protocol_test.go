package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// imty42 on X: omp's models.yml had magpie's provider on
// openai-completions with no way to change it — an api typed there was
// written back on the next sync, and omp's row had no protocol as Pi's
// has — so an auto group whose members don't all take Responses could
// only be asked on Chat. omp's row now picks the protocol: unpicked it is
// as before, a pick puts every chat model on it (an image model stays on
// openai-images), and an api typed into models.yml by hand stays and
// shows until a pick is made after it.
func TestOmpProtocolPicked(t *testing.T) {
	home := syncHome(t)
	was := ompVersion
	ompVersion = func() string { return "18.9.1" }
	t.Cleanup(func() { ompVersion = was })
	for _, p := range []provider.Provider{
		{ID: "oa", Name: "OA", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.1-sol"}},
		{ID: "an", Name: "An", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// a Chat-only member keeps the group on Chat unpicked
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"oa/gpt-6.1-sol", "relay/glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	a := omp(home)
	if err := a.Field("model").Set("magpie/group/mixed"); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(home, ".omp", "agent", "models.yml")
	f := a.Field("protocol")
	if f == nil {
		t.Fatal("omp has no protocol field")
	}
	read := func() (map[string]any, map[string]map[string]any) {
		t.Helper()
		b, err := os.ReadFile(models)
		if err != nil {
			t.Fatal(err)
		}
		var file map[string]any
		if err := yaml.Unmarshal(b, &file); err != nil {
			t.Fatal(err)
		}
		pm := file["providers"].(map[string]any)["magpie"].(map[string]any)
		ms := map[string]map[string]any{}
		for _, raw := range pm["models"].([]any) {
			m := raw.(map[string]any)
			ms[m["id"].(string)] = m
		}
		return pm, ms
	}
	gw := gateway.URL()
	sync := func() {
		t.Helper()
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}

	// unpicked: as before
	pm, ms := read()
	if v := f.Get(); v != "" || pm["api"] != "openai-completions" {
		t.Fatalf("unpicked: field %q, provider api %v", v, pm["api"])
	}
	if ms["group/mixed"]["api"] != nil || ms["oa/gpt-6.1-sol"]["api"] != "openai-responses" || ms["an/claude-sonnet-5"]["api"] != "anthropic-messages" {
		t.Fatalf("unpicked models: %v", ms)
	}

	// Responses: the provider's, every chat model on it with no api of its own
	if err := f.Set("openai-responses"); err != nil {
		t.Fatal(err)
	}
	sync()
	pm, ms = read()
	if pm["api"] != "openai-responses" || f.Get() != "openai-responses" {
		t.Fatalf("Responses picked: provider api %v, field %q", pm["api"], f.Get())
	}
	for id, m := range ms {
		if m["kind"] != nil {
			continue
		}
		if m["api"] != nil || m["baseUrl"] != nil {
			t.Errorf("%s on Responses has api %v at %v of its own", id, m["api"], m["baseUrl"])
		}
		if th, _ := m["thinking"].(map[string]any); th != nil && th["mode"] != "effort" {
			t.Errorf("%s on Responses thinks %v", id, th["mode"])
		}
	}

	// Messages: each at the gateway's root
	if err := f.Set("anthropic-messages"); err != nil {
		t.Fatal(err)
	}
	pm, ms = read()
	for _, id := range []string{"group/mixed", "oa/gpt-6.1-sol", "an/claude-sonnet-5"} {
		if ms[id]["api"] != "anthropic-messages" || ms[id]["baseUrl"] != gw {
			t.Errorf("%s on Messages: %v at %v", id, ms[id]["api"], ms[id]["baseUrl"])
		}
	}
	if pm["baseUrl"] != gw+"/v1" {
		t.Fatalf("provider baseUrl %v", pm["baseUrl"])
	}

	// back to each model's own
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	pm, ms = read()
	if pm["api"] != "openai-completions" || ms["oa/gpt-6.1-sol"]["api"] != "openai-responses" || ms["group/mixed"]["api"] != nil {
		t.Fatalf("each model's own: provider %v, models %v", pm["api"], ms)
	}

	// typed by hand: it stays through a sync, and shows
	if err := edit.SetYAML(models, edit.KV{Path: "providers.magpie.api", Value: "openai-responses"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if pm, _ = read(); pm["api"] != "openai-responses" || f.Get() != "openai-responses" {
		t.Fatalf("an api typed by hand: file %v, field %q", pm["api"], f.Get())
	}
	// a pick after it is the newer word
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	sync()
	if pm, _ = read(); pm["api"] != "openai-completions" || f.Get() != "" {
		t.Fatalf("picked after a hand value: file %v, field %q", pm["api"], f.Get())
	}

	if err := f.Set("google-generative-ai"); err == nil {
		t.Fatal("a protocol the gateway doesn't serve to omp was taken")
	}
}
