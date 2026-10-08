package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
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
	os.WriteFile(creds, []byte("version: 1\nrefs:\n  MAGPIE_API_KEY: sk-mine\n  "+dshKeyRef+": magpie\n"), 0o600)
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
	dshKeyFixture(dir)
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
	dshKeyFixture(dir)
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

// dshRouteFixture is magpie's route listing those models, as dsh's Models
// page would write it.
func dshRouteFixture(ids ...string) string {
	s := "- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: " + dshKeyRef + "\n        api: openai-completions\n        baseURL: " + gatewayV1() + "\n        models:\n"
	for _, id := range ids {
		s += "          - id: " + id + "\n            name: " + id + " · DeepSeek\n"
	}
	return s
}

// dsh reads the patch list live, so a route left behind while magpie runs —
// by dsh's own Models page, an older magpie — fails every session there with
// no such configured model until it is written again. It is written again
// while magpie serves, and the model a session starts on stays the user's.
func TestDshRouteIsWrittenAgainWhileServing(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a := dsh(home)
	if d := a.Check(); d == "" {
		t.Fatal("a route without the model it starts on is not named")
	}
	dshWiredOnce()
	b, _ := os.ReadFile(web)
	s := string(b)
	if !strings.Contains(s, "deepseek/pro") || !strings.Contains(s, "deepseek/flash") {
		t.Fatalf("the route is not what magpie writes:\n%s", s)
	}
	if !strings.Contains(s, "provider: magpie\n    model: deepseek/flash") {
		t.Fatalf("the model it starts on is not the user's still:\n%s", s)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after writing the route again: %s", d)
	}
	// what is already what magpie writes is not written again
	dshWiredOnce()
	if again, _ := os.ReadFile(web); string(again) != s {
		t.Fatalf("the same route written again:\n%s", again)
	}
}

// The loop is what runs while magpie serves: a patch list changed under it
// is written again on the next round, not only at the next start.
func TestKeepDshWiredWritesTheRouteAgain(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro", "deepseek/flash")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a := dsh(home)
	if d := a.Check(); d != "" {
		t.Fatalf("the route magpie writes is not clean: %s", d)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { stop(); <-done })
	go func() { defer close(done); keepDshWired(ctx, 10*time.Millisecond) }()
	time.Sleep(100 * time.Millisecond) // a round or two with the route already right
	// Observe the changed route before allowing the loop to repair it.
	// Without this lock, a successful repair can race the stale-route check.
	func() {
		dshWrites.Lock()
		defer dshWrites.Unlock()
		if err := os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
			dshRouteFixture("deepseek/pro")+
			"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if d := a.Check(); d == "" {
			t.Fatal("the route changed under magpie is not named")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for a.Check() != "" {
		if time.Now().After(deadline) {
			t.Fatal("the route was not written again while magpie served")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if b, _ := os.ReadFile(web); !strings.Contains(string(b), "deepseek/flash") {
		t.Fatalf("the route written again:\n%s", b)
	}
}

// A patch list without magpie's route is the user's: writing the route again
// does not put it back.
func TestDshRouteAgainLeavesAListWithoutMagpieAlone(t *testing.T) {
	_, _, web := dshRouteHome(t)
	mine := "# Your patch layer for this dsh profile.\n- id: mine\n  config:\n    model: m\n"
	os.WriteFile(web, []byte(mine), 0o644)
	dshWiredOnce()
	if b, _ := os.ReadFile(web); string(b) != mine {
		t.Fatalf("a list without magpie's route:\n%s", b)
	}
}

// A route is written from the catalog: with no providers there is no list to
// write, and an empty one would be worse than the route left as it is.
func TestDshRouteAgainLeavesTheRouteWithNoCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if n := len(magpieModels("dsh")); n != 0 {
		t.Fatalf("a catalog to write from: %d models", n)
	}
	stale := "# Your patch layer for this dsh profile.\n" +
		dshRouteFixture("deepseek/pro") +
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"
	web := filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte(stale), 0o644)
	if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(web); string(b) != stale {
		t.Fatalf("a route written with no catalog:\n%s", b)
	}
	if err := dshSync(filepath.Join(home, ".dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(web); string(b) != stale {
		t.Fatalf("the sync wrote a route with no catalog:\n%s", b)
	}
	dshWiredOnce()
	if b, _ := os.ReadFile(web); string(b) != stale {
		t.Fatalf("a route written with no catalog:\n%s", b)
	}
}

// dsh's own writers keep a patch list in flow style (#445). A route that is
// what magpie writes is read out of it as the same list; whatever the round
// writes, the round after it writes nothing: a list that came back different
// every time would be written again every 30 seconds.
func TestDshRouteAgainSettlesOnAFlowList(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile\n# a top-level YAML array\n"+
		"[ { id: llm-pi-ai, name: \"@deepseek-ai/dsh-llm-pi-ai\", config: { providers: { magpie: { displayName: Magpie, apiKeyEnv: "+
		dshKeyRef+", api: openai-completions, baseURL: "+gatewayV1()+
		", models: [ { id: deepseek/pro, name: pro }, { id: deepseek/flash, name: flash } ] } } } }, { id: agent-default-model, config: { provider: magpie, model: deepseek/flash } } ]\n"), 0o644)
	a := dsh(home)
	if d := a.Check(); d != "" {
		t.Fatalf("a route magpie writes, kept in flow style: %s", d)
	}
	dshWiredOnce()
	first, _ := os.ReadFile(web)
	if !strings.Contains(string(first), "deepseek/pro") || !strings.Contains(string(first), "deepseek/flash") {
		t.Fatalf("the route read out of a flow list:\n%s", first)
	}
	dshWiredOnce()
	if second, _ := os.ReadFile(web); string(second) != string(first) {
		t.Fatalf("a flow list written again and again:\n%s", second)
	}
}

