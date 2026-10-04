package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #750: Codex put on the group magpie found for gpt-6.1-sol
// (group/auto-gpt-6-1-sol), and its config.toml then naming the model as
// vendors spell it, gpt-6.1-sol, with openai_base_url still magpie's: the
// gateway takes gpt-6.1-sol as that same group, so Codex is connected, not
// "switched off group/auto-gpt-6-1-sol outside magpie". A model of
// another group, or of none, is still drift.
func TestCodexGroupByItsModelName(t *testing.T) {
	for _, auth := range []string{`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""} {
		home, read := codexHome(t, auth, "")
		for _, p := range []provider.Provider{
			{ID: "ra", Name: "RA", Chat: "https://ra.example/v1", Key: "k", Models: []string{"gpt-6.1-sol", "gpt-6.1"}},
			{ID: "rb", Name: "RB", Chat: "https://rb.example/v1", Key: "k", Models: []string{"gpt-6.1-sol", "gpt-6.1"}},
		} {
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
		}
		if g, ok := provider.GroupFor("gpt-6.1-sol"); !ok || g != "group/auto-gpt-6-1-sol" {
			t.Fatalf("GroupFor: %q %v", g, ok)
		}
		cx := codex(home)
		if err := cx.Apply("model", "group/auto-gpt-6-1-sol"); err != nil {
			t.Fatal(err)
		}
		if d := cx.Drift(); d != nil {
			t.Fatalf("drift right after a set: %+v", d)
		}
		path := filepath.Join(home, ".codex", "config.toml")
		cfg := read()
		if !strings.Contains(cfg, `model = "group/auto-gpt-6-1-sol"`) {
			t.Fatalf("set:\n%s", cfg)
		}
		// Codex names the group's model by its own name, nothing else changed
		os.WriteFile(path, []byte(strings.Replace(cfg, `model = "group/auto-gpt-6-1-sol"`, `model = "gpt-6.1-sol"`, 1)), 0o644)
		if !cx.Wired() {
			t.Errorf("auth %q: not wired on gpt-6.1-sol:\n%s", auth, read())
		}
		if d := cx.Drift(); d != nil {
			t.Errorf("auth %q: drift on gpt-6.1-sol, the same group: %+v", auth, d)
		}
		// with nothing sending it to magpie, gpt-6.1-sol goes to OpenAI
		os.WriteFile(path, []byte("model = \"gpt-6.1-sol\"\n"), 0o644)
		if cx.Wired() {
			t.Errorf("auth %q: wired on gpt-6.1-sol with no route to magpie", auth)
		}
		// another group's model is not what magpie set
		os.WriteFile(path, []byte(strings.Replace(cfg, `model = "group/auto-gpt-6-1-sol"`, `model = "gpt-6.1"`, 1)), 0o644)
		if d := cx.Drift(); d == nil || d.Kind != "replaced" || d.Now != "gpt-6.1" {
			t.Errorf("auth %q: gpt-6.1, another group: %+v", auth, d)
		}
		// nor is one no group has
		os.WriteFile(path, []byte(strings.Replace(cfg, `model = "group/auto-gpt-6-1-sol"`, `model = "gpt-5.5"`, 1)), 0o644)
		if d := cx.Drift(); d == nil || d.Kind != "replaced" || d.Now != "gpt-5.5" {
			t.Errorf("auth %q: gpt-5.5: %+v", auth, d)
		}
	}
}

// Disconnected on the group's model name, Codex is taken off it as off the
// group's id: back to its default, not left asking OpenAI for gpt-6.1-sol.
func TestCodexGroupByItsModelNameDisconnect(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	for _, id := range []string{"ra", "rb"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: "https://" + id + ".example/v1", Key: "k", Models: []string{"gpt-6.1-sol"}}); err != nil {
			t.Fatal(err)
		}
	}
	cx := codex(home)
	if err := cx.Apply("model", "group/auto-gpt-6-1-sol"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	os.WriteFile(path, []byte(strings.Replace(read(), `model = "group/auto-gpt-6-1-sol"`, `model = "gpt-6.1-sol"`, 1)), 0o644)
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "gpt-6.1-sol") || strings.Contains(cfg, "openai_base_url") || cx.Wired() {
		t.Fatalf("disconnected:\n%s", cfg)
	}
}
