package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// 33sczf on X: the Library's billion-context tab put Claude Code behind
// bili's lane, ANTHROPIC_BASE_URL
// http://127.0.0.1:18787/bili/http://127.0.0.1:3425, which forwards to the
// gateway. The Agents page called it "配置已变更", and Reconnect would
// have written the gateway over the user's billion-context. The lane runs
// only beside its agent, so nothing answers GET / there as magpie: every
// agent whose address names the gateway at the end of a local proxy's
// path still reaches magpie, with no probe, and a sync keeps the chain.
func TestBiliChainIsStillMagpie(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	craftFixture(t, home)
	t.Setenv("ASTRBOT_ROOT", "")
	writeFile(t, filepath.Join(home, ".astrbot", "data", "cmd_config.json"), astrbotConfig)
	qwenpawFixture(t, home)
	setPort(t, 3592)
	const gw = "http://127.0.0.1:3592"
	const lane = "http://127.0.0.1:18787/bili/"
	var agents []*Agent
	for _, a := range All() {
		if a.Check == nil || a.Launch != nil || a.WSL != "" || a.Native != nil || a.ID == "alma" || a.ID == "shelley" || a.ID == "cursorlocal" {
			continue
		}
		var key, want string
		for _, g := range a.Fields {
			for _, o := range g.Options(a.Values()) {
				if want == "" && (o.Ref == "fake/m1" || o.Value == magpieID) {
					key, want = g.Key, o.Value
				}
			}
		}
		if want == "" {
			t.Fatalf("%s: no field takes magpie's models", a.ID)
		}
		if err := a.Apply(key, want); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		agents = append(agents, a)
	}
	moved := filesWith(home, "127.0.0.1:3592")
	if len(moved) < 20 {
		t.Fatalf("only %d files have the gateway's address: %v", len(moved), moved)
	}
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.ReplaceAll(string(b), gw, lane+gw)), 0o644)
	}

	// the lane is down, or answers GET / as itself: never asked
	asked := fakeMagpieAt(t, map[string]bool{})
	for _, a := range agents {
		if a.ID == "codex" {
			// its openai_base_url is held to the gateway itself; bili's
			// Codex lane is an MCP server, never this address
			continue
		}
		if c := a.Check(); c != "" {
			t.Errorf("%s behind billion-context: %s", a.ID, c)
		}
		if d := a.Drift(); d != nil && d.Kind == "unwired" {
			t.Errorf("%s behind billion-context drifted: %+v", a.ID, d)
		}
	}
	for _, b := range asked() {
		if strings.Contains(b, "/bili/") {
			t.Errorf("asked %s whether it is magpie", b)
		}
	}

	// a sync, which a pick and Reconnect write through, keeps the chain
	if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		s, name := string(b), strings.TrimPrefix(p, home)
		if strings.Count(s, gw) != strings.Count(s, lane+gw) {
			t.Errorf("%s is back on the gateway past billion-context:\n%s", name, s)
		}
	}
}

func TestChainsToGateway(t *testing.T) {
	for v, want := range map[string]bool{
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425":          true,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425/":         true,
		"http://localhost:8787/bili/anthropic/http://127.0.0.1:3425": true,
		"http://127.0.0.1:8787/bili/http://localhost:3425":           true,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3426":          false,
		"http://127.0.0.1:18787/bili/https://api.anthropic.com":      false,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425/v1":       false,
		"https://relay.example/bili/http://127.0.0.1:3425":           false,
		"http://127.0.0.1:3425":                                      false,
		"http://127.0.0.1:18787/bili/http://user@127.0.0.1:3425":     false,
		"http://127.0.0.1:18787/bili/http://evil.example:3425":       false,
	} {
		if got := chainsToGateway(v, "http://127.0.0.1:3425"); got != want {
			t.Errorf("chainsToGateway(%s) = %v, want %v", v, got, want)
		}
	}
	if !chainsToGateway("http://127.0.0.1:8787/bili/http://127.0.0.1:3425/v1", "http://127.0.0.1:3425/v1") {
		t.Error("an OpenAI agent's /v1 through bili isn't magpie")
	}
}

// The reporter's own: Claude Code behind bili's lane, which isn't running.
// No drift, and a pick (what Reconnect and the model picker write) keeps
// ANTHROPIC_BASE_URL on the lane.
func TestClaudeBehindBiliLaneKept(t *testing.T) {
	home, path := claudeSettings(t, `{}`)
	saveDeepseek(t)
	cl := claude(home)
	if err := cl.Apply("model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	gw, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL")
	lane := "http://127.0.0.1:18787/bili/" + gw
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: lane}); err != nil {
		t.Fatal(err)
	}
	fakeMagpieAt(t, map[string]bool{})
	if c := cl.Check(); c != "" {
		t.Fatalf("Check: %s", c)
	}
	if d := cl.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	if err := cl.Apply("model", "deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != lane {
		t.Fatalf("a pick wrote ANTHROPIC_BASE_URL = %s over the lane %s", u, lane)
	}
}

