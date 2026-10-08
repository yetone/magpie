package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeDistroHome is a distro's / in a temp dir with a user whose
// ~/.claude holds settings (none when ""); magpie's own HOME is another
// temp dir, whose ~/.claude nothing may touch.
func claudeDistroHome(t *testing.T, settings string) (root, home string) {
	t.Helper()
	syncHome(t)
	root = t.TempDir()
	home = filepath.Join(root, "home", "me")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	if settings != "" {
		os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o644)
	}
	return root, home
}

// noOwnClaude fails if this machine's ~/.claude or ~/.claude.json was made.
func noOwnClaude(t *testing.T) {
	t.Helper()
	own, _ := os.UserHomeDir()
	for _, p := range []string{".claude", ".claude.json"} {
		if _, err := os.Stat(filepath.Join(own, p)); !os.IsNotExist(err) {
			t.Fatalf("this machine's ~/%s was touched: %v", p, err)
		}
	}
}

// The probe asks after Claude Code as after Codex and Pi, and reads what
// it says.
func TestWSLProbeFindsClaude(t *testing.T) {
	for _, want := range []string{`[ -d "$HOME/.claude" ] && echo dir:.claude`, `p=$(command -v claude 2>/dev/null || wslbin claude) && echo "bin:claude $p"`,
		`[ -d "$HOME/.pi" ] && echo dir:.pi`, `[ -d "$HOME/.codex" ] && echo dir:.codex`} {
		if !strings.Contains(wslProbeScript, want) {
			t.Errorf("probe lacks %q:\n%s", want, wslProbeScript)
		}
	}
	d := parseProbe("Debian", "home:/home/user\ndir:.claude\nbin:claude\n")
	if d == nil || !d.Has["dir:.claude"] || !d.Has["bin:claude"] || !wslFound(*d) {
		t.Fatalf("%+v", d)
	}
}

// A running distro with Claude Code — its ~/.claude, or claude on its PATH
// alone — is listed as Claude Code · WSL <distro>, with the model and
// effort it starts on; one with Codex, Pi and Claude Code has all three,
// each read as before and kept apart in wsl.json.
func TestWSLClaudeDiscovered(t *testing.T) {
	root, home := claudeDistroHome(t, `{"model": "claude-opus-5-5", "modelSettings": {"claude-opus-5-5": {"effortLevel": "high"}}}`)
	allRoot := t.TempDir()
	os.MkdirAll(filepath.Join(allRoot, "root", ".codex"), 0o755)
	os.MkdirAll(filepath.Join(allRoot, "root", ".pi", "agent"), 0o755)
	os.MkdirAll(filepath.Join(allRoot, "root", ".claude"), 0o755)
	os.WriteFile(filepath.Join(allRoot, "root", ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o644)
	os.WriteFile(filepath.Join(allRoot, "root", ".pi", "agent", "settings.json"), []byte(`{"defaultProvider": "xai", "defaultModel": "grok-4.6"}`), 0o644)
	os.WriteFile(filepath.Join(allRoot, "root", ".claude", "settings.json"), []byte(`{"model": "claude-sonnet-5"}`), 0o644)
	binRoot := t.TempDir()
	asked, opened := fakeWSL(t, "Debian\r\nArch\r\nAll\r\nNone\r\n", "Debian\r\nArch\r\nAll\r\nNone\r\n", map[string]string{
		"Debian": "home:/home/me\ndir:.claude\nroute:default via 172.20.0.1 dev eth0\n",
		"Arch":   "home:/home/a\nbin:claude\n",
		"All":    "home:/root\ndir:.codex\ndir:.pi\ndir:.claude\nbin:claude\n",
		"None":   "home:/home/n\n",
	}, map[string]string{"Debian": root, "Arch": binRoot, "All": allRoot})

	ds := wslDistros()
	if len(ds) != 3 || strings.Join(*opened, ",") != "Debian,Arch,All" || len(*asked) != 4 {
		t.Fatalf("distros %+v, asked %v, opened %v", ds, *asked, *opened)
	}
	var ids []string
	byID := map[string]*Agent{}
	for _, a := range wslAgentsOf(ds) {
		ids = append(ids, a.ID)
		byID[a.ID] = a
	}
	if got := strings.Join(ids, ","); got != "claude@wsl:Debian,claude@wsl:Arch,codex@wsl:All,pi@wsl:All,claude@wsl:All" {
		t.Fatalf("agents %s", got)
	}
	a := byID["claude@wsl:Debian"]
	if a.Name != "Claude Code · WSL Debian" || a.Icon != "claudecode-color" || a.WSL != "Debian" || a.Bin != "" || a.UA != nil ||
		a.Aliases != nil || a.LastUsed != nil || a.Path != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("%+v", a)
	}
	if v := a.Values(); v["model"] != "claude-opus-5-5" || v["effort"] != "high" {
		t.Fatalf("values %v", v)
	}
	if v := byID["codex@wsl:All"].Values(); v["model"] != "gpt-5.5" {
		t.Fatalf("codex %v", v)
	}
	if v := byID["pi@wsl:All"].Values(); v["model"] != "xai/grok-4.6" {
		t.Fatalf("pi %v", v)
	}
	if v := byID["claude@wsl:All"].Values(); v["model"] != "claude-sonnet-5" {
		t.Fatalf("claude %v", v)
	}
	wslSave()
	var kept map[string]*distro
	json.Unmarshal([]byte(readFile(wslStatePath())), &kept)
	if kept["Debian"].Values["claude.model"] != "claude-opus-5-5" || kept["Debian"].Values["claude.effort"] != "high" ||
		kept["All"].Values["model"] != "gpt-5.5" || kept["All"].Values["pi.model"] != "xai/grok-4.6" ||
		kept["All"].Values["claude.model"] != "claude-sonnet-5" || kept["None"] != nil {
		t.Fatalf("kept %s", readFile(wslStatePath()))
	}
	noOwnClaude(t)
}

