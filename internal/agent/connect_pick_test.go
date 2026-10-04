package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// The 「接入」 switch keeps an agent on the model it was on, now through
// magpie: Claude Code on its opus alias goes to the Claude Opus magpie
// serves, not to the first model of the first provider.
func TestConnectKeepsTheModel(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"first", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)
	c := claude(home)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "aaa/claude-opus-5-5" {
		t.Fatalf("connected on %q:\n%s", m, readFile(path))
	}
}

// Gemini CLI is connected through its model (its first field is how it
// signs in), and Disconnect leaves its settings.json as they were, with
// no "model": {} behind.
func TestGeminiConnectRoundTrip(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	const was = `{"security": {"auth": {"selectedType": "oauth-personal"}}}`
	writeFile(t, filepath.Join(dir, "settings.json"), was)
	g := gemini(home)
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	if !g.Wired() {
		t.Fatalf("not connected:\n%s", readFile(filepath.Join(dir, "settings.json")))
	}
	if err := g.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(filepath.Join(dir, "settings.json"), "model"); ok {
		t.Fatalf("model left: %q", v)
	}
	if a, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), "security.auth.selectedType"); a != "oauth-personal" {
		t.Fatalf("sign-in %q", a)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		if k, _ := edit.GetEnvFile(filepath.Join(dir, ".env"), "GEMINI_API_KEY"); k != "" {
			t.Fatalf("magpie's key left: %q", k)
		}
	}
	// and on again, on the model it was on (its sign-in follows the model)
	if err := g.Connect(); err != nil || !g.Wired() {
		t.Fatalf("connected again: %v\n%s", err, readFile(filepath.Join(dir, "settings.json")))
	}
}

// Of the providers serving the model the agent is on, Connect takes the
// account it is signed in to, then a subscription, over a relay listed
// first; on no model of its own, a subscription's before the first.
func TestConnectPrefersTheSubscription(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, c := range []struct {
		cur  string
		opts []Option
		want string
	}{
		{"claude-opus-5-5", []Option{
			{Value: "group/auto", Ref: "group/auto", Group: RoutingGroups},
			{Value: "relay/claude-opus-5-5[1m]", Ref: "relay/claude-opus-5-5[1m]", Group: "Relay"},
			{Value: "other/claude-opus-5-5", Ref: "other/claude-opus-5-5", Group: "Other", sub: true},
			{Value: "claude/claude-opus-5-5", Ref: "claude/claude-opus-5-5", Group: "Claude", sub: true, own: true},
		}, "claude/claude-opus-5-5"},
		{"opus", []Option{
			{Value: "relay/claude-opus-5-5[1m]", Ref: "relay/claude-opus-5-5", Group: "Relay"},
			{Value: "claude/claude-opus-5-5[1m]", Ref: "claude/claude-opus-5-5", Group: "X"},
		}, "claude/claude-opus-5-5[1m]"},
		{"claude-opus-5-5", []Option{
			{Value: "relay/claude-opus-5-5", Ref: "relay/claude-opus-5-5", Group: "Relay"},
			{Value: "other/claude-opus-5-5", Ref: "other/claude-opus-5-5", Group: "Other", sub: true},
		}, "other/claude-opus-5-5"},
		{"", []Option{
			{Value: "stepfun/step-1", Ref: "stepfun/step-1", Group: "StepFun"},
			{Value: "claude/claude-sonnet-5-5", Ref: "claude/claude-sonnet-5-5", Group: "Claude", sub: true},
		}, "claude/claude-sonnet-5-5"},
		{"", []Option{
			{Value: "stepfun/step-1", Ref: "stepfun/step-1", Group: "StepFun"},
		}, "stepfun/step-1"},
	} {
		v := c.cur
		a := &Agent{ID: "x", Name: "X", Fields: []Field{{Key: "model",
			Get:     func() string { return v },
			Set:     func(s string) error { v = s; return nil },
			Options: func(map[string]string) []Option { return c.opts },
		}}}
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		if v != c.want {
			t.Errorf("on %q: connected on %q, want %q", c.cur, v, c.want)
		}
	}
}

