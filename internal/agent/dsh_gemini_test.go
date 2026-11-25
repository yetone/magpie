package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// no DeepSeek Harness Desktop in a test, whatever the machine has
func init() { dshAppVersion = func() string { return "" } }

// dshGeminiHome is dshRouteHome with a provider serving a Gemini on
// Google's API alone, with levels Google takes and one (xhigh) it doesn't,
// and the dsh CLI and desktop app saying cli and app.
func dshGeminiHome(t *testing.T, cli, app string) (home, dir, web string) {
	t.Helper()
	home, dir, web = dshRouteHome(t)
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gemini", Key: "k", Gemini: "http://127.0.0.1:1/v1beta", Models: []string{"gemini-3-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("gem", "http://127.0.0.1:1/v1beta", []catalog.Model{{ID: "gemini-3-flash", Efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}}}); err != nil {
		t.Fatal(err)
	}
	dshSays(t, cli, app)
	dshKeyFixture(dir)
	return home, dir, web
}

// dshSays has the dsh CLI and desktop app here say these versions.
func dshSays(t *testing.T, cli, app string) {
	t.Helper()
	wasCLI, wasApp := dshVersion, dshAppVersion
	dshVersion = func() string { return cli }
	dshAppVersion = func() string { return app }
	t.Cleanup(func() { dshVersion, dshAppVersion = wasCLI, wasApp })
}

