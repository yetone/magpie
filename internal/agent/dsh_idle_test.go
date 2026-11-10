package agent

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// dshRouteIdle is streamIdleTimeoutMs of each of magpie's routes in a
// llm-pi-ai providers mapping at path (a patch list's entry, or
// settings.yaml's section), 0 for a route without one.
func dshRouteIdle(t *testing.T, path string, list bool) map[string]int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type route struct {
		Idle int `yaml:"streamIdleTimeoutMs"`
	}
	var providers map[string]route
	if list {
		var items []struct {
			ID     string `yaml:"id"`
			Config struct {
				Providers map[string]route `yaml:"providers"`
			} `yaml:"config"`
		}
		if err := yaml.Unmarshal(b, &items); err != nil {
			t.Fatalf("%v:\n%s", err, b)
		}
		for _, it := range items {
			if it.ID == dshPiRow {
				providers = it.Config.Providers
			}
		}
	} else {
		var doc map[string]struct {
			Providers map[string]route `yaml:"providers"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%v:\n%s", err, b)
		}
		providers = doc[dshPiRow].Providers
	}
	out := map[string]int{}
	for k, r := range providers {
		if isDshRoute(k) {
			out[k] = r.Idle
		}
	}
	if len(out) == 0 {
		t.Fatalf("no route of magpie's in %s:\n%s", path, b)
	}
	return out
}

// tim on Discord: a first "who are you" to Gemini through the
// magpie-gemini-web plugin took minutes in dsh and ended in 499.
// llm-pi-ai gives a turn up after 5 minutes with no pi-ai event, and the
// gateway's keepalive comments aren't events: a turn queued behind dsh's
// own title request on a plugin serving one request at a time was cut
// while the gateway (10 minutes for headers) and the plugin still waited.
// magpie's routes, in every profile and in deepseek-acp's settings.yaml,
// now wait as long as the gateway does.
func TestDshRoutesWaitAsLongAsTheGateway(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	a := dsh(home)
	if err := a.Pick("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		path string
		list bool
	}{{web, true}, {filepath.Join(dir, "settings.yaml"), false}} {
		for k, ms := range dshRouteIdle(t, f.path, f.list) {
			if ms != dshIdleWait {
				t.Fatalf("%s: route %s waits %dms for the next event, want %d", f.path, k, ms, dshIdleWait)
			}
		}
	}
	if dshIdleWait != 10*60*1000 {
		t.Fatalf("dshIdleWait %d is not the gateway's 10 minutes for a reply's headers", dshIdleWait)
	}
}

// A wait set on magpie's route in dsh (or by hand), shorter or longer, is
// the user's: a sync keeps it.
func TestDshRouteKeepsTheUsersIdleWait(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: "+dshKeyRef+"\n        api: openai-completions\n        baseURL: "+gatewayV1()+"\n        streamIdleTimeoutMs: 120000\n        models:\n          - id: deepseek/pro\n"+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/pro\n"), 0o644)
	a := dsh(home)
	for i := 0; i < 2; i++ {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := dshRouteIdle(t, web, true)[dshRoute]; got != 120000 {
			b, _ := os.ReadFile(web)
			t.Fatalf("sync %d: the user's 120000 became %d:\n%s", i+1, got, b)
		}
	}
}
