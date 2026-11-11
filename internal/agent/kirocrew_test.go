package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kiroCrewUser is config.json as Kiro Crew's dashboard saves it: the
// user's own settings beside the agent section, and the harness the user
// chose for crew members.
const kiroCrewUser = `{
  "agent": {
    "default_agent": "kirocrew",
    "member_acp_backend": "kas",
    "model": "auto"
  },
  "gateway": {"port": 5476},
  "ui": {"theme": "dark"}
}
`

// kiroCrewSandbox is a sandbox HOME with Kiro Crew's home in it, the
// harnesses connected to magpie as wired says and the adapters as found.
func kiroCrewSandbox(t *testing.T, wired []string, found []string) (home string, a *Agent) {
	t.Helper()
	home = syncHome(t)
	t.Setenv("KIROCREW_HOME", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	w, h, j := kiroCrewWired, kiroCrewHasAdapter, kiroCrewJoin
	t.Cleanup(func() { kiroCrewWired, kiroCrewHasAdapter, kiroCrewJoin = w, h, j })
	kiroCrewWired = func(id string) bool { return slices.Contains(wired, id) }
	// none of the harnesses is installed here, so none is connected along
	kiroCrewJoin = func(string) (bool, error) { return false, nil }
	kiroCrewHasAdapter = func(h kiroCrewHarness) bool { return slices.Contains(found, h.id) }
	a = kiroCrew(home)
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, a
}

func kiroCrewKey(t *testing.T, path, key string) (any, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	var cur any = m
	for _, k := range strings.Split(key, ".") {
		o, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = o[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Connecting Kiro Crew runs it on Codex, connected to magpie, and its crew
// members too; Disconnect puts back what the user had, byte for byte, and
// every other setting stays.
func TestKiroCrewConnect(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"codex", "claude"}, []string{"codex", "claude"})
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "kirocrew" && x.Name == "Kiro Crew" }) {
		t.Fatal("Kiro Crew is not among the agents")
	}
	if a.Detected() {
		t.Fatal("found with no settings, app or command")
	}
	os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
	if !a.Detected() {
		t.Fatal("not found with its config.json")
	}
	if a.Wired() {
		t.Fatal("wired before connecting")
	}
	c, err := a.ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	if c.How != "magpie" || !a.Wired() {
		t.Fatalf("connect: %+v, wired %v", c, a.Wired())
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewBackend); v != "codex" {
		t.Errorf("acp_backend %v", v)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewMember); v != "codex" {
		t.Errorf("member_acp_backend %v", v)
	}
	if v := a.Values(); v["provider"] != magpieID || v["harness"] != "codex" {
		t.Errorf("values %v", v)
	}
	if n := a.Notice(); n != noticeKiroCrew.String() {
		t.Errorf("notice %q", n)
	}
	for _, k := range []string{"agent.default_agent", "agent.model", "gateway.port", "ui.theme"} {
		if _, ok := kiroCrewKey(t, a.Path, k); !ok {
			t.Errorf("%s is gone", k)
		}
	}
	// connecting again changes nothing
	b1, _ := os.ReadFile(a.Path)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(a.Path); string(b1) != string(b2) {
		t.Errorf("set again rewrote it:\n%s", b2)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Fatal("wired after disconnecting")
	}
	var was, now map[string]any
	json.Unmarshal([]byte(kiroCrewUser), &was)
	b, _ := os.ReadFile(a.Path)
	json.Unmarshal(b, &now)
	if !sameJSON(string(b), was) {
		t.Errorf("after disconnect:\n%s\nwant\n%s", b, kiroCrewUser)
	}
	if s := stashLoad(); s[kiroCrewWasKey] != "" || s[kiroCrewWroteKey] != "" {
		t.Errorf("stash kept: %v", s)
	}
}