// Codex signed in with ChatGPT keeps its own last pick when connected (the
// owner: 让 Codex 记住上次的选择): magpie's models join its list by the base
// URL and its model stays; a model picked beside them keeps it connected,
// and Disconnect leaves Codex on that pick, magpie taken out.
func TestCodexConnectKeepsItsPick(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\nmodel_reasoning_effort = \"xhigh\"\n")
	cx := codex(home)
	if err := cx.Connect(); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !cx.Wired() || !strings.Contains(cfg, `model = "gpt-5.5"`) || !strings.Contains(cfg, `openai_base_url = "`+codexGatewayURL()+`"`) ||
		!strings.Contains(cfg, "[model_providers.magpie]") || strings.Contains(cfg, "model_provider =") {
		t.Fatalf("connected (wired %v):\n%s", cx.Wired(), cfg)
	}
	if d := cx.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	// Sync, the failover's turn included, leaves it connected
	if err := cx.Sync(); err != nil || !cx.Wired() {
		t.Fatalf("after Sync (%v):\n%s", err, read())
	}
	// picked in Codex's /model, as Codex writes it
	if err := edit.SetTOMLTop(filepath.Join(home, ".codex", "config.toml"), edit.KV{Path: "model", Value: "gpt-5.4"}); err != nil {
		t.Fatal(err)
	}
	if !cx.Wired() {
		t.Fatalf("a pick of its own disconnected it:\n%s", read())
	}
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if cfg = read(); cx.Wired() || !strings.Contains(cfg, `model = "gpt-5.4"`) || strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("disconnected:\n%s", cfg)
	}
	// connected again, on that pick still
	if err := cx.Connect(); err != nil || !cx.Wired() || !strings.Contains(read(), `model = "gpt-5.4"`) {
		t.Fatalf("again (%v):\n%s", err, read())
	}
}

// The 「接入」 switch off and on again leaves the agent on the models it was
// on (the owner: an agent remembers its last pick): Claude Code on a second
// magpie model and its effort, not the one Connect would pick. One the user
// changed while it was off is theirs, and Connect picks as it does at first.
func TestReconnectKeepsTheLastPick(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"first", "claude-opus-5-5", "second"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)
	c := claude(home)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("model", "aaa/second"); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("effort", "low"); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "opus" {
		t.Fatalf("disconnected on %q:\n%s", m, readFile(path))
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if v := c.Values(); v["model"] != "aaa/second" || v["effort"] != "low" {
		t.Fatalf("connected again on %v:\n%s", v, readFile(path))
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"model": "sonnet"}`)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if v := c.Values(); v["model"] == "aaa/second" || v["effort"] == "low" {
		t.Fatalf("a model picked while off was replaced by the last pick: %v", v)
	}
}

// Goose's own provider and model come back when it is disconnected: its
// field's empty value takes both keys out, and it was on anthropic before.
func TestGooseRoundTripKeepsItsOwnModel(t *testing.T) {
	home := syncHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	if err := provider.Save(provider.Provider{ID: "oa", Name: "OA", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	a := goose(home, filepath.Join(home, ".config"))
	writeFile(t, a.Path, "GOOSE_PROVIDER: anthropic\nGOOSE_MODEL: claude-opus-5-5\nGOOSE_MODE: auto\n")
	for i := 0; i < 2; i++ { // and again, through its last pick
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		if !a.Wired() {
			t.Fatalf("not connected:\n%s", readFile(a.Path))
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
	}
	for k, want := range map[string]string{"GOOSE_PROVIDER": "anthropic", "GOOSE_MODEL": "claude-opus-5-5", "GOOSE_MODE": "auto"} {
		if v, _ := edit.GetYAMLTop(a.Path, k); v != want {
			t.Fatalf("%s = %q after the round trip:\n%s", k, v, readFile(a.Path))
		}
	}
	if _, err := os.Stat(gooseProviderPath(a.Path)); err == nil {
		t.Fatal("magpie's custom provider left")
	}
}

// magpie's max rides in Claude Code's env, over the effortLevel the user
// had: disconnecting takes the env's level away and leaves theirs, and
// switching on again puts magpie's back over it (found on the omarchy VM:
// a round trip lost the user's xhigh, under a connection made before
// Connect kept what the agent was on).
func TestClaudeDisconnectKeepsTheUsersEffort(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"first", "second"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"effortLevel": "xhigh"}`)
	c := claude(home)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("model", "aaa/second"); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("effort", "max"); err != nil {
		t.Fatal(err)
	}
	// connected by a magpie before this one, which kept no record of what
	// the agent was on
	stash(map[string]string{"claude.connect.was": ""})
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	if get("effortLevel") != "xhigh" || get("env."+claudeEffortEnv) != "" || get("model") != "" {
		t.Fatalf("disconnected:\n%s", readFile(path))
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if v := c.Values(); v["model"] != "aaa/second" || v["effort"] != "max" || get("effortLevel") != "xhigh" {
		t.Fatalf("connected again on %v:\n%s", v, readFile(path))
	}
}

