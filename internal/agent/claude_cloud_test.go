package agent

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// claudeCloudHome is claudeAboutHome with the managed settings in the
// test's home, empty, and no login shell to ask.
func claudeCloudHome(t *testing.T, settings string) (home, path string, parse func() map[string]any) {
	t.Helper()
	home, path, parse = claudeAboutHome(t, settings)
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	shell := claudeShellEnv
	claudeShellEnv = func(string) string { return "" }
	t.Cleanup(func() { claudeShellEnv = shell })
	return home, path, parse
}

// claudeClouded is each settings.json of a Claude Code on a cloud, as jw1dev
// on X had Bedrock: the switch on beside the cloud's own variables, and an
// endpoint and Bedrock main model of the user's own for magpie to give back.
var claudeClouded = []struct {
	name, settings, key, raw string
	cloud                    []string
}{
	{"bedrock", `{"model":"opus","env":{"ANTHROPIC_BASE_URL":"https://relay.example","ANTHROPIC_AUTH_TOKEN":"sk-relay",
		"CLAUDE_CODE_USE_BEDROCK":"1","AWS_REGION":"us-west-2","AWS_PROFILE":"work","AWS_BEARER_TOKEN_BEDROCK":"bedrock-key","ANTHROPIC_MODEL":"us.anthropic.claude-sonnet-4-5-20250929-v1:0"}}`,
		"CLAUDE_CODE_USE_BEDROCK", `"1"`, []string{"AWS_REGION", "AWS_PROFILE", "AWS_BEARER_TOKEN_BEDROCK"}},
	// a JSON true, which Claude Code takes as on too, comes back a true
	{"bedrock-bool", `{"env":{"CLAUDE_CODE_USE_BEDROCK":true,"AWS_REGION":"eu-central-1"}}`,
		"CLAUDE_CODE_USE_BEDROCK", `true`, []string{"AWS_REGION"}},
	{"vertex", `{"env":{"CLAUDE_CODE_USE_VERTEX":" TRUE ","CLOUD_ML_REGION":"us-east5","ANTHROPIC_VERTEX_PROJECT_ID":"my-proj"}}`,
		"CLAUDE_CODE_USE_VERTEX", `" TRUE "`, []string{"CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID"}},
	{"foundry", `{"env":{"CLAUDE_CODE_USE_FOUNDRY":"yes","ANTHROPIC_FOUNDRY_RESOURCE":"my-res","ANTHROPIC_FOUNDRY_API_KEY":"fk"}}`,
		"CLAUDE_CODE_USE_FOUNDRY", `"yes"`, []string{"ANTHROPIC_FOUNDRY_RESOURCE", "ANTHROPIC_FOUNDRY_API_KEY"}},
}

// jw1dev on X: magpie wired Claude Code (ANTHROPIC_BASE_URL and its model)
// while CLAUDE_CODE_USE_BEDROCK=1 stayed in the same env, so Claude Code
// went on to Bedrock, which refused magpie's model, and magpie showed
// nothing wrong. Routed through magpie, the switch is out and the cloud's
// own variables stay; through Sync too; and magpie stepping out, by
// Disconnect or Claude Code's own model, gives the switch back as written.
func TestClaudeCloudSwitchStepsAsideForMagpie(t *testing.T) {
	for _, c := range claudeClouded {
		for _, out := range []string{"disconnect", "default"} {
			t.Run(c.name+"/"+out, func(t *testing.T) {
				home, path, parse := claudeCloudHome(t, c.settings)
				want := parse()
				a := claude(home)
				if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
					t.Fatal(err)
				}
				routed := func(when string) {
					t.Helper()
					if v, has := edit.GetJSONRaw(path, "env."+c.key); has {
						t.Fatalf("%s: %s left at %s beside magpie's wiring:\n%s", when, c.key, v, readFile(path))
					}
					for _, k := range c.cloud {
						if _, has := edit.GetJSON(path, "env."+k); !has {
							t.Fatalf("%s: the cloud's own %s taken:\n%s", when, k, readFile(path))
						}
					}
					if d := a.Drift(); d != nil {
						t.Fatalf("%s: drift %+v", when, d)
					}
				}
				routed("connected")
				if err := a.Sync(); err != nil {
					t.Fatal(err)
				}
				routed("synced")
				// a model picked again goes through writeTiers once more
				if err := a.Apply("model", "opencode-go/deepseek-pro"); err != nil {
					t.Fatal(err)
				}
				routed("picked again")

				if out == "default" {
					if err := a.Apply("model", ""); err != nil {
						t.Fatal(err)
					}
				} else if err := a.Disconnect(); err != nil {
					t.Fatal(err)
				}
				if raw, _ := edit.GetJSONRaw(path, "env."+c.key); raw != c.raw {
					t.Fatalf("%s given back as %s, want %s:\n%s", c.key, raw, c.raw, readFile(path))
				}
				got := parse()
				if out == "default" {
					// Claude Code's own model: the one magpie took over
					// isn't the user's to have back
					delete(want, "model")
					delete(got, "model")
				}
				if !reflect.DeepEqual(got["env"], want["env"]) {
					t.Fatalf("env not given back:\n got %v\nwant %v", got["env"], want["env"])
				}
				if left := stashLoad()["claude.cloud."+c.key]; left != "" {
					t.Fatalf("the switch left in the stash: %q", left)
				}
			})
		}
	}
}