// A profile dsh made after magpie wired the others is filled with the route
// and the model they start on. With no catalog there is nothing to fill it
// with: a list of no models, and a default pointing at one, is what a session
// there fails on. A route written again never reaches this profile.
func TestDshFillNewProfilesLeavesABareProfileWithNoCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if n := len(magpieModels("dsh")); n != 0 {
		t.Fatalf("a catalog to write from: %d models", n)
	}
	dir := filepath.Join(home, ".dsh")
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	for _, f := range []string{web, desktop} {
		os.MkdirAll(filepath.Dir(f), 0o755)
	}
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/pro\n"), 0o644)
	bare := "# A new profile.\n[]\n"
	os.WriteFile(desktop, []byte(bare), 0o644)
	if err := dshSync(dir, gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(desktop); string(b) != bare {
		t.Fatalf("a new profile filled with no catalog:\n%s", b)
	}
}

// The other way round: with a catalog the new profile is filled as before.
func TestDshFillNewProfilesFillsABareProfile(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro", "deepseek/flash")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	os.WriteFile(desktop, []byte("# A new profile.\n[]\n"), 0o644)
	if err := dshSync(dir, gateway.URL()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(desktop)
	for _, want := range []string{"id: llm-pi-ai", "displayName: Magpie", "      magpie:", "provider: magpie", "id: deepseek/pro"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("a new profile not filled with %q:\n%s", want, b)
		}
	}
	if d := dsh(home).Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
}

func TestDshWriteErrorReadsTheSameEveryRound(t *testing.T) {
	one := dshWriteError(&os.PathError{Op: "open", Path: "/x/.cordis.patch.yml.4236419057.tmp", Err: os.ErrPermission})
	two := dshWriteError(&os.PathError{Op: "open", Path: "/x/.cordis.patch.yml.3958428140.tmp", Err: os.ErrPermission})
	if one != two {
		t.Fatalf("the same trouble reads two ways: %q and %q", one, two)
	}
	if strings.Contains(one, ".tmp") {
		t.Fatalf("what is said carries the temporary name: %q", one)
	}
	if !strings.Contains(one, "permission denied") {
		t.Fatalf("what is said does not say why: %q", one)
	}
}

func TestDshSaidOnceSaysATroubleOnce(t *testing.T) {
	var said dshSaidOnce
	const trouble = "writing dsh's route again in /x: open: permission denied"
	if !said.first(trouble) {
		t.Fatal("the first round said nothing")
	}
	if said.first(trouble) {
		t.Fatal("the same trouble was said again")
	}
	if said.first("") {
		t.Fatal("a quiet round said something")
	}
	if !said.first(trouble) {
		t.Fatal("trouble after a quiet round was not said")
	}
}

