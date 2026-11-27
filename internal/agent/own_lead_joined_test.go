package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// An own lead leaves magpie's models available until Default or Disconnect.
func TestOwnLeadKeepsProviderJoined(t *testing.T) {
	for _, leave := range []string{"Disconnect", "Default"} {
		t.Run(leave, func(t *testing.T) {
			check := func(t *testing.T, a *Agent, model, own string, provider func() bool) {
				t.Helper()
				if err := a.Pick("model", model); err != nil {
					t.Fatal(err)
				}
				if err := a.Pick("model", own); err != nil {
					t.Fatal(err)
				}
				if !a.Wired() || a.Joined == nil || !a.Joined() {
					t.Fatal("picking the user's own lead switched the agent off")
				}
				if got := a.Values()["model"]; got != own || !provider() {
					t.Fatalf("lead %q, want %q, provider present %t", got, own, provider())
				}
				if d := a.Drift(); d != nil {
					t.Fatalf("drift on the user's own lead: %+v", d)
				}
				var err error
				if leave == "Default" {
					err = a.Pick("model", "")
				} else {
					err = a.Disconnect()
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := a.Values()["model"]; got != own || a.Wired() || provider() {
					t.Fatalf("%s: lead %q, want %q, wired %t, provider present %t", leave, got, own, a.Wired(), provider())
				}
			}

			t.Run("Copilot", func(t *testing.T) {
				a, path := copilotSandbox(t)
				writeFile(t, a.Path, `{"model":"gpt-5"}`)
				check(t, a, "magpie/deepseek/pro", "auto", func() bool {
					ps, _, _ := copilotParsed(t, path)
					return len(ps) != 0
				})
			})
			t.Run("Alma", func(t *testing.T) {
				syncHome(t)
				f := startAlma(t)
				const image = "magpie:relay/glm-4.6"
				f.mu.Lock()
				f.settings["imageGen"] = map[string]any{"model": image}
				f.mu.Unlock()
				check(t, alma(), "magpie/relay/glm-4.6", "own:gpt-5", func() bool {
					f.mu.Lock()
					defer f.mu.Unlock()
					present := f.provider("magpie") != nil
					want := ""
					if present {
						want = image
					}
					if got := almaImage(f.settings); got != want {
						t.Fatalf("image model %q, want %q", got, want)
					}
					return present
				})
			})
			t.Run("OpenHanako", func(t *testing.T) {
				home, dir := hanakoTestHome(t)
				cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
				writeFile(t, cfg, "models:\n  chat: {provider: openai, id: gpt-5}\n  utility: {provider: mine, id: m1}\n")
				check(t, hanako(home), "magpie/relay/glm-4.6", "mine/m2", func() bool {
					_, ok := hanakoCurrent(dir)
					return ok
				})
				if got, _ := edit.GetYAML(cfg, "models.utility.id"); got != "m1" {
					t.Fatalf("utility model changed: %q", got)
				}
			})
			t.Run("MothX", func(t *testing.T) {
				home := syncHome(t)
				a := mothx(home)
				writeFile(t, a.Path, mothxDefault(home))
				check(t, a, "magpie/relay/glm-4.6", "deepseek-openai/deepseek-v4-pro", func() bool {
					_, ok := edit.GetJSON(a.Path, "providers.magpie")
					return ok
				})
			})
		})
	}
}

func TestOwnLeadLeavesHandWrittenMagpie(t *testing.T) {
	check := func(t *testing.T, a *Agent, own string) {
		t.Helper()
		if a.Wired() {
			t.Fatal("the user's provider joined the agent")
		}
		if err := a.Pick("model", own); err != nil {
			t.Fatal(err)
		}
		if a.Wired() || a.Values()["model"] != own {
			t.Fatalf("own pick: wired %t, model %q", a.Wired(), a.Values()["model"])
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("Copilot", func(t *testing.T) {
		a, path := copilotSandbox(t)
		const hand = `{"providers":[{"name":"magpie","type":"openai","baseUrl":"http://127.0.0.1:9/v1","apiKey":"mine"}]}`
		writeFile(t, path, hand)
		check(t, a, "auto")
		if got := readFile(path); got != hand {
			t.Fatalf("provider changed: %s", got)
		}
	})
	t.Run("Alma", func(t *testing.T) {
		syncHome(t)
		f := startAlma(t)
		f.mu.Lock()
		f.providers = append(f.providers, almaPreset(true, "my-model"))
		f.mu.Unlock()
		check(t, alma(), "own:gpt-5")
		for _, w := range f.takeWrites() {
			if w != "PUT /api/settings" {
				t.Fatalf("own pick wrote to the user's provider: %s", w)
			}
		}
	})
	t.Run("OpenHanako", func(t *testing.T) {
		home, dir := hanakoTestHome(t)
		a := hanako(home)
		const hand = `{"catalogVersion":2,"providers":{"magpie":{"base_url":"http://127.0.0.1:9/v1","api_key":"mine"}}}`
		writeFile(t, a.Path, hand)
		writeFile(t, filepath.Join(dir, "agents", "hana", "config.yaml"), "models:\n  chat: {provider: openai, id: gpt-5}\n")
		check(t, a, "openai/gpt-5-mini")
		if got := readFile(a.Path); got != hand {
			t.Fatalf("provider changed: %s", got)
		}
	})
	t.Run("MothX", func(t *testing.T) {
		home := syncHome(t)
		a := mothx(home)
		writeFile(t, a.Path, `{"providers":{"magpie":{"baseUrl":"http://127.0.0.1:9/v1","apiKey":"mine"}}}`)
		was, _ := edit.GetJSON(a.Path, "providers.magpie")
		check(t, a, "deepseek-openai/deepseek-v4-pro")
		if got, _ := edit.GetJSON(a.Path, "providers.magpie"); got != was {
			t.Fatalf("provider changed: %s", got)
		}
	})
}
