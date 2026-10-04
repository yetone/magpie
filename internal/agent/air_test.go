package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