// With no harness connected to magpie, connecting says to connect one and
// writes nothing; the harness picked by itself is Codex, then Claude Code,
// one whose adapter is here before one whose isn't, and a harness with no
// adapter's install says how to install it.
func TestKiroCrewPicksAHarness(t *testing.T) {
	_, a := kiroCrewSandbox(t, nil, nil)
	os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
	if _, err := a.ConnectHow(); !errors.Is(err, errKiroCrewNoHarness) {
		t.Fatalf("no harness: %v", err)
	}
	if b, _ := os.ReadFile(a.Path); string(b) != kiroCrewUser {
		t.Errorf("written with no harness:\n%s", b)
	}
	if len(a.Field("harness").Options(nil)) != 0 {
		t.Errorf("harnesses offered with none connected: %v", a.Field("harness").Options(nil))
	}

	for _, c := range []struct {
		wired, found []string
		want         string
		member       any
		notice       string
	}{
		{wired: []string{"claude", "codex", "goose"}, found: []string{"claude", "codex"}, want: "codex", member: "codex"},
		{wired: []string{"claude", "codex"}, found: []string{"claude"}, want: "claude", member: "claude"},
		{wired: []string{"opencode", "pi"}, found: []string{"opencode", "pi"}, want: "opencode", member: "opencode"},
		// Pi can't run crew members: theirs stays the user's
		{wired: []string{"pi"}, found: []string{"pi"}, want: "pi", member: "kas"},
		{wired: []string{"codex"}, want: "codex", member: "codex",
			notice: "Kiro Crew runs Codex through codex-acp, which magpie didn't find here: install it with npm i -g @agentclientprotocol/codex-acp, then start a new session."},
		// deepseek is no harness of magpie's
		{wired: []string{"dsh", "deepseek"}},
	} {
		_, a := kiroCrewSandbox(t, c.wired, c.found)
		os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
		_, err := a.ConnectHow()
		if c.want == "" {
			if !errors.Is(err, errKiroCrewNoHarness) {
				t.Errorf("%v: %v", c.wired, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v: %v", c.wired, err)
		}
		if v, _ := kiroCrewKey(t, a.Path, kiroCrewBackend); v != c.want {
			t.Errorf("%v %v: picked %v, want %s", c.wired, c.found, v, c.want)
		}
		if v, _ := kiroCrewKey(t, a.Path, kiroCrewMember); v != c.member {
			t.Errorf("%v: member %v, want %v", c.wired, v, c.member)
		}
		var opts []string
		for _, o := range a.Field("harness").Options(nil) {
			opts = append(opts, o.Value)
		}
		var want []string
		for _, h := range kiroCrewHarnesses {
			if slices.Contains(c.wired, h.id) {
				want = append(want, h.id)
			}
		}
		if !slices.Equal(opts, want) {
			t.Errorf("%v: offered %v", c.wired, opts)
		}
		n := a.Notice()
		if c.notice != "" && !strings.Contains(n, c.notice) || c.notice == "" && n != noticeKiroCrew.String() {
			t.Errorf("%v: notice %q", c.wired, n)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(a.Path); !sameJSON(string(b), json.RawMessage(kiroCrewUser)) {
			t.Errorf("%v: after disconnect:\n%s", c.wired, b)
		}
	}
}

// The harness picked on the row is written, its crew members' with it;
// one not connected to magpie is refused. Disconnect after a pick puts
// back the user's, and the pick comes back on connecting again.
func TestKiroCrewHarnessPick(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"codex", "claude", "pi"}, []string{"codex", "claude", "pi"})
	os.WriteFile(a.Path, []byte(`{"agent":{"acp_backend":"kas","member_acp_backend":"kas"}}`), 0o600)
	if err := a.Pick("harness", "goose"); !errors.Is(err, errKiroCrewHarness) {
		t.Errorf("goose, not connected: %v", err)
	}
	if err := a.Pick("harness", "deepseek"); !errors.Is(err, errKiroCrewHarness) {
		t.Errorf("deepseek: %v", err)
	}
	if err := a.Pick("harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if v := a.Values(); v["provider"] != magpieID || v["harness"] != "claude" {
		t.Fatalf("values %v", v)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewMember); v != "claude" {
		t.Errorf("member %v", v)
	}
	// Pi runs no crew members: theirs stays magpie's Claude Code
	if err := a.Pick("harness", "pi"); err != nil {
		t.Fatal(err)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewBackend); v != "pi" {
		t.Errorf("backend %v", v)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewMember); v != "claude" {
		t.Errorf("member %v", v)
	}
	if err := a.Pick("harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path)
	if !sameJSON(string(b), json.RawMessage(`{"agent":{"acp_backend":"kas","member_acp_backend":"kas"}}`)) {
		t.Errorf("after disconnect: %s", b)
	}
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if v := a.Values()["harness"]; v != "claude" {
		t.Errorf("reconnected on %q, not the harness picked before", v)
	}
}

// A harness set in config.local.json wins over config.json, so magpie
// writes none: connecting is refused and says so, nothing is written, and
// the row's note and notice say why.
func TestKiroCrewLocalPin(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"codex"}, []string{"codex"})
	os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
	local := filepath.Join(a.Dir, "config.local.json")
	os.WriteFile(local, []byte(`{"agent":{"acp_backend":""}}`), 0o600)
	if _, err := a.ConnectHow(); !errors.Is(err, errKiroCrewPinned) {
		t.Fatalf("pinned: %v", err)
	}
	if b, _ := os.ReadFile(a.Path); string(b) != kiroCrewUser {
		t.Errorf("config.json written:\n%s", b)
	}
	if b, _ := os.ReadFile(local); string(b) != `{"agent":{"acp_backend":""}}` {
		t.Errorf("config.local.json written: %s", b)
	}
	if a.Wired() {
		t.Error("wired")
	}
	if n := a.Notice(); n != noticeKiroCrewPinned.String() {
		t.Errorf("notice %q", n)
	}
	if o := a.Field("provider").Options(nil); o[0].Note != kiroCrewPinnedNote {
		t.Errorf("note %q", o[0].Note)
	}
	// set there after magpie connected it: what Kiro Crew runs is the
	// user's, and magpie isn't said to be its provider
	os.Remove(local)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(local, []byte(`{"agent":{"acp_backend":"kas"}}`), 0o600)
	if a.Wired() {
		t.Error("wired with the local pin on kas")
	}
	// on a harness of magpie's: connected
	os.WriteFile(local, []byte(`{"agent":{"acp_backend":"codex"}}`), 0o600)
	if !a.Wired() {
		t.Error("not wired with the local pin on Codex")
	}
}