// A switch that is off ("0", "false", "") sends Claude Code nowhere else:
// it stays as written, and is no drift.
func TestClaudeCloudSwitchOffStays(t *testing.T) {
	home, path, parse := claudeCloudHome(t, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"0","CLAUDE_CODE_USE_VERTEX":"false","CLAUDE_CODE_USE_FOUNDRY":""}}`)
	want := parse()
	a := claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if _, has := edit.GetJSON(path, "env."+k); !has {
			t.Fatalf("%s, off, taken:\n%s", k, readFile(path))
		}
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift %+v", d)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got := parse(); !reflect.DeepEqual(got["env"], want["env"]) {
		t.Fatalf("env not given back:\n got %v\nwant %v", got["env"], want["env"])
	}
}

// A switch turned on in settings.json while Claude Code is on magpie is
// drift, saying which and where; magpie's next Sync takes it out (and
// Apply again would), and Disconnect gives it back. One the user writes
// again before that stays as they wrote it.
func TestClaudeCloudSwitchTurnedOnWhileRouted(t *testing.T) {
	home, path, _ := claudeCloudHome(t, `{}`)
	a := claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_USE_MANTLE", Value: "on"}); err != nil {
		t.Fatal(err)
	}
	d := a.Drift()
	if d == nil || !strings.Contains(d.Detail, "CLAUDE_CODE_USE_MANTLE") || !strings.Contains(d.Detail, "Amazon Bedrock (Mantle)") || !strings.Contains(d.Detail, path) {
		t.Fatalf("drift %+v, want one naming the switch, its cloud and %s", d, path)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, has := edit.GetJSON(path, "env.CLAUDE_CODE_USE_MANTLE"); has {
		t.Fatalf("Sync left the switch:\n%s", readFile(path))
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift after Sync %+v", d)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env.CLAUDE_CODE_USE_MANTLE"); v != "on" {
		t.Fatalf("switch given back as %q:\n%s", v, readFile(path))
	}

	// connected again, the user writes the switch anew, then disconnects:
	// theirs stays, the kept one isn't put over it
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_USE_MANTLE", Value: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env.CLAUDE_CODE_USE_MANTLE"); v != "0" {
		t.Fatalf("the user's own %q written over:\n%s", v, readFile(path))
	}
}

// A switch magpie doesn't write over, where Claude Code still reads it, is
// drift that says where: an administrator's managed settings (which win
// over the user's), ~/.claude.json's env (which the user's settings win
// over), and the login shell (which every settings file wins over).
func TestClaudeCloudSwitchElsewhereIsSaid(t *testing.T) {
	for _, c := range []struct {
		name                    string
		managed, settings, glob string
		shell                   map[string]string
		want                    string // "" for no drift
	}{
		{name: "managed", managed: `{"env":{"CLAUDE_CODE_USE_VERTEX":"1"}}`, want: "managed-settings.json sets CLAUDE_CODE_USE_VERTEX, so Claude Code sends its requests to Google Vertex AI"},
		{name: "managed over the user's off", managed: `{"env":{"CLAUDE_CODE_USE_VERTEX":"1"}}`, settings: `{"env":{"CLAUDE_CODE_USE_VERTEX":"0"}}`, want: "managed-settings.json sets CLAUDE_CODE_USE_VERTEX"},
		{name: "managed off", managed: `{"env":{"CLAUDE_CODE_USE_BEDROCK":"0"}}`, glob: `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`},
		{name: "claude.json", glob: `{"env":{"CLAUDE_CODE_USE_ANTHROPIC_AWS":"true"}}`, want: ".claude.json sets CLAUDE_CODE_USE_ANTHROPIC_AWS, so Claude Code sends its requests to Claude Platform on AWS"},
		{name: "claude.json under the user's off", glob: `{"env":{"CLAUDE_CODE_USE_ANTHROPIC_AWS":"true"}}`, settings: `{"env":{"CLAUDE_CODE_USE_ANTHROPIC_AWS":"0"}}`},
		{name: "shell", shell: map[string]string{"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD": "1"}, want: "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD is set in your shell's environment, so Claude Code started there sends its requests to Claude Platform on Google Cloud"},
		{name: "shell under the user's off", shell: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, settings: `{"env":{"CLAUDE_CODE_USE_BEDROCK":"false"}}`},
		{name: "shell off", shell: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, path, _ := claudeCloudHome(t, cmpOr(c.settings, `{}`))
			if c.managed != "" {
				writeFile(t, claudeManaged(), c.managed)
			}
			if c.glob != "" {
				writeFile(t, filepath.Join(home, ".claude.json"), c.glob)
			}
			claudeShellEnv = func(k string) string { return c.shell[k] }
			a := claude(home)
			if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
				t.Fatal(err)
			}
			d := a.Drift()
			if c.want == "" {
				if d != nil {
					t.Fatalf("drift %+v", d)
				}
				return
			}
			if d == nil || d.Kind != "outside" || !strings.Contains(d.Detail, c.want) {
				t.Fatalf("drift %+v, want one saying %q", d, c.want)
			}
			// the template's own words, its slots filled, for the GUI to
			// translate (tNotice)
			if !strings.HasSuffix(d.Detail, "to go through magpie.") {
				t.Fatalf("%q isn't one of the notices", d.Detail)
			}
			// where magpie can't write it, it doesn't: the file stays
			if c.managed != "" && !strings.Contains(readFile(claudeManaged()), `"1"`) {
				t.Fatalf("managed settings written: %s", readFile(claudeManaged()))
			}
			if c.glob != "" && !strings.Contains(readFile(filepath.Join(home, ".claude.json")), "CLAUDE_CODE_USE") {
				t.Fatalf(".claude.json written: %s", readFile(filepath.Join(home, ".claude.json")))
			}
			_ = path
		})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// A Claude Code in WSL keeps its switch under its own stash key, apart
// from this machine's, and has it given back there.
func TestClaudeCloudSwitchInWSL(t *testing.T) {
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Chat: "https://o.example/v1", Key: "k", Models: []string{"deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	shell := claudeShellEnv
	claudeShellEnv = func(string) string { return "1" } // a distro's isn't this shell's
	t.Cleanup(func() { claudeShellEnv = shell })
	d := distro{Name: "Ubuntu", Home: "/home/me", Root: t.TempDir(), Running: true, Mirrored: true}
	at := d.place("claude@wsl:Ubuntu")
	path := filepath.Join(at.claudeDir(), "settings.json")
	writeFile(t, path, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1","AWS_REGION":"us-west-2"}}`)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"CLAUDE_CODE_USE_BEDROCK":"true"}}`)
	a := claudeIn(at)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if _, has := edit.GetJSON(path, "env.CLAUDE_CODE_USE_BEDROCK"); has {
		t.Fatalf("left in WSL's settings:\n%s", readFile(path))
	}
	if v, _ := edit.GetJSON(path, "env.AWS_REGION"); v != "us-west-2" {
		t.Fatalf("AWS_REGION taken:\n%s", readFile(path))
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift %+v (a distro's Claude Code doesn't run in this machine's shell)", d)
	}
	if got := stashLoad()["claude@wsl:Ubuntu.cloud.CLAUDE_CODE_USE_BEDROCK"]; got != `"1"` {
		t.Fatalf("kept as %q under the distro's key; stash %v", got, stashLoad())
	}
	if v, _ := edit.GetJSON(filepath.Join(home, ".claude", "settings.json"), "env.CLAUDE_CODE_USE_BEDROCK"); v != "true" {
		t.Fatal("this machine's Claude Code touched")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := edit.GetJSONRaw(path, "env.CLAUDE_CODE_USE_BEDROCK"); raw != `"1"` {
		t.Fatalf("given back as %s:\n%s", raw, readFile(path))
	}
}

// T3 Code's magpie instance runs Claude Code with the user's ~/.claude
// settings env put over its own, so a switch on there takes it past magpie
// too: drift that says so. One the user put on magpie's own instance goes
// at its next write.
func TestT3CodeSaysAClaudeCloudSwitch(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	shell := claudeShellEnv
	claudeShellEnv = func(string) string { return "" }
	t.Cleanup(func() { claudeShellEnv = shell })
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, claudePath, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`)
	a := t3code(home)
	if a.Check() != "" {
		t.Fatal("drift before magpie's instance is there")
	}
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d == nil || d.Kind != "outside" || !strings.Contains(d.Detail, "CLAUDE_CODE_USE_BEDROCK") || !strings.Contains(d.Detail, "Amazon Bedrock") {
		t.Fatalf("drift %+v", d)
	}
	writeFile(t, claudePath, `{}`)
	if d := a.Drift(); d != nil {
		t.Fatalf("drift %+v with the switch gone", d)
	}

	// put on magpie's instance in T3's own editor
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	raw, _ := edit.GetJSON(path, t3Instance+".environment")
	var env []map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	env = append(env, map[string]any{"name": "CLAUDE_CODE_USE_VERTEX", "value": "1", "sensitive": false},
		map[string]any{"name": "MY_OWN", "value": "x", "sensitive": false})
	if err := edit.SetJSON(path, edit.KV{Path: t3Instance + ".environment", Value: env}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, _ = edit.GetJSON(path, t3Instance+".environment")
	for _, k := range agentenv.ClaudeProviders {
		if strings.Contains(raw, k) {
			t.Fatalf("%s left on magpie's instance: %s", k, raw)
		}
	}
	if !strings.Contains(raw, "MY_OWN") {
		t.Fatalf("the user's own variable taken: %s", raw)
	}
}