// Discord (01huadalang): 这些协议怎么改 responses，在 dsh 改了一会就会被
// magpie 接管. The route was switched to OpenAI Responses in dsh's Models
// page, and the next round wrote Chat Completions back. A protocol the
// gateway speaks stays, at the address the vendor's SDK wants for it; one it
// has no endpoint for goes back to Chat Completions.
func TestDshRouteKeepsTheAPIPickedInDsh(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	route := func(api, base string) string {
		return "# Your patch layer for this dsh profile.\n" +
			"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
			"      magpie:\n        displayName: Magpie\n        apiKeyEnv: " + dshKeyRef + "\n        api: " + api + "\n        baseURL: " + base + "\n        models:\n          - id: deepseek/pro\n" +
			"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/pro\n"
	}
	a := dsh(home)
	for _, c := range []struct{ api, base, want, wantBase string }{
		{"openai-responses", gatewayV1(), "openai-responses", gatewayV1()},
		// dsh's Models page keeps the base as it was; Anthropic's SDK adds
		// /v1/messages to it
		{"anthropic-messages", gatewayV1(), "anthropic-messages", gateway.URL()},
		{"google-generative-ai", gatewayV1(), "openai-completions", gatewayV1()},
		{`""`, gatewayV1(), "openai-completions", gatewayV1()},
	} {
		os.WriteFile(web, []byte(route(c.api, c.base)), 0o644)
		// a round of the loop, then a catalog sync
		dshWiredOnce()
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(web)
		s := string(b)
		if !strings.Contains(s, "        api: "+c.want+"\n        baseURL: "+c.wantBase+"\n") || !strings.Contains(s, "- id: deepseek/flash") {
			t.Fatalf("api %s: want %s at %s:\n%s", c.api, c.want, c.wantBase, s)
		}
		if d := a.Check(); d != "" {
			t.Fatalf("api %s: %s", c.api, d)
		}
		// and the round after leaves it as it is
		dshWiredOnce()
		if again, _ := os.ReadFile(web); string(again) != s {
			t.Fatalf("api %s written again:\n%s", c.api, again)
		}
	}
}

// dsh's own save of a patch list can strip magpie's route while the start
// still names one of magpie's models — its desktop app keeps a snapshot of
// the list and writes it back as it saves. The start is the user's pick, so
// this is not a route taken out: the check says nothing (the row would go
// red for a loss that repairs itself), and the round writes the route back
// beside the user's own, the start kept.
func TestDshStrippedRouteIsWrittenBackQuietly(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	stripped := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n" +
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"
	os.WriteFile(web, []byte(stripped), 0o644)
	read := func() string { b, _ := os.ReadFile(web); return string(b) }
	a := dsh(home)
	if got := a.Field("model").Get(); got != "magpie/deepseek/flash" {
		t.Fatalf("model: %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("a route dsh stripped is said: %q", d)
	}
	dshWiredOnce()
	s := read()
	for _, want := range []string{"      mine:\n", "      magpie:\n", "apiKeyEnv: " + dshKeyRef, "baseURL: " + gatewayV1(), "- id: deepseek/flash", "provider: magpie\n    model: deepseek/flash"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q after the write-back:\n%s", want, s)
		}
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after the write-back: %q", d)
	}
	// and the round after leaves it as it is
	dshWiredOnce()
	if again := read(); again != s {
		t.Fatalf("written again:\n%s", again)
	}
}

// A profile taken off magpie for real — the start no longer names one of
// magpie's models — is left as it is, as before: the round writes no route
// back into it.
func TestDshStrippedRouteWithoutAMagpieStartIsLeftAlone(t *testing.T) {
	_, _, web := dshRouteHome(t)
	mine := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n" +
		"- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-flash\n"
	os.WriteFile(web, []byte(mine), 0o644)
	dshWiredOnce()
	if b, _ := os.ReadFile(web); string(b) != mine {
		t.Fatalf("a profile taken off magpie was written into:\n%s", b)
	}
}

// With no catalog there is no route to write back, so a stripped profile is
// said as before rather than met with silence.
func TestDshCheckSaysForAStrippedRouteWithNoCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// no provider: no catalog to write the route from
	dir := filepath.Join(home, ".dsh")
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a := dsh(home)
	if d := a.Check(); d == "" {
		t.Fatal("a route that cannot be written back is not met with silence")
	}
}

// and a profile still on the old wiring — its llm-deepseek row is magpie's,
// not yet moved — is dshMove's: the serving round must not write an
// llm-pi-ai entry into it beside the row the move is about to take over.
func TestDshServingRoundLeavesTheOldWiringToDshMove(t *testing.T) {
	home, _, web := dshRouteHome(t)
	_ = home
	old := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-deepseek # magpie\n  config:\n    apiKeyEnv: MAGPIE_API_KEY\n    baseURL: \"" + gatewayV1() + "\"\n    thinking: enabled\n" +
		"- id: agent-default-model # magpie\n  config:\n    provider: deepseek-official\n    model: \"deepseek/pro\"\n"
	os.WriteFile(web, []byte(old), 0o644)

	dshWiredOnce()

	b, _ := os.ReadFile(web)
	if string(b) != old {
		t.Fatalf("a list on the old wiring was written into by the round:\n%s", b)
	}
}

