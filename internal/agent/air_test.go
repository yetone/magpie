package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// airHome is a sandbox home with Air's config folder and an OpenCode at a
// known place.
func airHome(t *testing.T, bin string) (home, dir string) {
	home, _ = codexHome(t, "", "")
	dir = airDir(home, filepath.Join(home, ".config"))
	was := airOpenCode
	airOpenCode = func(string) string { return bin }
	t.Cleanup(func() { airOpenCode = was })
	return home, dir
}

// Air's model menu is the ACP agent's: magpie adds one, Magpie, to
// acp.json beside the user's own agents — OpenCode's ACP server on a config
// of magpie's that has magpie's provider alone — and taking it off takes
// out that entry and that file, the user's agents kept.
func TestAirAddsMagpieAgent(t *testing.T) {
	home, dir := airHome(t, "/opt/bin/opencode")
	acp := filepath.Join(dir, "acp.json")
	writeFile(t, acp, `{
    "agent_servers": {
        "Goose": {"command": "goose", "args": ["acp"], "env": {}}
    }
}
`)
	a := air(home, filepath.Join(home, ".config"))
	if !a.Detected() || a.Path != acp {
		t.Fatalf("detected %v, path %s", a.Detected(), a.Path)
	}
	if v := a.Field("model").Get(); v != "" {
		t.Fatalf("model before = %q", v)
	}
	opts := a.Field("model").Options(nil)
	want := ""
	for _, o := range opts {
		if o.Ref == "fake/m1" {
			want = o.Value
		}
	}
	if want != "magpie/fake/m1" {
		t.Fatalf("options: %+v", opts)
	}
	if err := a.Apply("model", want); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "magpie-opencode.json")
	for k, v := range map[string]string{
		"agent_servers.Goose.command":              "goose",
		"agent_servers.Magpie.command":             "/opt/bin/opencode",
		"agent_servers.Magpie.args.0":              "acp",
		"agent_servers.Magpie.env.OPENCODE_CONFIG": cfg,
	} {
		if got, _ := edit.GetJSON(acp, k); got != v {
			t.Fatalf("%s = %q, want %q\n%s", k, got, v, readFile(acp))
		}
	}
	for k, v := range map[string]string{
		"model":                           "magpie/fake/m1",
		"small_model":                     "magpie/fake/m1",
		"enabled_providers":               `["magpie"]`,
		"provider.magpie.options.baseURL": gatewayV1(),
		"provider.magpie.options.apiKey":  "magpie",
		"provider.magpie.npm":             "@ai-sdk/openai-compatible",
	} {
		if got, _ := edit.GetJSON(cfg, k); strings.Join(strings.Fields(got), "") != v {
			t.Fatalf("%s = %q, want %q\n%s", k, got, v, readFile(cfg))
		}
	}
	if _, ok := edit.GetJSON(cfg, `provider.magpie.models.fake/m1`); !ok {
		t.Fatalf("no fake/m1 in magpie's models: %s", readFile(cfg))
	}
	if v := a.Field("model").Get(); v != want {
		t.Fatalf("model = %q", v)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	if n := a.Notice(); !strings.Contains(n, "pick Magpie") {
		t.Fatalf("notice: %q", n)
	}
	// one of Air's own agents isn't set from here
	if err := a.Apply("model", "anthropic/claude-sonnet-5"); err == nil {
		t.Fatal("took a model that isn't magpie's")
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(acp, "agent_servers.Magpie"); ok {
		t.Fatalf("Magpie still in acp.json: %s", readFile(acp))
	}
	if v, _ := edit.GetJSON(acp, "agent_servers.Goose.command"); v != "goose" {
		t.Fatalf("the user's agent went: %s", readFile(acp))
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("magpie's OpenCode config left: %v", err)
	}
	if v := a.Field("model").Get(); v != "" {
		t.Fatalf("model after = %q", v)
	}
}

// With no acp.json yet (Air makes it on the first "Edit ACP JSON File"),
// magpie writes one; without OpenCode found it names it by its command and
// the notice says to install it.
func TestAirWithoutOpenCode(t *testing.T) {
	home, dir := airHome(t, "")
	os.MkdirAll(dir, 0o755)
	a := air(home, filepath.Join(home, ".config"))
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	acp := filepath.Join(dir, "acp.json")
	if v, _ := edit.GetJSON(acp, "agent_servers.Magpie.command"); v != "opencode" {
		t.Fatalf("command = %q\n%s", v, readFile(acp))
	}
	if n := a.Notice(); !strings.Contains(n, "install OpenCode") {
		t.Fatalf("notice: %q", n)
	}
}

// Magpie's entry taken out of acp.json by hand while its config still
// names one of magpie's models is wiring gone, which setting it again puts
// back; providers added later reach the config's model list, and Sync
// writes no config magpie never set up.
func TestAirCheckAndSync(t *testing.T) {
	home, dir := airHome(t, "/opt/bin/opencode")
	a := air(home, filepath.Join(home, ".config"))
	acp, cfg := filepath.Join(dir, "acp.json"), filepath.Join(dir, "magpie-opencode.json")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("Sync wrote a config magpie never set up")
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake 2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(cfg, `provider.magpie.models.fake2/m2`); !ok {
		t.Fatalf("a provider added later isn't listed: %s", readFile(cfg))
	}
	if err := edit.DelJSON(acp, "agent_servers.Magpie"); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d == nil || d.Kind != "unwired" || !strings.Contains(d.Detail, "Magpie agent") {
		t.Fatalf("drift: %+v", d)
	}
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(acp, "agent_servers.Magpie.env.OPENCODE_CONFIG"); v != cfg {
		t.Fatalf("not put back: %s", readFile(acp))
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift after setting it again: %+v", d)
	}
}

// Sync and a model pick keep the user's provider fields, while magpie's
// endpoint, key and complete model list still follow the gateway.
func TestAirProviderWritesKeepCustomFields(t *testing.T) {
	for _, action := range []string{"sync", "pick"} {
		t.Run(action, func(t *testing.T) {
			home, dir := airHome(t, "/opt/bin/opencode")
			a := air(home, filepath.Join(home, ".config"))
			cfg := filepath.Join(dir, "magpie-opencode.json")
			if err := a.Apply("model", "magpie/fake/m1"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, cfg, `{
  // the user's configuration
  "theme": "dark",
  "model": "magpie/fake/m1",
  "provider": {
    "mine": {"name": "mine"},
    "magpie": {
      "name": "old",
      "npm": "old",
      "options": {
        "baseURL": "http://127.0.0.1:1/v1",
        "apiKey": "old-key",
        "timeout": 12345,
        "headers": {"z": "last", "a": "first"}
      },
      "compat": {"sendSessionAffinityHeaders": true, "supportsLongCacheRetention": false},
      "marker": {"z": 9007199254740993, "a": null},
      "models": {"fake/m1": {"obsolete": true}, "removed/m0": {}}
    }
  }
}
`)
			kept := map[string]string{}
			for _, field := range []string{"compat", "marker", "options.timeout", "options.headers"} {
				kept[field], _ = edit.GetJSON(cfg, "provider.magpie."+field)
			}
			check := func() {
				t.Helper()
				for field, was := range kept {
					if v, ok := edit.GetJSON(cfg, "provider.magpie."+field); !ok || !sameOrder(v, json.RawMessage(was)) {
						t.Errorf("custom %s lost or changed: got %q, want %q", field, v, was)
					}
				}
				if !strings.Contains(readFile(cfg), "// the user's configuration") {
					t.Error("the user's comment was removed")
				}
				for k, want := range map[string]string{"theme": "dark", "provider.mine.name": "mine"} {
					if got, _ := edit.GetJSON(cfg, k); got != want {
						t.Errorf("%s = %q, want %q", k, got, want)
					}
				}
				want := magpieProviderJSONFor("opencode", "air").(map[string]any)
				for k, v := range want["options"].(map[string]any) {
					if got, _ := edit.GetJSON(cfg, "provider.magpie.options."+k); got != v {
						t.Errorf("options.%s = %q, want %v", k, got, v)
					}
				}
				for _, k := range []string{"name", "npm", "models"} {
					got, _ := edit.GetJSON(cfg, "provider.magpie."+k)
					if k != "models" {
						b, _ := json.Marshal(got)
						got = string(b)
					}
					if !sameOrder(got, want[k]) {
						t.Errorf("managed %s is stale: %s", k, got)
					}
				}
			}
			if action == "sync" {
				if err := a.Sync(); err != nil {
					t.Fatal(err)
				}
			} else if err := a.Apply("model", "magpie/fake/m1"); err != nil {
				t.Fatal(err)
			}
			check()
			// The old catalog entry must go when the provider changes.
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
				t.Fatal(err)
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			check()
			before := readFile(cfg)
			stamp := time.Unix(1000000000, 0)
			if err := os.Chtimes(cfg, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if readFile(cfg) != before || !stat.ModTime().Equal(stamp) {
				t.Error("unchanged provider was rewritten")
			}
		})
	}
}