// Picking one of magpie's models writes Claude Code's settings.json in
// the distro's home, the same as this machine's Claude Code gets but for
// the gateway as the distro reaches it: Windows' address under NAT,
// 127.0.0.1 when mirrored. Its check passes, and fails on a base URL the
// distro can't reach or a managed setting in the distro's /etc; going back
// to Anthropic's model restores what was there. This machine's ~/.claude is
// never touched.
func TestWSLClaudePick(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		const before = `{"model": "claude-opus-5-5", "env": {"ANTHROPIC_BASE_URL": "https://relay.example", "ANTHROPIC_AUTH_TOKEN": "sk-own"}}`
		root, home := claudeDistroHome(t, before)
		d := distro{Name: "Debian", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.claude": true},
			Gateway: "172.20.0.1", Mirrored: mirrored, Running: true}
		a := wslAgent(wslKindOf("claude"), d)
		if a.ID != "claude@wsl:Debian" || !a.Detected() {
			t.Fatalf("%+v", a)
		}
		if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
		if err := a.Field("effort").Set("max"); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, ".claude", "settings.json")
		gw := gateway.URL()
		if !mirrored {
			gw = "http://172.20.0.1:" + gateway.Port()
		}
		env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
		if env("ANTHROPIC_BASE_URL") != gw || env("ANTHROPIC_AUTH_TOKEN") != gateway.Token || env("ANTHROPIC_MODEL") != "" ||
			env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "relay/glm-4.6" || env(claudeEffortEnv) != "max" {
			t.Fatalf("mirrored %v:\n%s", mirrored, readFile(path))
		}
		if v := a.Values(); v["model"] != "relay/glm-4.6" || v["effort"] != "max" {
			t.Fatalf("values %v", v)
		}
		// what was there is stashed under the distro's agent
		if s := stashLoad(); s["claude@wsl:Debian.model"] != "claude-opus-5-5" || s["claude@wsl:Debian.base_url"] != "https://relay.example" ||
			s["claude.model"] != "" {
			t.Fatalf("stash %v", s)
		}
		// the same file as native Claude Code's, the gateway's address aside
		nat := t.TempDir()
		os.MkdirAll(filepath.Join(nat, ".claude"), 0o755)
		os.WriteFile(filepath.Join(nat, ".claude", "settings.json"), []byte(before), 0o644)
		n := claude(nat)
		n.Field("model").Set("relay/glm-4.6")
		n.Field("effort").Set("max")
		want := strings.ReplaceAll(readFile(filepath.Join(nat, ".claude", "settings.json")), `"`+gateway.URL()+`"`, `"`+gw+`"`)
		if got := readFile(path); got != want {
			t.Fatalf("mirrored %v:\n got %s\nwant %s", mirrored, got, want)
		}
		// a tier of its own
		if err := a.Field("haiku").Set("relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
		if len(a.Field("haiku").Options(a.Values())) == 0 {
			t.Fatal("no tier options")
		}
		if c := a.Check(); c != "" {
			t.Fatalf("mirrored %v: %s", mirrored, c)
		}
		if a.Drift() != nil {
			t.Fatalf("drift %+v", a.Drift())
		}
		if !mirrored && !strings.Contains(a.Notice(), "mirrored") {
			t.Error(a.Notice())
		}

		// the check is the distro's: a URL it can't reach under NAT, and a
		// managed setting in its /etc
		if !mirrored {
			edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()})
			if c := a.Check(); !strings.Contains(c, gw) {
				t.Fatalf("nat, 127.0.0.1: %q", c)
			}
			edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gw})
		}
		managed := filepath.Join(root, "etc", "claude-code", "managed-settings.json")
		os.MkdirAll(filepath.Dir(managed), 0o755)
		os.WriteFile(managed, []byte(`{"env": {"ANTHROPIC_BASE_URL": "https://corp.example"}}`), 0o644)
		if c := a.Check(); !strings.Contains(c, "/etc/claude-code/managed-settings.json") || !strings.Contains(c, "corp.example") {
			t.Fatalf("managed: %q", c)
		}
		os.Remove(managed)

		// back to Anthropic's own model: its endpoint and token come back
		if err := a.Field("model").Set("claude-opus-5-5"); err != nil {
			t.Fatal(err)
		}
		if env("ANTHROPIC_BASE_URL") != "https://relay.example" || env("ANTHROPIC_AUTH_TOKEN") != "sk-own" || env("ANTHROPIC_MODEL") != "" {
			t.Fatalf("back:\n%s", readFile(path))
		}
		noOwnClaude(t)
	}
}

