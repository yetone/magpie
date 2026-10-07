package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// movedPlugin sets up a built-in moved onto its plugin, id's accounts as
// auths (by key), the plugin's provider as though it had just been asked,
// with no Bun.
func movedPlugin(t *testing.T, id string, auths map[string]map[string]any) {
	t.Helper()
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	spec := "opencode-" + id + "-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", auths)
	plugin.UseCached([]plugin.Provider{{ID: id, Spec: spec, Name: strings.ToUpper(id[:1]) + id[1:],
		Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}}})
	// and a change the test told the plugins' hooks (signing out with no
	// host) is over before the next test, whose holds it would drop
	t.Cleanup(func() { plugin.UseCached(nil); plugin.Told() })
	if err := setMigration(id, func(m *Migration) { m.State, m.Package = MovePlugin, spec }); err != nil {
		t.Fatal(err)
	}
	old := pluginOwnUser
	t.Cleanup(func() { pluginOwnUser = old })
}

func listed(agent string) string {
	var out []string
	for _, l := range Logins(agent) {
		u := l.User
		if l.Active {
			u = "*" + u
		}
		if l.Own {
			u += " (own)"
		}
		out = append(out, u)
	}
	return strings.Join(out, ",")
}

// The agent's own account, moved onto its plugin, is still the agent's:
// named as the agent is signed in now, removed in magpie it is only hidden
// (the agent stays signed in), and it shows again once the agent signs in
// to another account, as the built-in's did.
func TestMovedOwnAccount(t *testing.T) {
	movedPlugin(t, "devin", map[string]map[string]any{
		"devin":   {"type": "api", "key": "cog_own_key_1234", "metadata": map[string]any{"email": "old@devin", "cli": true}},
		"devin#2": {"type": "api", "key": "cog_two_key_5678", "metadata": map[string]any{"email": "two@devin"}},
	})
	pluginOwnUser = func(id string) string {
		if id == "devin" {
			return "me@devin"
		}
		return ""
	}
	if got := listed("devin"); got != "*me@devin (own),two@devin" {
		t.Fatalf("accounts %s", got)
	}
	if err := ForgetLogin("devin", "me@devin"); err != nil {
		t.Fatal(err)
	}
	if got := listed("devin"); got != "*two@devin" {
		t.Fatalf("after removing the CLI's own: %s", got)
	}
	if _, ok := plugin.Auths("devin")["devin"]; !ok {
		t.Fatal("removing the CLI's own account signed the CLI out")
	}
	// the CLI signs in to another account: it shows again
	pluginOwnUser = func(string) string { return "new@devin" }
	if got := listed("devin"); got != "two@devin,new@devin (own)" && got != "*two@devin,new@devin (own)" {
		t.Fatalf("after the CLI signed in anew: %s", got)
	}
}

// Qoder's first account can be removed, the next put first, as the
// built-in let it be: the Remove button it shows works.
func TestMovedQoderFirstGoes(t *testing.T) {
	movedPlugin(t, "qoder", map[string]map[string]any{
		"qoder":   {"type": "oauth", "access": "a", "refresh": "r", "expires": 0, "accountId": "one@q"},
		"qoder#2": {"type": "oauth", "access": "b", "refresh": "s", "expires": 0, "accountId": "two@q"},
	})
	if got := listed("qoder"); got != "*one@q,two@q" {
		t.Fatalf("accounts %s", got)
	}
	if err := ForgetLogin("qoder", "one@q"); err != nil {
		t.Fatal(err)
	}
	if got := listed("qoder"); got != "*two@q" {
		t.Fatalf("after removing the first: %s", got)
	}
	if _, ok := plugin.Auths("qoder")["qoder"]; ok {
		t.Fatal("the removed account is still signed in")
	}
}