// dshKeyFixture puts the key where magpie puts it for dsh: .env, which the
// product CLI loads, and dsh's own key store, which the desktop app reads.
func dshKeyFixture(dir string) {
	os.WriteFile(filepath.Join(dir, ".env"), []byte(dshKeyRef+"=magpie\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".credentials.yaml"), []byte("version: 1\nrefs:\n  "+dshKeyRef+": magpie\n"), 0o600)
}

// the user's own provider, as a profile's llm-pi-ai entry holds one
const dshMineRow = "- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
	"      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n"

// yetone's review of #1029: a home layer whose start names one of magpie's
// models and which has no llm-pi-ai of its own must gain none — the home
// layer's config replaces every profile's whole entry (dshOver), so an entry
// added there takes the user's own providers out of dsh's list (#804).
func TestDshHomeLayerWithoutPiRowGainsNone(t *testing.T) {
	_, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshMineRow), 0o644)
	hp := filepath.Join(dir, "cordis.patch.yml")
	before := "# shared by every profile\n" +
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"
	os.WriteFile(hp, []byte(before), 0o644)

	dshWiredOnce()

	after, _ := os.ReadFile(hp)
	if string(after) != before {
		t.Fatalf("the home layer gained an entry:\n%s", after)
	}
}

// and a home layer that does hold one keeps what magpie writes there: the
// route goes back beside the user's own, never a second entry.
func TestDshHomeLayerWithPiRowGetsItsRouteBack(t *testing.T) {
	_, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshMineRow), 0o644)
	hp := filepath.Join(dir, "cordis.patch.yml")
	os.WriteFile(hp, []byte("# shared by every profile\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      mine:\n        displayName: Mine\n        apiKeyEnv: MINE_API_KEY\n        api: openai-completions\n        baseURL: https://mine.example/v1\n        models:\n          - id: m1\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)

	dshWiredOnce()

	after, _ := os.ReadFile(hp)
	s := string(after)
	if strings.Count(s, "- id: llm-pi-ai") != 1 {
		t.Fatalf("the home layer got a second entry:\n%s", s)
	}
	if !strings.Contains(s, "\n      magpie:\n") {
		t.Fatalf("the home layer's route was not written back:\n%s", s)
	}
	if !strings.Contains(s, "\n      mine:\n") {
		t.Fatalf("the user's own provider was lost:\n%s", s)
	}
}

// yetone's review of #1029: with no catalog to write a route from, a profile
// whose start is not magpie's and which has no route is still said — the
// loop over the other profiles must not be skipped just because nothing can
// be written back (the web profile here is wired, with magpie's route, so
// the check reaches the loop).
func TestDshCheckSaysWhenNoCatalogAndAProfileHasNoRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// no provider: no catalog to write the route from
	dir := filepath.Join(home, ".dsh")
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	cli := filepath.Join(dir, "profiles", "cli", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.MkdirAll(filepath.Dir(cli), 0o755)
	dshKeyFixture(dir)
	// web: magpie's route in it, and the start on magpie's
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: "+dshKeyRef+"\n        api: openai-completions\n        baseURL: http://127.0.0.1:3425/v1\n        models:\n          - id: deepseek/flash\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	// cli: no route, and a start of the user's own
	os.WriteFile(cli, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-flash\n"), 0o644)

	d := dsh(home).Check()
	if !strings.Contains(d, "cli profile") || !strings.Contains(d, "none of magpie's models") {
		t.Fatalf("a profile without magpie's route and without magpie's start is not said: %q", d)
	}
}

// yetone's review of #1029, the branch left untested: with no catalog there is
// nothing a round could write back, so a second profile whose start names one
// of magpie's models is said too — `canWriteBack` must stay false here (set it
// true and this test fails: the profile would be passed over in silence).
func TestDshCheckSaysWhenNoCatalogAndAProfilesStartIsMagpies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// no provider: no catalog to write the route from
	dir := filepath.Join(home, ".dsh")
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	cli := filepath.Join(dir, "profiles", "cli", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.MkdirAll(filepath.Dir(cli), 0o755)
	dshKeyFixture(dir)
	// web: magpie's route in it, and the start on magpie's
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: "+dshKeyRef+"\n        api: openai-completions\n        baseURL: http://127.0.0.1:3425/v1\n        models:\n          - id: deepseek/flash\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	// cli: no route of its own, and its start names one of magpie's — the shape
	// dsh's own save leaves, which a round would write back were there a catalog
	os.WriteFile(cli, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)

	d := dsh(home).Check()
	if !strings.Contains(d, "cli profile") || !strings.Contains(d, "none of magpie's models") {
		t.Fatalf("with no catalog, a stripped profile of magpie's own start is not said: %q", d)
	}
}
