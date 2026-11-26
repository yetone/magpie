package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// ocEffortOptions is the effort field's offer for the agent's model now.
func ocEffortOptions(t *testing.T, a *Agent) []string {
	t.Helper()
	f := a.Field("effort")
	if f == nil {
		t.Fatal("OpenCode has no effort field")
	}
	var out []string
	for _, o := range f.Options(a.Values()) {
		out = append(out, o.Value)
	}
	return out
}

// ocHasMagpie says the OpenCode config at path has magpie's provider, in
// either OpenCode's words.
func ocHasMagpie(path string) bool {
	_, one := edit.GetJSONRaw(path, "provider."+magpieID)
	_, two := edit.GetJSONRaw(path, openCodeNativeKey)
	return one || two
}

// 0xRichardH on X: OpenCode 2 started every session of magpie's models at
// the model's default, with no way to say a level from magpie. OpenCode 2
// takes a default variant after the model, "provider/model#variant"
// (ConfigModel.Selection, 2.3.2), which its TUI starts a session at unless
// the user picked one there (local.tsx, preferredSelection).
func TestOpenCode2EffortIsTheModelsVariant(t *testing.T) {
	home, _ := codexHome(t, "", "")
	thinkCatalog(t)
	openCodeAt(t, "2.3.2")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	// the levels OpenCode offers for it: magpie's variants, weakest first
	if got := ocEffortOptions(t, oc); !slices.Equal(got, []string{"none", "high", "max"}) {
		t.Fatalf("effort options = %v, want deep's variants none, high, max", got)
	}
	if err := oc.Apply("effort", "high"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(oc.Path, "model"); m != "magpie/think/deep#high" {
		t.Fatalf("model = %q, want magpie/think/deep#high\n%s", m, readFile(oc.Path))
	}
	if v := oc.Values(); v["model"] != "magpie/think/deep" || v["effort"] != "high" {
		t.Fatalf("values = %v, want the model without its variant and effort high", v)
	}
	// picking the model again keeps the level
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(oc.Path, "model"); m != "magpie/think/deep#high" {
		t.Fatalf("model picked again = %q, want its variant kept", m)
	}
	// a model without that level goes without it: OpenCode would refuse it
	if err := oc.Apply("model", "magpie/think/new"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(oc.Path, "model"); m != "magpie/think/new" {
		t.Fatalf("model = %q, want magpie/think/new with no variant", m)
	}
	if got := ocEffortOptions(t, oc); len(got) != 0 {
		t.Fatalf("effort options for a model with no levels = %v", got)
	}
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("effort", "max"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("effort", ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(oc.Path, "model"); m != "magpie/think/deep" {
		t.Fatalf("effort cleared: model = %q, want no variant", m)
	}
	if _, ok := edit.GetJSON(oc.Path, "agent"); ok {
		t.Fatalf("OpenCode 1's agent written for OpenCode 2:\n%s", readFile(oc.Path))
	}
}

// A variant the user put after one of magpie's models in OpenCode 2's
// config is theirs: the model still reads as magpie's, its provider is
// written for it, and nothing magpie does on its own takes the variant out.
func TestOpenCode2KeepsTheUsersVariant(t *testing.T) {
	home, _ := codexHome(t, "", "")
	thinkCatalog(t)
	openCodeAt(t, "2.3.2")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := os.MkdirAll(oc.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oc.Path, []byte(`{"model": "magpie/think/deep#none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if oc.Spelled == nil || !oc.Spelled("magpie/think/deep#none") {
		t.Fatal("a model with a variant doesn't read as magpie's")
	}
	if v := oc.Values(); v["model"] != "magpie/think/deep" || v["effort"] != "none" {
		t.Fatalf("values = %v, want magpie/think/deep at none", v)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if !ocHasMagpie(oc.Path) {
		t.Fatalf("magpie's provider not written for a model with a variant:\n%s", readFile(oc.Path))
	}
	if m, _ := edit.GetJSON(oc.Path, "model"); m != "magpie/think/deep#none" {
		t.Fatalf("Sync took the user's variant: model = %q", m)
	}
	// the ref OpenCode's and OpenChamber's pickers write: the provider is put
	// in for it, and the variant stays
	path := filepath.Join(t.TempDir(), "opencode.json")
	v, err := openCodeRef(path, "opencode", "magpie/think/deep#high")
	if err != nil {
		t.Fatal(err)
	}
	if v != "magpie/think/deep#high" {
		t.Fatalf("openCodeRef = %q, want the variant kept", v)
	}
	if !ocHasMagpie(path) {
		t.Fatalf("openCodeRef wrote no provider for a model with a variant:\n%s", readFile(path))
	}
	// a provider of the file's own that sends to magpie's gateway names the
	// model, variant and all
	own := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(own, []byte(`{"provider": {"mine": {"options": {"baseURL": "`+gatewayV1()+`"}, "models": {"think/deep": {}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := openCodeRef(own, "opencode", "magpie/think/deep#high"); err != nil || v != "mine/think/deep#high" {
		t.Fatalf("openCodeRef with a provider of the file's own = %q, %v, want mine/think/deep#high", v, err)
	}
}

// OpenCode 1 takes no variant after the model (1.19.6 drops one as
// unsupported, earlier ones take it for part of the model's id). It takes
// an agent's: agent.<name>.variant, used when the agent's model is the one
// asked (session/prompt.ts, createUserMessage), so magpie writes both for
// its primary agents, build and plan, and leaves an agent with a model of
// the user's own alone.
func TestOpenCode1EffortIsTheAgentsVariant(t *testing.T) {
	for _, version := range []string{"1.19.6", ""} {
		t.Run("version="+version, func(t *testing.T) {
			home, _ := codexHome(t, "", "")
			thinkCatalog(t)
			openCodeAt(t, version)
			oc := opencode(home, filepath.Join(home, ".config"))
			if err := os.MkdirAll(oc.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oc.Path, []byte(`{"agent": {"build": {"prompt": "be brief"}, "plan": {"model": "anthropic/claude-x", "variant": "low"}}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			if err := oc.Apply("effort", "high"); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"model":               "magpie/think/deep",
				"agent.build.model":   "magpie/think/deep",
				"agent.build.variant": "high",
				"agent.build.prompt":  "be brief",
				"agent.plan.model":    "anthropic/claude-x",
				"agent.plan.variant":  "low",
			}
			for k, w := range want {
				if got, _ := edit.GetJSON(oc.Path, k); got != w {
					t.Fatalf("%s = %q, want %q\n%s", k, got, w, readFile(oc.Path))
				}
			}
			if v := oc.Values(); v["model"] != "magpie/think/deep" || v["effort"] != "high" {
				t.Fatalf("values = %v", v)
			}
			// a model without the level: the agent's model and variant
			// magpie wrote go, the user's prompt stays
			if err := oc.Apply("model", "magpie/think/new"); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"agent.build.model", "agent.build.variant"} {
				if got, ok := edit.GetJSON(oc.Path, k); ok {
					t.Fatalf("%s = %q left for a model without the level\n%s", k, got, readFile(oc.Path))
				}
			}
			if got, _ := edit.GetJSON(oc.Path, "agent.build.prompt"); got != "be brief" {
				t.Fatalf("the user's build prompt went:\n%s", readFile(oc.Path))
			}
			// one with it moves the agents along, and Disconnect's empty
			// model takes what magpie wrote out
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			if err := oc.Apply("effort", "max"); err != nil {
				t.Fatal(err)
			}
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			if got, _ := edit.GetJSON(oc.Path, "agent.build.variant"); got != "max" {
				t.Fatalf("model picked again: agent.build.variant = %q, want max", got)
			}
			if err := oc.Apply("model", ""); err != nil {
				t.Fatal(err)
			}
			if got, ok := edit.GetJSON(oc.Path, "agent.build.model"); ok {
				t.Fatalf("model cleared: agent.build.model %q left\n%s", got, readFile(oc.Path))
			}
			if got, _ := edit.GetJSON(oc.Path, "agent.plan.variant"); got != "low" {
				t.Fatalf("the user's plan agent changed:\n%s", readFile(oc.Path))
			}
			if m, _ := edit.GetJSON(oc.Path, "model"); m != "" {
				t.Fatalf("model = %q after clearing", m)
			}
		})
	}
}