// Discord (01huadalang): dsh 支持 gemini 协议了，增加一下，目前 magpie 导入 dsh
// 只有三种，补上 gemini 的协议. dsh 0.2.2-rc.2's llm-pi-ai takes
// google-generative-ai as a route's api, so a Gemini served on Google's API
// goes on a fourth route of magpie's, at the gateway's /v1beta, where it
// was translated from Chat. Its levels are the ones pi-ai's Google adapter
// sends (xhigh it refuses before sending), and the start names its route.
func TestDshGeminiRoute(t *testing.T) {
	home, _, web := dshGeminiHome(t, "0.2.2-rc.5", "")
	a := dsh(home)
	f := a.Field("model")
	if err := f.Set("magpie/gem/gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	s := string(b)
	ps := dshPiProviders(t, web)
	r, ok := ps["magpie-gemini"]
	if !ok {
		t.Fatalf("no Gemini route:\n%s", s)
	}
	if r.API != "google-generative-ai" || r.BaseURL != gateway.URL()+"/v1beta" || r.DisplayName != "Magpie · Gemini" || r.APIKeyEnv != dshKeyRef {
		t.Fatalf("route: %+v", r)
	}
	if got := dshRouteModelIDs(r); len(got) != 1 || got[0] != "gem/gemini-3-flash" {
		t.Fatalf("Gemini route lists %v", got)
	}
	if contains(dshRouteModelIDs(ps["magpie"]), "gem/gemini-3-flash") || !contains(dshRouteModelIDs(ps["magpie"]), "deepseek/pro") {
		t.Fatalf("Chat route lists %v", dshRouteModelIDs(ps["magpie"]))
	}
	if l := r.Models[0].ReasoningEfforts; l == nil || *l != (dshPiLevels{Off: "none", Minimal: "minimal", Low: "low", Medium: "medium", High: "high"}) {
		t.Fatalf("levels on Gemini's API: %+v", l)
	}
	if r.Models[0].Compat != nil {
		t.Fatalf("a compat switch on Gemini's API: %+v", r.Models[0].Compat)
	}
	if !strings.Contains(s, "    provider: magpie-gemini\n") || f.Get() != "magpie/gem/gemini-3-flash" || a.Check() != "" {
		t.Fatalf("start: get %q, check %q\n%s", f.Get(), a.Check(), s)
	}
	// the effort offered is one the route sends
	e := a.Field("effort")
	var offered []string
	for _, o := range e.Options(map[string]string{"model": "magpie/gem/gemini-3-flash"}) {
		offered = append(offered, o.Value)
	}
	if !contains(offered, "high") || contains(offered, "xhigh") {
		t.Fatalf("efforts offered: %v", offered)
	}
	if err := e.Set("xhigh"); err == nil {
		t.Fatal("xhigh set on a route that can't send it")
	}
	if err := e.Set("high"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(web)
	if !strings.Contains(string(b), "    provider: magpie-gemini\n") || e.Get() != "high" || a.Check() != "" {
		t.Fatalf("effort moved the start off its route:\n%s", b)
	}
	// the round after leaves it as it is
	dshWiredOnce()
	if again, _ := os.ReadFile(web); string(again) != string(b) {
		t.Fatalf("written again:\n%s", again)
	}
	// a Chat api picked for the route in dsh's Models page stays, at /v1
	os.WriteFile(web, []byte(strings.Replace(string(b), "api: google-generative-ai\n        baseURL: "+gateway.URL()+"/v1beta", "api: openai-completions\n        baseURL: "+gateway.URL()+"/v1beta", 1)), 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if r := dshPiProviders(t, web)["magpie-gemini"]; r.API != "openai-completions" || r.BaseURL != gatewayV1() || a.Check() != "" {
		t.Fatalf("api picked in dsh: %+v %q", r, a.Check())
	}
	// Disconnect takes it out with the rest
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(web); strings.Contains(string(b), "magpie-gemini") {
		t.Fatalf("disconnect:\n%s", b)
	}
}

// A dsh before 0.2.2-rc.2 refuses a route on google-generative-ai while it
// reads its config (llm-pi-ai's schema knows its own apis alone), so where
// any dsh reading the home is older, or says no version, a Gemini stays on
// the Chat route. Once every one is new enough, a sync moves it and its
// start onto the Gemini route.
func TestDshGeminiWaitsForADshThatSpeaksIt(t *testing.T) {
	for _, c := range []struct{ name, cli, app string }{
		{"older CLI", "0.2.2-rc.1", ""},
		{"no version", "", ""},
		{"older app", "", "0.2.1-rc.5"},
		{"new CLI, older app", "0.2.2-rc.5", "0.2.1-rc.2"},
		{"app with no version", "0.2.2-rc.5", "unknown"},
		{"alpha before", "0.2.2-alpha.3", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, _, web := dshGeminiHome(t, c.cli, c.app)
			a := dsh(home)
			if err := a.Field("model").Set("magpie/gem/gemini-3-flash"); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(web)
			ps := dshPiProviders(t, web)
			if _, ok := ps["magpie-gemini"]; ok || strings.Contains(string(b), "google-generative-ai") {
				t.Fatalf("a Gemini route for this dsh:\n%s", b)
			}
			if !contains(dshRouteModelIDs(ps["magpie"]), "gem/gemini-3-flash") || !strings.Contains(string(b), "    provider: magpie\n") {
				t.Fatalf("the Gemini is not on Chat:\n%s", b)
			}
			// on Chat the gateway fits xhigh to the model, so it is offered
			if l := ps["magpie"].Models[len(ps["magpie"].Models)-1].ReasoningEfforts; l == nil || l.XHigh != "xhigh" {
				t.Fatalf("levels on Chat: %+v", l)
			}
			if a.Check() != "" {
				t.Fatal(a.Check())
			}

			// dsh updated: the next sync moves the model and its start
			dshSays(t, "0.2.2-rc.2", "")
			if c.app != "" {
				dshSays(t, "0.2.2-rc.2", "0.2.3-alpha.2")
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			b, _ = os.ReadFile(web)
			ps = dshPiProviders(t, web)
			if !contains(dshRouteModelIDs(ps["magpie-gemini"]), "gem/gemini-3-flash") || contains(dshRouteModelIDs(ps["magpie"]), "gem/gemini-3-flash") {
				t.Fatalf("not moved:\n%s", b)
			}
			if !strings.Contains(string(b), "    provider: magpie-gemini\n") || a.Check() != "" {
				t.Fatalf("start not moved: check %q\n%s", a.Check(), b)
			}
		})
	}
}

// A dsh home in a WSL distro is read by the distro's dsh, by the version
// its probe found, never this machine's; DS Bot's runs a dsh of its own
// whose version magpie doesn't read.
func TestDshSpeaksGeminiByTheDshReadingTheHome(t *testing.T) {
	dshSays(t, "0.2.2-rc.5", "")
	root := t.TempDir()
	newer, older := filepath.Join(root, "new", ".dsh"), filepath.Join(root, "old", ".dsh")
	dshDistros.Store(filepath.Clean(newer), "0.2.2-rc.2")
	dshDistros.Store(filepath.Clean(older), "0.2.1-rc.5")
	bot := filepath.Join(root, "dsbot")
	dshApps.Store(filepath.Clean(bot), dsbotID)
	t.Cleanup(func() {
		dshDistros.Delete(filepath.Clean(newer))
		dshDistros.Delete(filepath.Clean(older))
		dshApps.Delete(filepath.Clean(bot))
	})
	for path, want := range map[string]bool{
		newer: true,
		filepath.Join(newer, "profiles", "web", "cordis.patch.yml"): true,
		older: false,
		filepath.Join(older, "profiles", "web", "cordis.patch.yml"): false,
		filepath.Join(root, "here", ".dsh"):                         true, // this machine's 0.2.2-rc.5
		bot:                                                         false,
		"":                                                          false,
	} {
		if got := dshSpeaksGemini(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

// Only Gemini's levels are held to Google's: a level of another name, or a
// model left with off alone, is not offered.
func TestDshGeminiLevels(t *testing.T) {
	levels, l := dshLevelsOn([]string{"none", "low", "high", "max"}, dshGeminiAPI)
	if strings.Join(levels, ",") != "off,low,high" || l == nil || *l != (dshPiLevels{Off: "none", Low: "low", High: "high"}) {
		t.Fatalf("%v %+v", levels, l)
	}
	if levels, l := dshLevelsOn([]string{"none", "xhigh", "max"}, dshGeminiAPI); levels != nil || l != nil {
		t.Fatalf("off alone offered: %v %+v", levels, l)
	}
	if levels, _ := dshLevelsOn([]string{"none", "low", "high", "max"}, "openai-completions"); strings.Join(levels, ",") != "off,low,high,max" {
		t.Fatalf("Chat's levels: %v", levels)
	}
}

// A route behind a local proxy's prefix (DshThrough) is read back from the
// Gemini route's /v1beta too, so a home with only a Gemini on magpie keeps
// the prefix through a sync.
func TestDshReachThroughTheGeminiRoute(t *testing.T) {
	_, dir, web := dshRouteHome(t)
	const prefix = "http://127.0.0.1:8787/bili/"
	gw := gateway.URL()
	os.WriteFile(web, []byte("- id: llm-pi-ai # magpie\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie-gemini:\n        displayName: Magpie · Gemini\n        apiKeyEnv: "+dshKeyRef+"\n        api: google-generative-ai\n        baseURL: "+prefix+gw+"/v1beta\n        models:\n          - id: gem/gemini-3-flash\n"), 0o644)
	if got := dshReach(dir, gw); got != prefix+gw {
		t.Fatalf("dshReach %q", got)
	}
}

// DSH's Gemini route goes out through Google's SDK, whose User-Agent comes
// first and DSH's after it (captured from dsh 0.2.2-rc.5 against a fake
// Gemini endpoint), with the plain token: its calls are DSH's, so the
// gateway's DSH handling (dshCompactTo) and the usage pages count them as
// they do the other routes'. The SDK alone, as agy sends it, is still no
// agent, and neither is a name in a comment.
func TestDshGeminiRouteIsDsh(t *testing.T) {
	const ua = "google-genai-sdk/2.32.0 gl-node/v25.8.2, deepseek-harness/0.2.2-rc.5 (+https://github.com/deepseek-ai/deepseek-harness) dsh-profile/headless"
	if a := usage.AgentOf(ua); a != "dsh" {
		t.Errorf("DSH's Gemini route reads as %q", a)
	}
	for ua, want := range map[string]string{
		"google-genai-sdk/1.71.0 gl-go/go1.28-20260721-RC03 cl/951519500":        "google-genai-sdk",
		"google-genai-sdk/2.32.0 gl-node/v25.8.2 (compatible; deepseek-harness)": "google-genai-sdk",
		"OpenAI/Python 1.54.0": "OpenAI",
		"deepseek-harness/0.2.2-rc.5 (+https://github.com/deepseek-ai/deepseek-harness)": "dsh",
		"MyApp/2.0 deepseek-harness/0.2.2":                                               "MyApp",
	} {
		if a := usage.AgentOf(ua); a != want {
			t.Errorf("%q reads as %q, want %q", ua, a, want)
		}
	}
}