// A harness no longer connected to magpie leaves Kiro Crew not connected,
// and a harness the user set in Kiro Crew since is kept by Disconnect.
func TestKiroCrewFollowsItsHarness(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"codex"}, []string{"codex"})
	os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	kiroCrewWired = func(string) bool { return false }
	if a.Wired() {
		t.Error("wired with Codex off magpie")
	}
	kiroCrewWired = func(id string) bool { return id == "codex" }
	// the user moves it to kiro-cli and its members to Claude in Kiro Crew
	if err := setJSONFile(a.Path, map[string]any{kiroCrewBackend: "", kiroCrewMember: "claude"}); err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Error("wired on kiro-cli")
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewBackend); v != "" {
		t.Errorf("backend %v, the user's \"\" written over", v)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewMember); v != "claude" {
		t.Errorf("member %v, the user's claude written over", v)
	}
}

// config.json not there yet (the app installed, never started): magpie
// makes it with the one key, and takes it out again on Disconnect.
func TestKiroCrewMakesItsFile(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"opencode"}, []string{"opencode"})
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if v, _ := kiroCrewKey(t, a.Path, kiroCrewBackend); v != "opencode" {
		t.Errorf("backend %v", v)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(a.Path)
		t.Errorf("config.json magpie made is still there: %s", b)
	}
}

// A config.json that isn't JSON is left alone.
func TestKiroCrewLeavesBrokenAlone(t *testing.T) {
	_, a := kiroCrewSandbox(t, []string{"codex"}, []string{"codex"})
	bad := `{"agent": {"acp_backend": "kas",`
	os.WriteFile(a.Path, []byte(bad), 0o600)
	if _, err := a.ConnectHow(); err == nil {
		t.Error("connected over a broken config.json")
	}
	if b, _ := os.ReadFile(a.Path); string(b) != bad {
		t.Errorf("written: %s", b)
	}
}