// A stopped distro's Claude Code is never asked anything nor opened: it
// is listed as magpie last saw it, its options come from magpie alone
// (not the base URL its file names, nor whether it is routed), and a pick
// starts the distro, then writes its files.
func TestWSLClaudeStopped(t *testing.T) {
	const onDisk = `{"model": "on-disk", "env": {"ANTHROPIC_BASE_URL": "https://relay.example"}}`
	root, home := claudeDistroHome(t, onDisk)
	settings := filepath.Join(home, ".claude", "settings.json")
	before, _ := os.Stat(settings)
	b, _ := json.Marshal(map[string]*distro{
		"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: map[string]bool{"dir:.claude": true},
			Values: map[string]string{"claude.model": "relay/glm-4.6", "claude.effort": "low", "claude.haiku": ""}},
	})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	asked, opened := fakeWSL(t, "Stopped\r\n", "", nil, nil)

	ds := wslDistros()
	if len(ds) != 1 || ds[0].Running || len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("%+v asked %v opened %v", ds, *asked, *opened)
	}
	as := wslAgentsOf(ds)
	if len(as) != 1 {
		t.Fatalf("%d agents", len(as))
	}
	a := as[0]
	if a.ID != "claude@wsl:Stopped" || a.Icon != "claudecode-color" || a.Path != "" || a.Dir != "" || a.Check != nil || a.Sync != nil ||
		a.LastUsed != nil || a.Reached != nil {
		t.Fatalf("%+v", a)
	}
	if v := a.Values(); v["model"] != "relay/glm-4.6" || v["effort"] != "low" {
		t.Fatalf("values %v", v)
	}
	opts := a.Field("model").Options(a.Values())
	if len(opts) == 0 {
		t.Fatal("no model options")
	}
	for _, o := range opts {
		if strings.Contains(o.Group, "relay.example") {
			t.Fatalf("the stopped distro's file was read: %+v", o)
		}
	}
	// its tiers offer magpie's models while the model last seen is one
	if len(a.Field("haiku").Options(a.Values())) == 0 || len(a.Field("effort").Options(a.Values())) == 0 {
		t.Fatal("tier / effort options")
	}
	if !strings.Contains(a.Notice(), "isn't running") || a.Drift() != nil {
		t.Fatal("notice / drift")
	}
	if got := wslClaudeStandIn("claude-haiku-4-5"); got != "" {
		t.Fatalf("stand-in from a stopped distro: %q", got)
	}
	if len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("asked %v opened %v", *asked, *opened)
	}
	if after, _ := os.Stat(settings); !after.ModTime().Equal(before.ModTime()) || readFile(settings) != onDisk {
		t.Fatal("a stopped distro's files were touched")
	}

	if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*asked, ",") != "Stopped" {
		t.Fatalf("not started first: %v", *asked)
	}
	if u, _ := edit.GetJSON(settings, "env.ANTHROPIC_BASE_URL"); u != gateway.URL() {
		t.Fatalf("set:\n%s", readFile(settings))
	}
	if v := a.Field("model").Get(); v != "relay/glm-4.6" {
		t.Fatal(v)
	}
	noOwnClaude(t)
}