// Deleting a moved provider hides it, as deleting the built-in did: its
// accounts stay signed in.
func TestDeleteMovedHides(t *testing.T) {
	movedPlugin(t, "zed", map[string]map[string]any{
		"zed": {"type": "oauth", "access": "a", "refresh": "r", "expires": 0, "accountId": "me@zed"},
	})
	if _, err := Find("zed"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("zed"); err != nil {
		t.Fatal(err)
	}
	if len(plugin.Auths("zed")) != 1 {
		t.Fatal("deleting the moved provider signed its account out")
	}
	hidden := false
	for _, p := range Hidden() {
		hidden = hidden || p.ID == "zed"
	}
	for _, p := range All() {
		if p.ID == "zed" {
			t.Fatal("deleted, zed is still listed")
		}
	}
	if !hidden {
		t.Fatal("zed isn't among the hidden")
	}
}

// Every agent's accounts list a moved built-in's by its id, the one in use
// marked, not as plugin:<id>.
func TestAllLoginsMoved(t *testing.T) {
	movedPlugin(t, "zed", map[string]map[string]any{
		"zed":   {"type": "oauth", "access": "a", "refresh": "r", "expires": 0, "accountId": "me@zed"},
		"zed#2": {"type": "oauth", "access": "b", "refresh": "s", "expires": 0, "accountId": "two@zed"},
	})
	var got []string
	for _, l := range Logins("") {
		if strings.Contains(l.Agent, "zed") {
			s := l.Agent + ":" + l.User
			if l.Active {
				s = "*" + s
			}
			got = append(got, s)
		}
	}
	if strings.Join(got, ",") != "*zed:me@zed,zed:two@zed" {
		t.Fatalf("every agent's accounts: %v", got)
	}
}

// A moved Devin's sign-in goes as the built-in's did: the Devin CLI
// installed first when it isn't there, the code the page asks about shown
// to copy, and the account it signs in to named as the one in use.
func TestMovedSignInAsBuiltIn(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(home, "devin-plugin")
	os.MkdirAll(dir, 0o755)
	src := filepath.Join(dir, "index.js")
	os.WriteFile(src, []byte(`export const P = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.devin = { name: "Devin", npm: "@ai-sdk/openai-compatible", api: "https://fake.invalid/v1", models: { m: { name: "M" } } }
  },
  auth: {
    provider: "devin",
    methods: [{
      type: "oauth",
      label: "Devin (browser)",
      authorize: async () => ({
        url: "https://fake.invalid/device",
        instructions: "Confirm the code ABCD-EFGH on Devin's page",
        method: "auto",
        callback: async () => {
          await new Promise((r) => setTimeout(r, 300))
          return { type: "success", refresh: "r", access: "a", expires: 0, accountId: "dev@fake" }
        },
      }),
    }],
  },
})
`), 0o644)
	if _, err := plugin.Add(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := setMigration("devin", func(m *Migration) { m.State, m.Package = MovePlugin, src }); err != nil {
		t.Fatal(err)
	}
	old := pluginOwnUser
	pluginOwnUser = func(string) string { return "" }
	t.Cleanup(func() { pluginOwnUser = old })
	exe := filepath.Join(home, "bin", "devin")
	oldExe := DevinExecutable
	DevinExecutable = func() string {
		if isFile(exe) {
			return exe
		}
		return ""
	}
	t.Cleanup(func() { DevinExecutable = oldExe })
	release := make(chan struct{})
	oldRun := runInstaller
	runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
		<-release
		os.MkdirAll(filepath.Dir(exe), 0o755)
		return nil, testenv.WriteProgram(exe, "#!/bin/sh\n")
	}
	t.Cleanup(func() { runInstaller = oldRun })

	st, err := StartPluginSignIn("devin", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Agent != "devin" || st.State != "installing" || st.Installing != "Devin CLI" || st.URL != "" {
		t.Fatalf("started %+v", st)
	}
	close(release)
	st = waitPast(t, st.ID, "installing")
	if st.State != "waiting" || st.URL != "https://fake.invalid/device" || st.Code != "ABCD-EFGH" {
		t.Fatalf("after the install %+v", st)
	}
	st = waitPast(t, st.ID, "waiting")
	if st.State != "done" || st.User != "dev@fake" || !st.Using {
		t.Fatalf("finished %+v", st)
	}
}

// magpie accounts lists a moved built-in's accounts where the built-in's
// stood, not after every other: Grok's before Zed's, whichever order the
// plugins come in.
func TestAllLoginsMovedKeepTheirPlace(t *testing.T) {
	movedPlugin(t, "zed", map[string]map[string]any{
		"zed":  {"type": "oauth", "access": "a", "refresh": "r", "expires": 0, "accountId": "me@zed"},
		"grok": {"type": "oauth", "access": "b", "refresh": "s", "expires": 0, "accountId": "me@grok"},
	})
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": "opencode-zed-auth"}, {"spec": "opencode-grok-auth"}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	plugin.UseCached([]plugin.Provider{{ID: "zed", Spec: "opencode-zed-auth", Name: "Zed"}, {ID: "grok", Spec: "opencode-grok-auth", Name: "Grok"}})
	if err := setMigration("grok", func(m *Migration) { m.State, m.Package = MovePlugin, "opencode-grok-auth" }); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range Logins("") {
		if l.Agent == "zed" || l.Agent == "grok" {
			got = append(got, l.Agent)
		}
	}
	if strings.Join(got, ",") != "grok,zed" {
		t.Fatalf("every agent's accounts: %v, want grok's before zed's", got)
	}
}
