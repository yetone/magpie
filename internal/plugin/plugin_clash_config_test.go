package plugin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Two plugins that sign in to one provider id, as on the owner's MacBook
// Air: a third party's doubao-work (tianpeng-dev's, added first) sets
// the provider with models: {} in its config hook and lists models
// itself with its own sign-in; @magpie-community's, added after it,
// serves doubao-work and writes its models with `??=`. Its models are
// the provider's, not the first plugin's empty list, and Prefer hands
// the provider, with its config, to the other.
func TestServingPluginsConfigIsTheProviders(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	write := func(name, src string) string {
		f := filepath.Join(dir, name, "index.js")
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	theirs := write("theirs", `export default async () => ({
  async config(cfg) {
    cfg.provider ??= {}
    cfg.provider["dup"] ??= { name: "Theirs", npm: "@ai-sdk/openai-compatible", api: "https://theirs.invalid/v1", models: {} }
  },
  auth: { provider: "dup", methods: [{ type: "api", label: "Key" }] },
})
`)
	mine := write("mine", `export default async () => ({
  async config(cfg) {
    cfg.provider ??= {}
    cfg.provider["dup"] ??= { name: "Mine", npm: "@ai-sdk/openai-compatible", api: "https://mine.invalid/v1",
      models: { auto: { name: "Auto" }, pro: { name: "Pro" } } }
  },
  auth: { provider: "dup", methods: [{ type: "api", label: "Key" }] },
})
`)
	row := func() Provider {
		t.Helper()
		ps, err := Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.ID == "dup" {
				return p
			}
		}
		t.Fatalf("no dup in %+v", ps)
		return Provider{}
	}
	ids := func(p Provider) []string {
		var out []string
		for _, m := range p.Models {
			out = append(out, m.ID)
		}
		return out
	}
	if _, err := Add(ctx, theirs); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, mine); err != nil {
		t.Fatal(err)
	}
	if p := row(); p.Spec != mine || p.Name != "Mine" || p.API != "https://mine.invalid/v1" || !reflect.DeepEqual(ids(p), []string{"auto", "pro"}) {
		t.Fatalf("dup = %s %q %s %v, want mine's: Mine, its api and models auto, pro", p.Spec, p.Name, p.API, ids(p))
	}
	if err := Prefer(theirs, "dup"); err != nil {
		t.Fatal(err)
	}
	if p := row(); p.Spec != theirs || p.Name != "Theirs" || len(p.Models) != 0 {
		t.Fatalf("after Prefer(theirs), dup = %s %q %v, want theirs' with no models", p.Spec, p.Name, ids(p))
	}
}