// #7846 (XueYukii on X): billion-context switched on for a Claude Code not
// yet on magpie writes its lane in front of where Claude Code went
// (bili's claudeNativeBaseUrlForOrigin: Anthropic's API with no base URL,
// else the user's own endpoint). Connecting Claude Code to magpie then
// wrote the gateway over the lane, so Claude Code went past bili straight
// to magpie until bili was switched off and on. Whichever came first,
// Claude Code ends behind bili's lane with magpie's gateway as its
// upstream, once: a Reconnect, a pick and a sync keep it so, and
// Disconnect puts back the lane to where it went before.
func TestClaudeConnectBehindBiliLane(t *testing.T) {
	for _, up := range []string{"https://api.anthropic.com", "https://relay.example/api"} {
		t.Run(up, func(t *testing.T) {
			t.Setenv("MAGPIE_ADDR", "")
			const lane = "http://127.0.0.1:18787/bili/"
			// as bili 0.1.216's `bili plugin install claude` leaves it, then
			// magpie's biliKeepCompact
			home, path := claudeSettings(t, `{
  "env": {
    "ANTHROPIC_BASE_URL": "`+lane+up+`",
    "DISABLE_AUTO_COMPACT": "0"
  },
  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "node /x/dist/claude-native-bootstrap.js"}]}]}
}`)
			saveDeepseek(t)
			fakeMagpieAt(t, map[string]bool{})
			cl := claude(home)
			base := func() string { u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); return u }
			if err := cl.Connect(); err != nil {
				t.Fatal(err)
			}
			want := lane + gateway.URL()
			if u := base(); u != want {
				t.Fatalf("Connect wrote ANTHROPIC_BASE_URL = %s, want %s", u, want)
			}
			if c := cl.Check(); c != "" {
				t.Fatalf("Check: %s", c)
			}
			if d := cl.Drift(); d != nil {
				t.Fatalf("drift: %+v", d)
			}
			for what, do := range map[string]func() error{
				"a pick":    func() error { return cl.Apply("model", "deepseek/flash") },
				"Reconnect": cl.Reapply,
				"a sync":    func() error { SyncCatalog(); return nil },
				"Connect":   cl.Connect,
			} {
				if err := do(); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				if u := base(); u != want {
					t.Fatalf("after %s ANTHROPIC_BASE_URL = %s, want %s", what, u, want)
				}
			}
			if err := cl.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if u := base(); u != lane+up {
				t.Fatalf("Disconnect left ANTHROPIC_BASE_URL = %s, want bili's lane to %s", u, up)
			}
		})
	}
}

// The other order of #7846: Claude Code on magpie first, then bili's
// install puts its lane in front of the gateway. Reconnect and Connect
// leave it, with no second /bili/.
func TestClaudeBiliAfterConnectKept(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	home, path := claudeSettings(t, `{}`)
	saveDeepseek(t)
	fakeMagpieAt(t, map[string]bool{})
	cl := claude(home)
	if err := cl.Connect(); err != nil {
		t.Fatal(err)
	}
	gw, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL")
	lane := "http://127.0.0.1:18787/bili/" + gw
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: lane}); err != nil {
		t.Fatal(err)
	}
	for what, do := range map[string]func() error{"Reconnect": cl.Reapply, "Connect": cl.Connect} {
		if err := do(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != lane {
			t.Fatalf("after %s ANTHROPIC_BASE_URL = %s, want %s", what, u, lane)
		}
	}
}

// #7846: the gateway's port moved in Settings with agents behind bili's
// /bili/ prefix (Claude Code's lane, Hermes', Zed's and dsh's) brings
// each to the new port behind the same prefix: bili's upstream follows
// magpie, and nothing goes past bili to the gateway.
func TestBiliChainFollowsPortMove(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	setPort(t, 3593)
	const old, lane = "http://127.0.0.1:3593", "http://127.0.0.1:18787/bili/"
	var agents []*Agent
	for _, a := range All() {
		if !contains([]string{"claude", "hermes", "zed", "dsh"}, a.ID) || a.WSL != "" {
			continue
		}
		var key, want string
		for _, g := range a.Fields {
			for _, o := range g.Options(a.Values()) {
				if want == "" && (o.Ref == "fake/m1" || o.Value == magpieID) {
					key, want = g.Key, o.Value
				}
			}
		}
		if want == "" {
			t.Fatalf("%s: no field takes magpie's models", a.ID)
		}
		if err := a.Apply(key, want); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		agents = append(agents, a)
	}
	if len(agents) != 4 {
		t.Fatalf("only %d of the four agents", len(agents))
	}
	// the agents' own files; magpie's folder is its own record
	moved := slices.DeleteFunc(filesWith(home, "127.0.0.1:3593"), magpieFile)
	if len(moved) < 4 {
		t.Fatalf("only %d files have the gateway's address: %v", len(moved), moved)
	}
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.ReplaceAll(string(b), old, lane+old)), 0o644)
	}
	fakeMagpieAt(t, map[string]bool{})
	on := OnGateway()
	for _, a := range agents {
		if !containsAgent(on, a.ID) {
			t.Fatalf("%s behind bili isn't on the gateway: %+v", a.ID, a.Drift())
		}
	}
	setPort(t, 3594)
	if _, err := Rewire(on); err != nil {
		t.Fatal(err)
	}
	const gw = "http://127.0.0.1:3594"
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		s, name := string(b), strings.TrimPrefix(p, home)
		if strings.Contains(s, "127.0.0.1:3593") {
			t.Errorf("%s is still on the old port:\n%s", name, s)
		}
		if strings.Contains(s, gw) && strings.Count(s, gw) != strings.Count(s, lane+gw) {
			t.Errorf("%s went past billion-context to the gateway:\n%s", name, s)
		}
		if strings.Contains(s, "/bili/"+lane) {
			t.Errorf("%s has bili's prefix twice:\n%s", name, s)
		}
	}
	for _, a := range agents {
		if d := a.Drift(); d != nil {
			t.Errorf("%s is off after the move: %+v", a.ID, d)
		}
	}
}
