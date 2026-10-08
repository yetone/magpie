package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// claudeAboutSetup is #1227's settings.json before magpie: every tier on the
// user's own model, each named for it, two with a description and
// capabilities of their own.
const claudeAboutSetup = `{"model":"opus","env":{
	"ANTHROPIC_BASE_URL":"https://relay.example","ANTHROPIC_AUTH_TOKEN":"sk-relay",
	"ANTHROPIC_DEFAULT_FABLE_MODEL":"grok-4.5","ANTHROPIC_DEFAULT_FABLE_MODEL_NAME":"grok-4.5",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":"grok-4.5","ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME":"grok-4.5",
	"ANTHROPIC_DEFAULT_OPUS_MODEL":"grok-4.5","ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":"grok-4.5",
	"ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION":"Grok on my relay","ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES":"effort,thinking",
	"ANTHROPIC_DEFAULT_SONNET_MODEL":"grok-4.5","ANTHROPIC_DEFAULT_SONNET_MODEL_NAME":"grok-4.5",
	"ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION":"Grok on my relay"}}`

func claudeAboutHome(t *testing.T, settings string) (home, path string, parse func() map[string]any) {
	t.Helper()
	home, _ = codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Chat: "https://o.example/v1", Key: "k", Models: []string{"deepseek-flash", "deepseek-pro"}}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, settings)
	parse = func() map[string]any {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	return home, path, parse
}

// claudeAboutLeft is what the env still says about a tier's model.
func claudeAboutLeft(path string) map[string]string {
	out := map[string]string{}
	for _, k := range claudeAboutEnv {
		if v, has := edit.GetJSON(path, "env."+k); has {
			out[k] = v
		}
	}
	return out
}

// Routed through magpie, the names, descriptions and capabilities the user
// wrote for their own tier models no longer name the model each tier runs
// (#1227: every tier on deepseek-flash, shown as grok-4.5): they go while
// magpie's models are there, and come back with the user's models when
// magpie steps out, by its default or by Disconnect.
func TestClaudeTierNamesGoWithTheirModel(t *testing.T) {
	for _, out := range []string{"default", "disconnect"} {
		t.Run(out, func(t *testing.T) {
			home, path, parse := claudeAboutHome(t, claudeAboutSetup)
			want := parse()
			a := claude(home)
			if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
				t.Fatal(err)
			}
			if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL"); v != "opencode-go/deepseek-flash" {
				t.Fatalf("fable not on magpie's model:\n%s", readFile(path))
			}
			if left := claudeAboutLeft(path); len(left) > 0 {
				t.Fatalf("the user's own models' names left on magpie's: %v", left)
			}
			if out == "default" {
				if err := a.Apply("model", ""); err != nil {
					t.Fatal(err)
				}
				delete(want, "model")
			} else if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if got := parse(); !reflect.DeepEqual(got, want) {
				t.Fatalf("not given back:\n%s", readFile(path))
			}
		})
	}
}

// Claude Code routed by an older magpie, which left the names of the
// models before it in place (#1227 as reported): magpie's next look takes
// them out and keeps them to give back.
func TestClaudeTierNamesLeftByAnOlderMagpie(t *testing.T) {
	home, path, _ := claudeAboutHome(t, `{"model":"opus"}`)
	a := claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	var kvs []edit.KV
	for _, tier := range claudeTiers {
		names[tierEnv(tier)+"_NAME"] = "grok-4.5"
		kvs = append(kvs, edit.KV{Path: "env." + tierEnv(tier) + "_NAME", Value: "grok-4.5"})
	}
	if err := edit.SetJSON(path, kvs...); err != nil {
		t.Fatal(err)
	}
	// as an older magpie left it: nothing kept of having looked
	forget("claude.tier_about")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if left := claudeAboutLeft(path); len(left) > 0 {
		t.Fatalf("the names before magpie are left: %v", left)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := claudeAboutLeft(path); !reflect.DeepEqual(got, names) {
		t.Fatalf("not given back: %v\n%s", got, readFile(path))
	}
}

// A name the user writes for magpie's model is theirs: it stays while the
// tier is on that model, whatever else magpie writes, and goes when the
// tier moves to another.
func TestClaudeOwnTierNameOnMagpie(t *testing.T) {
	home, path, _ := claudeAboutHome(t, `{"model":"opus"}`)
	a := claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("opus", "opencode-go/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path,
		edit.KV{Path: "env.ANTHROPIC_DEFAULT_OPUS_MODEL_NAME", Value: "DeepSeek Pro"},
		edit.KV{Path: "env.ANTHROPIC_DEFAULT_SONNET_MODEL_NAME", Value: "DeepSeek Flash"},
	); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("opus_effort", "high"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("haiku", "opencode-go/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "DeepSeek Pro", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "DeepSeek Flash"}
	if got := claudeAboutLeft(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("the user's names for magpie's models: %v\n%s", got, readFile(path))
	}
	// the main model moves: sonnet, following it, moves too; opus stays
	if err := a.Apply("model", "opencode-go/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "DeepSeek Pro"}
	if got := claudeAboutLeft(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("after sonnet moved to pro: %v\n%s", got, readFile(path))
	}
	if err := a.Apply("opus", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if got := claudeAboutLeft(path); len(got) > 0 {
		t.Fatalf("opus moved to flash, still named: %v", got)
	}
}