// A Claude Code in a running distro, routed through magpie, has its tiers
// stand in for a Claude model it names, as this machine's has; one whose
// settings.json isn't routed has none.
func TestWSLClaudeStandIn(t *testing.T) {
	root, home := claudeDistroHome(t, "")
	path := filepath.Join(home, ".claude", "settings.json")
	fakeWSL(t, "Debian\r\n", "Debian\r\n", map[string]string{
		"Debian": "home:/home/me\ndir:.claude\nroute:default via 172.20.0.1 dev eth0\n",
	}, map[string]string{"Debian": root})
	ds := wslDistros()
	if len(ds) != 1 {
		t.Fatalf("%+v", ds)
	}
	nat := "http://172.20.0.1:" + gateway.Port()
	os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_BASE_URL":"`+nat+`","ANTHROPIC_MODEL":"a/main","ANTHROPIC_DEFAULT_HAIKU_MODEL":"a/small"}}`), 0o644)
	if got := wslClaudeStandIn("claude-haiku-4-5-20251001"); got != "a/small" {
		t.Fatalf("haiku: %q", got)
	}
	if got := wslClaudeStandIn("claude-opus-5-5"); got != "a/main" {
		t.Fatalf("opus: %q", got)
	}
	os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_MODEL":"a/main"}}`), 0o644)
	if got := wslClaudeStandIn("claude-haiku-4-5"); got != "" {
		t.Fatalf("not routed: %q", got)
	}
	noOwnClaude(t)
}

// The models magpie serves on the Claude account this machine's Claude
// Code is signed in to fold into one row of its picker (#496), the same
// models a second time. A distro's Claude Code has a sign-in of its own,
// which magpie doesn't read, so its picker leaves them as they are: it
// folded them all the same while the distro ran, only a stopped one's
// options leaving them out.
func TestWSLClaudeNotFolded(t *testing.T) {
	root, _ := claudeDistroHome(t, `{"model": "claude-opus-5-5"}`)
	home, _ := os.UserHomeDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeFile(t, catalog.CachePath(), `{"anthropic":{"models":{"claude-opus-5-5":{"id":"claude-opus-5-5","name":"Claude Opus 5.5","limit":{"context":1000000}}}}}`)
	catalog.Reset()
	// this machine's Claude Code signed in, as its files say, with an inert
	// claude first on PATH: the machine's own is never asked
	bin := t.TempDir()
	testenv.Program(t, filepath.Join(bin, "claude"), "#!/bin/sh\nexit 1\n")
	writeFile(t, filepath.Join(bin, "claude.cmd"), "@exit /b 1\r\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"r","expiresAt":%d,"subscriptionType":"max"}}`, time.Now().Add(time.Hour).UnixMilli()))
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"me@example.com"}}`)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)

	folded := func(a *Agent) (n int) {
		for _, o := range a.Field("model").Options(a.Values()) {
			if o.Same {
				n++
			}
		}
		return n
	}
	if n := folded(claude(home)); n != 1 {
		t.Fatalf("this machine's Claude Code: %d folded, want claude/claude-opus-5-5[1m]", n)
	}
	for _, running := range []bool{true, false} {
		d := distro{Name: "Debian", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.claude": true}, Mirrored: true, Running: running}
		if n := folded(wslAgent(wslKindOf("claude"), d)); n != 0 {
			t.Errorf("Claude Code in WSL Debian, running %v: %d folded", running, n)
		}
	}
}