// Kiro Crew's home is KIROCREW_HOME, ~ and a relative path made absolute,
// unless it is one Kiro Crew refuses; else ~/.kiro/crew.
func TestKiroCrewHome(t *testing.T) {
	home := t.TempDir()
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "KIROCREW_HOME" {
				return v
			}
			return ""
		}
	}
	def := filepath.Join(home, ".kiro", "crew")
	if got := kiroCrewHome(home, env("")); got != def {
		t.Errorf("default %s", got)
	}
	own := filepath.Join(home, "crew-two")
	os.MkdirAll(own, 0o700)
	own, _ = filepath.EvalSymlinks(own)
	if got := kiroCrewHome(home, env(own)); got != own {
		t.Errorf("KIROCREW_HOME %s", got)
	}
	if got, want := kiroCrewHome(home, env("~/x")), filepath.Join(home, "x"); got != want {
		t.Errorf("~/x: %s, want %s", got, want)
	}
	if runtime.GOOS != "windows" {
		for _, bad := range []string{"/", "/usr/local/crew", "/etc", "/System/x"} {
			if got := kiroCrewHome(home, env(bad)); got != def {
				t.Errorf("%s: %s, want the default", bad, got)
			}
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("KIROCREW_HOME", own)
	if a := kiroCrew(home); a.Path != filepath.Join(own, "config.json") {
		t.Errorf("path %s", a.Path)
	}
}

// The adapter is found where Kiro Crew looks: the variable naming it, a
// Node version's bin, Crew's own folders; never one of another name.
func TestKiroCrewFindsAdapters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	codex, _ := kiroCrewHarnessOf("codex")
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if kiroCrewFinds(codex, home, getenv) {
		t.Fatal("found with none")
	}
	put := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	}
	put(filepath.Join(home, ".nvm", "versions", "node", "v22.1.0", "bin", "claude-agent-acp"))
	if kiroCrewFinds(codex, home, getenv) {
		t.Error("codex-acp found as claude-agent-acp")
	}
	claude, _ := kiroCrewHarnessOf("claude")
	if !kiroCrewFinds(claude, home, getenv) {
		t.Error("claude-agent-acp under nvm not found")
	}
	put(filepath.Join(home, ".local", "share", "mise", "installs", "node", "24.0.0", "bin", "codex-acp"))
	if !kiroCrewFinds(codex, home, getenv) {
		t.Error("codex-acp under mise not found")
	}
	pi, _ := kiroCrewHarnessOf("pi")
	env["PI_ACP_BIN"] = filepath.Join(home, "elsewhere", "pi-acp")
	if kiroCrewFinds(pi, home, getenv) {
		t.Error("PI_ACP_BIN found with no file there")
	}
	put(env["PI_ACP_BIN"])
	if !kiroCrewFinds(pi, home, getenv) {
		t.Error("PI_ACP_BIN not found")
	}
}

// End to end, with magpie's own Codex wiring in the sandbox: Kiro Crew is
// offered Codex only once Codex is connected to magpie.
func TestKiroCrewOnRealCodexWiring(t *testing.T) {
	home := syncHome(t)
	t.Setenv("KIROCREW_HOME", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	h := kiroCrewHasAdapter
	t.Cleanup(func() { kiroCrewHasAdapter = h })
	kiroCrewHasAdapter = func(kiroCrewHarness) bool { return true }
	a := kiroCrew(home)
	os.MkdirAll(a.Dir, 0o700)
	os.WriteFile(a.Path, []byte(kiroCrewUser), 0o600)
	if _, err := a.ConnectHow(); !errors.Is(err, errKiroCrewNoHarness) {
		t.Fatalf("before Codex: %v", err)
	}
	cx := codex(home)
	os.MkdirAll(cx.Dir, 0o755)
	if _, err := cx.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if !cx.Wired() {
		t.Fatal("Codex not wired")
	}
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if v := a.Values(); v["provider"] != magpieID || v["harness"] != "codex" {
		t.Errorf("values %v", v)
	}
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Error("Kiro Crew still connected with Codex off magpie")
	}
}

