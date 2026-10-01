package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func dshRouteHome(t *testing.T) (home, dir, web string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(home, ".dsh")
	web = filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	return home, dir, web
}

// Discord (01huadalang): 集成进 dsh 会被 dsh 覆写，应该走 dsh 的另外的供应商.
// magpie took over dsh's own DeepSeek row: dsh's Models page showed it as
// DeepSeek, and a DeepSeek key entered there was saved under magpie's
// credential name, over magpie's. magpie is now a custom provider of dsh's,
// the route its Models page writes for one, and dsh's DeepSeek row and the
// user's own routes stay as they are.
func TestDshCustomProvider(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	own := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-deepseek\n  config:\n    apiKeyEnv: DEEPSEEK_API_KEY\n    thinking: enabled\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n"
	os.WriteFile(web, []byte(own), 0o644)
	read := func() string { b, _ := os.ReadFile(web); return string(b) }
	a := dsh(home)
	f := a.Field("model")

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	if !strings.HasPrefix(s, "# Your patch layer for this dsh profile.\n- id: llm-deepseek\n  config:\n    apiKeyEnv: DEEPSEEK_API_KEY\n    thinking: enabled\n") || strings.Count(s, "llm-deepseek") != 1 {
		t.Fatalf("dsh's own DeepSeek row was taken:\n%s", s)
	}
	for _, want := range []string{"      mine:\n", "baseURL: https://mine.example/v1", "      magpie:\n", "displayName: Magpie", "apiKeyEnv: " + dshKeyRef, "api: openai-completions", "baseURL: " + gatewayV1(), "- id: deepseek/pro", "provider: magpie", `model: "deepseek/pro"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Count(s, "id: llm-pi-ai") != 1 || strings.Contains(s, "deepseek-official") {
		t.Fatalf("patch:\n%s", s)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}

	// dsh writes the file anew as it saves an edit — a model picked in its
	// composer, a provider added in its Models page — in its own style
	dshSaved := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-deepseek\n  config:\n    apiKeyEnv: DEEPSEEK_API_KEY\n    thinking: enabled\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n" +
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: " + dshKeyRef + "\n        api: openai-completions\n        baseURL: " + gatewayV1() + "\n        reasoning: high\n        models:\n          - id: deepseek/pro\n            name: pro · DeepSeek\n          - id: deepseek/flash\n            name: flash · DeepSeek\n" +
		"      added:\n        displayName: Added\n        apiKeyEnv: ADDED_API_KEY\n        api: anthropic-messages\n        baseURL: https://added.example\n        models:\n          - id: a1\n" +
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n    reasoningEffort: high\n"
	os.WriteFile(web, []byte(dshSaved), 0o644)
	if f.Get() != "magpie/deepseek/flash" || a.Check() != "" {
		t.Fatalf("after dsh saved it: get %q, check %q", f.Get(), a.Check())
	}
	// a catalog sync keeps what dsh added to magpie's route and beside it
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	s = read()
	for _, want := range []string{"      mine:\n", "      added:\n", "reasoning: high", "- id: deepseek/flash", "model: deepseek/flash"} {
		if !strings.Contains(s, want) {
			t.Fatalf("sync lost %q:\n%s", want, s)
		}
	}

	// a DeepSeek key entered while magpie had taken dsh's DeepSeek row sits
	// in dsh's own store under the name that row gave it, which dsh reads
	// over .env; the route names another
	creds := filepath.Join(dir, ".credentials.yaml")
	os.WriteFile(creds, []byte("version: 1\nrefs:\n  MAGPIE_API_KEY: sk-mine\n"), 0o600)
	if dshKeyRef == "MAGPIE_API_KEY" || a.Check() != "" {
		t.Fatalf("the route reads the key a DeepSeek key was saved under: %q", a.Check())
	}
	// one entered for the route itself goes over magpie's; it is reported
	os.WriteFile(creds, []byte("version: 1\nrefs:\n  "+dshKeyRef+": sk-mine\n"), 0o600)
	if d := a.Check(); !strings.Contains(d, ".credentials.yaml") {
		t.Fatalf("shadowed key not reported: %q", d)
	}
	os.Remove(creds)

	// back to dsh's own models: magpie's route and model go, the rest stays
	if err := f.Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	s = read()
	if strings.Contains(s, "magpie:") || strings.Contains(s, "provider: magpie") || !strings.Contains(s, "      mine:\n") || !strings.Contains(s, "      added:\n") || !strings.Contains(s, "provider: deepseek-official") || f.Get() != "deepseek-v4-pro" {
		t.Fatalf("own model: %q\n%s", f.Get(), s)
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	s = read()
	if strings.Contains(s, "magpie") || strings.Contains(s, "agent-default-model") || !strings.Contains(s, "      mine:\n") || !strings.Contains(s, "      added:\n") || !strings.Contains(s, "apiKeyEnv: DEEPSEEK_API_KEY") {
		t.Fatalf("reset:\n%s", s)
	}
}

// A route magpie added to a patch list with none goes whole, leaving the
// file as it was.
func TestDshCustomProviderAlone(t *testing.T) {
	home, _, web := dshRouteHome(t)
	template := "# Your patch layer for this dsh profile.\n[]\n"
	os.WriteFile(web, []byte(template), 0o644)
	f := dsh(home).Field("model")
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(web); string(b) != template {
		t.Fatalf("reset:\n%s", b)
	}
}

// A profile magpie wired by taking over llm-deepseek moves onto the route on
// the next sync, with its model and effort, and the user's own DeepSeek
// entry it stood in for comes back.
func TestDshMovesOffDeepSeekRow(t *testing.T) {
	home, _, web := dshRouteHome(t)
	userRow := "- id: llm-deepseek\n  config:\n    baseURL: https://example.com\n"
	env := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(web))), ".env")
	os.WriteFile(env, []byte("OTHER=1\nMAGPIE_API_KEY=magpie\n"), 0o600)
	stash(map[string]string{dshStashKey(web, "llm-deepseek"): strings.TrimSuffix(userRow, "\n")})
	old := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-deepseek # magpie\n  config:\n    apiKeyEnv: MAGPIE_API_KEY\n    baseURL: \"" + gatewayV1() + "\"\n    thinking: enabled\n    reasoningEffort: max\n    models:\n      - id: \"deepseek/pro\"\n        name: \"pro · DeepSeek\"\n" +
		"- id: agent-default-model # magpie\n  config:\n    provider: deepseek-official\n    model: \"deepseek/pro\"\n"
	os.WriteFile(web, []byte(old), 0o644)
	a := dsh(home)
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("before the move: %q", got)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	s := string(b)
	if !strings.Contains(s, userRow) || strings.Contains(s, "llm-deepseek # magpie") || !strings.Contains(s, "      magpie:\n") || !strings.Contains(s, "provider: magpie") || !strings.Contains(s, `model: "deepseek/pro"`) {
		t.Fatalf("moved:\n%s", s)
	}
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("after the move: %q", got)
	}
	if b, _ := os.ReadFile(env); string(b) != "OTHER=1\n"+dshKeyRef+"=magpie\n" {
		t.Fatalf(".env:\n%s", b)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
}

// dsh counts a model its provider doesn't list as none at all and refuses the
// turn: a profile whose route lists one of magpie's models while
// agent-default-model names another — a catalog that moved on, or a file
// written elsewhere — fails every session on a model magpie has. The check
// says so, and the sync it asks for lists the model again.
func TestDshCheckSaysTheModelItStartsOnIsNotInTheRoute(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	env := filepath.Join(dir, ".env")
	os.WriteFile(env, []byte(dshKeyRef+"=magpie\n"), 0o600)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: "+dshKeyRef+"\n        api: openai-completions\n        baseURL: "+gatewayV1()+"\n        models:\n          - id: deepseek/pro\n            name: pro · DeepSeek\n"+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a := dsh(home)
	if got := a.Field("model").Get(); got != "magpie/deepseek/flash" {
		t.Fatalf("model: %q", got)
	}
	if d := a.Check(); !strings.Contains(d, "deepseek/flash") || !strings.Contains(d, web) || !strings.Contains(d, "Apply again") {
		t.Fatalf("the model it starts on, the route, and the click that helps are not named: %q", d)
	}
	// what magpie writes: the route lists it, and the check is quiet
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after a sync: %q", d)
	}
}

// dsh runs whichever profile its session names — its desktop app reads the
// desktop one — so a second profile whose route has not got the model it
// starts on fails there too, even while the first profile is fine.
func TestDshCheckSaysWhenAnotherProfilesRouteHasNotGotTheModel(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(dshKeyRef+"=magpie\n"), 0o600)
	route := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: " + dshKeyRef + "\n        api: openai-completions\n        baseURL: " + gatewayV1() + "\n        models:\n" +
		"          - id: deepseek/pro\n            name: pro · DeepSeek\n"
	os.WriteFile(web, []byte(route+
		"          - id: deepseek/flash\n            name: flash · DeepSeek\n"+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	os.WriteFile(desktop, []byte(route+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a := dsh(home)
	if d := a.Check(); !strings.Contains(d, desktop) {
		t.Fatalf("the profile whose route has not got the model is not named: %q", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after a sync: %q", d)
	}
}

// a model magpie no longer gives dsh — its catalog moved on, the provider was
// switched off — is one the route written again cannot list: the check says
// so and asks for another model, not for a click that cannot help.
func TestDshCheckSaysWhenTheModelItStartsOnIsOneMagpieNoLongerGives(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(dshKeyRef+"=magpie\n"), 0o600)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: "+dshKeyRef+"\n        api: openai-completions\n        baseURL: "+gatewayV1()+"\n        models:\n"+
		"          - id: deepseek/pro\n            name: pro · DeepSeek\n"+
		"          - id: deepseek/flash\n            name: flash · DeepSeek\n"+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/gone\n"), 0o644)
	a := dsh(home)
	if got := a.Field("model").Get(); got != "magpie/deepseek/gone" {
		t.Fatalf("model: %q", got)
	}
	if d := a.Check(); !strings.Contains(d, "deepseek/gone") || !strings.Contains(d, "no longer gives") {
		t.Fatalf("the model magpie no longer gives is not named: %q", d)
	}
	// the route already holds what magpie writes: a sync leaves the model it
	// starts on alone, so the check still stands
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d == "" {
		t.Fatal("a sync that cannot list the model it starts on says nothing")
	}
}