// A model of magpie's picked on the Agents page for an agent not connected
// connects it first, as its switch does (the owner: the switch, and one
// click in magpie, both): Goose starts on the model picked, and
// disconnecting it brings back its own provider and model.
func TestPickConnectsFirst(t *testing.T) {
	home := syncHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	if err := provider.Save(provider.Provider{ID: "oa", Name: "OA", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5", "gpt-5.4"}}); err != nil {
		t.Fatal(err)
	}
	a := goose(home, filepath.Join(home, ".config"))
	writeFile(t, a.Path, "GOOSE_PROVIDER: anthropic\nGOOSE_MODEL: claude-opus-5-5\n")
	var key, pick string
	for _, f := range a.Fields {
		for _, o := range f.Options(a.Values()) {
			if o.Ref == "oa/gpt-5.4" {
				key, pick = f.Key, o.Value
			}
		}
	}
	if pick == "" {
		t.Fatal("no oa/gpt-5.4 to pick")
	}
	if err := a.Pick(key, pick); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() || a.Values()[key] != pick {
		t.Fatalf("picked %q: %v\n%s", pick, a.Values(), readFile(a.Path))
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"GOOSE_PROVIDER": "anthropic", "GOOSE_MODEL": "claude-opus-5-5"} {
		if v, _ := edit.GetYAMLTop(a.Path, k); v != want {
			t.Fatalf("%s = %q after disconnecting:\n%s", k, v, readFile(a.Path))
		}
	}
}

// The switch says how it chose the model it connected an agent on (#726:
// Codex kept its last pick, Claude Code went to a Sonnet through magpie,
// with nothing to say why): Claude Code on its opus alias goes to the
// Opus magpie serves, said "alike"; switched off and on, it is back on what
// it was on, "again"; one already connected is "kept"; on a model magpie
// doesn't serve, "first".
func TestConnectSaysHow(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"first", "claude-opus-5-5", "claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)
	c := claude(home)
	how, err := c.ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	if how != (Connection{How: "alike", Field: "model", Value: "aaa/claude-opus-5-5"}) {
		t.Fatalf("connected %+v:\n%s", how, readFile(path))
	}
	if how, _ := c.ConnectHow(); how.How != "kept" {
		t.Fatalf("again while connected: %+v", how)
	}
	if err := c.Apply("model", "aaa/claude-sonnet-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if how, err := c.ConnectHow(); err != nil || how.How != "again" {
		t.Fatalf("switched on again: %+v %v", how, err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "aaa/claude-sonnet-5-5" {
		t.Fatalf("back on %q", m)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"model": "claude-haiku-9"}`)
	if how, err := c.ConnectHow(); err != nil || how.How != "first" || how.Value == "" {
		t.Fatalf("on a model magpie lacks: %+v %v", how, err)
	}
}