// kiroCrewOnClaude is a sandbox HOME with Claude Code installed but not
// connected to magpie, nothing else, and Kiro Crew's config.json set to
// run on Claude Code, as wuping9525's Windows had it.
func kiroCrewOnClaude(t *testing.T) (a, cc *Agent, before []byte) {
	t.Helper()
	home := syncHome(t)
	t.Setenv("KIROCREW_HOME", "")
	t.Setenv("PATH", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	h := kiroCrewHasAdapter
	t.Cleanup(func() { kiroCrewHasAdapter = h })
	kiroCrewHasAdapter = func(kiroCrewHarness) bool { return true }
	cc = claude(home)
	if err := os.MkdirAll(cc.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cc.Path, []byte("{\"theme\": \"dark\"}\n"), 0o600)
	if !cc.Detected() || cc.Wired() {
		t.Fatalf("Claude Code detected %v, wired %v", cc.Detected(), cc.Wired())
	}
	a = kiroCrew(home)
	os.MkdirAll(a.Dir, 0o700)
	before = []byte(strings.Replace(kiroCrewUser, `"default_agent": "kirocrew",`, `"acp_backend": "claude",
    "default_agent": "kirocrew",`, 1))
	if err := os.WriteFile(a.Path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	return a, cc, before
}

// wuping9525: Kiro Crew set to Claude Code, Claude Code installed and not
// connected to magpie. Connecting Kiro Crew connects Claude Code too, in
// the one step, and runs Crew on it; Disconnect takes both off again and
// leaves the files as they were.
func TestKiroCrewConnectsItsHarness(t *testing.T) {
	a, cc, before := kiroCrewOnClaude(t)
	ccBefore, _ := os.ReadFile(cc.Path)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !cc.Wired() {
		t.Fatal("Claude Code not connected along with Kiro Crew")
	}
	if v := a.Values(); v["provider"] != magpieID || v["harness"] != "claude" {
		t.Errorf("values %v", v)
	}
	if n := a.Notice(); !strings.Contains(n, "magpie connected Claude Code too") {
		t.Errorf("notice %q", n)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if cc.Wired() {
		t.Error("Claude Code still connected after Kiro Crew's Disconnect")
	}
	if cc.Disconnected() {
		t.Error("Claude Code recorded as the user's own disconnect")
	}
	if b, _ := os.ReadFile(a.Path); string(b) != string(before) {
		t.Errorf("config.json\n%s\nwant\n%s", b, before)
	}
	// as Claude Code's own Disconnect leaves it: the user's settings kept,
	// nothing of magpie's
	if b, _ := os.ReadFile(cc.Path); !strings.Contains(string(b), `"theme": "dark"`) || strings.Contains(string(b), "ANTHROPIC") || strings.Contains(string(b), "magpie") {
		t.Errorf("Claude Code's settings.json\n%s\nwas\n%s", b, ccBefore)
	}
	if _, ok := stashLoad()[kiroCrewJoinedKey]; ok {
		t.Error("joined mark left")
	}
}

// A harness the user connected before is theirs: Kiro Crew runs on it and
// its Disconnect leaves it on. So is one the user disconnected and
// connected again themselves after Kiro Crew's connect.
func TestKiroCrewLeavesTheUsersHarness(t *testing.T) {
	t.Run("connected before", func(t *testing.T) {
		a, cc, _ := kiroCrewOnClaude(t)
		if _, err := cc.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(a.Notice(), "magpie connected") {
			t.Errorf("notice %q", a.Notice())
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if !cc.Wired() {
			t.Error("the user's Claude Code taken off")
		}
	})
	t.Run("taken back after", func(t *testing.T) {
		a, cc, _ := kiroCrewOnClaude(t)
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := cc.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if _, err := cc.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if !cc.Wired() {
			t.Error("Claude Code the user connected again taken off")
		}
	})
	t.Run("off before", func(t *testing.T) {
		// the user's disconnect from before stays theirs once Kiro Crew is off
		a, cc, _ := kiroCrewOnClaude(t)
		if _, err := cc.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := cc.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if !cc.Wired() {
			t.Fatal("Claude Code not connected along with Kiro Crew")
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if cc.Wired() || !cc.Disconnected() {
			t.Errorf("Claude Code wired %v, the user's disconnect %v", cc.Wired(), cc.Disconnected())
		}
	})
}

// Set to a harness that isn't installed here, Kiro Crew runs on one that
// is connected, as before; with none, it still says to connect one.
func TestKiroCrewHarnessNotHere(t *testing.T) {
	a, cc, _ := kiroCrewOnClaude(t)
	os.RemoveAll(cc.Dir)
	if cc.Detected() {
		t.Skip("Claude Code found here anyway")
	}
	if _, err := a.ConnectHow(); !errors.Is(err, errKiroCrewNoHarness) {
		t.Fatalf("connect: %v", err)
	}
	if cc.Wired() {
		t.Error("Claude Code connected while not installed")
	}
}

// On Windows Kiro Crew's home is under the user's profile the same way,
// its app is found by its venv's kirocrew.exe, and an adapter npm put in
// as a .cmd shim is found where Crew's which finds it.
func TestKiroCrewOnWindows(t *testing.T) {
	was := kiroCrewOS
	t.Cleanup(func() { kiroCrewOS = was })
	kiroCrewOS = "windows"
	home := syncHome(t)
	t.Setenv("KIROCREW_HOME", "")
	t.Setenv("PATH", t.TempDir())
	env := map[string]string{"USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local")}
	getenv := func(k string) string { return env[k] }
	put := func(p string) {
		t.Helper()
		if err := mkFile(p); err != nil {
			t.Fatal(err)
		}
	}
	a := kiroCrew(home)
	if a.Path != filepath.Join(home, ".kiro", "crew", "config.json") {
		t.Errorf("path %s", a.Path)
	}
	if a.Detected() {
		t.Fatal("detected with nothing here")
	}
	put(filepath.Join(home, ".kirocrew-app", ".venv", "Scripts", "kirocrew.exe"))
	if !a.Detected() {
		t.Error("Kiro Crew's app not detected by its venv's kirocrew.exe")
	}
	cl, _ := kiroCrewHarnessOf("claude")
	if kiroCrewFinds(cl, home, getenv) {
		t.Fatal("found with none")
	}
	put(filepath.Join(home, ".npm-global", "bin", "claude-agent-acp.cmd"))
	if !kiroCrewFinds(cl, home, getenv) {
		t.Error("claude-agent-acp.cmd not found")
	}
}

// setJSONFile sets dotted keys in a JSON file, as a user's editor would.
func setJSONFile(path string, kv map[string]any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range kv {
		o := m
		parts := strings.Split(k, ".")
		for _, p := range parts[:len(parts)-1] {
			n, ok := o[p].(map[string]any)
			if !ok {
				n = map[string]any{}
				o[p] = n
			}
			o = n
		}
		o[parts[len(parts)-1]] = v
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// mkFile makes an empty file and its folders.
func mkFile(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o755)
}

// A harness adapter bun installed globally is found in bun's bin, where
// BUN_INSTALL puts it, and on Windows npm's in %APPDATA%\npm.
func TestKiroCrewFindsBunAndWindowsNpm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	bunHome := filepath.Join(t.TempDir(), "bun-home")
	getenv := func(k string) string {
		if k == "BUN_INSTALL" {
			return bunHome
		}
		return ""
	}
	ext := ""
	if kiroCrewOS == "windows" {
		ext = ".exe"
	}
	h := kiroCrewHarness{agent: "zzbun-agent"}
	if kiroCrewFinds(h, home, getenv) {
		t.Fatal("found one that is not installed")
	}
	os.MkdirAll(filepath.Join(bunHome, "bin"), 0o755)
	os.WriteFile(filepath.Join(bunHome, "bin", "zzbun-agent"+ext), []byte("x"), 0o755)
	if !kiroCrewFinds(h, home, getenv) {
		t.Error("BUN_INSTALL's bin not searched")
	}
	h2 := kiroCrewHarness{agent: "zzbun-two"}
	os.MkdirAll(filepath.Join(home, ".bun", "bin"), 0o755)
	os.WriteFile(filepath.Join(home, ".bun", "bin", "zzbun-two"+ext), []byte("x"), 0o755)
	if !kiroCrewFinds(h2, home, func(string) string { return "" }) {
		t.Error("~/.bun/bin not searched")
	}
}
