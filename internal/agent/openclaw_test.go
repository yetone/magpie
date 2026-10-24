package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func openclawHome(t *testing.T, config string) (*Agent, func() []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "test-key", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2.1"}}); err != nil {
		t.Fatal(err)
	}
	a := openclaw(home)
	if config != "" {
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a.Path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a, func() []byte {
		b, err := os.ReadFile(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

// The install list must offer OpenClaw on a fresh home, without creating
// its config or pretending it is installed.
func TestOpenClawInstallOnFreshHome(t *testing.T) {
	a, _ := openclawHome(t, "")
	t.Setenv("PATH", t.TempDir())
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			for _, x := range installsOf(All(), goos, false) {
				if x.ID != a.ID {
					continue
				}
				if x.Name != a.Name || x.Icon != "openclaw-color" || len(x.Commands) != 1 || x.Missing {
					t.Fatalf("OpenClaw install entry: %+v", x)
				}
				if _, err := os.Stat(a.Dir); !os.IsNotExist(err) {
					t.Fatalf("listing created OpenClaw's config directory: %v", err)
				}
				return
			}
			t.Fatal("OpenClaw is missing from the install list on a fresh home")
		})
	}
}

func TestOpenClawRegistered(t *testing.T) {
	a, _ := openclawHome(t, "")
	t.Setenv("PATH", t.TempDir())
	found, err := Find("openclaw")
	if err != nil || found == nil || found.Path != a.Path {
		t.Fatalf("OpenClaw is missing from Agents: %v, %v", found, err)
	}
	if a.Detected() {
		t.Fatal("an uninstalled OpenClaw was detected")
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detected() {
		t.Fatal("OpenClaw config folder was not detected")
	}
}

func TestOpenClawConnectPickDisconnect(t *testing.T) {
	const initial = `{
  // Keep my chat channels and fallback models.
  "agents": {"defaults": {
    "model": {"primary": "mine/original", "fallbacks": ["mine/backup"]},
    "models": {"mine/original": {"alias": "original"}},
    "workspace": "/fixture/work"
  }},
  "models": {"mode": "merge", "providers": {"mine": {
    "baseUrl": "https://example.com/v1", "apiKey": "mine-key",
    "api": "openai-completions", "models": [{"id": "original", "name": "Original"}]
  }}},
  "channels": {"telegram": {"enabled": false}},
  "gateway": {"port": 18789}
}`
	a, read := openclawHome(t, initial)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() || a.Drift() != nil {
		t.Fatalf("not connected: model=%q drift=%+v", a.Field("model").Get(), a.Drift())
	}
	if err := a.Pick("model", "magpie/relay/m2.1"); err != nil {
		t.Fatal(err)
	}
	_, data, err := openclawRead(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		openclawModel + ".primary":               "magpie/relay/m2.1",
		openclawProvider + ".baseUrl":            gateway.URL() + "/v1",
		openclawProvider + ".apiKey":             keyAt(gateway.URL()),
		openclawProvider + ".api":                "openai-completions",
		openclawProvider + ".headers.User-Agent": "openclaw",
		"agents.defaults.workspace":              "/fixture/work",
		"models.mode":                            "merge",
	} {
		if got := gjson.GetBytes(data, key).String(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, ref := range []string{"magpie/relay/m1", `magpie/relay/m2\.1`} {
		if !gjson.GetBytes(data, "agents.defaults.models."+ref).Exists() {
			t.Errorf("%s is missing from OpenClaw's model menu", ref)
		}
	}
	if !bytes.Contains(read(), []byte("// Keep my chat channels")) {
		t.Fatal("JSONC comments were lost")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() || a.Field("model").Get() != "mine/original" {
		t.Fatalf("disconnect didn't restore model: %q", a.Field("model").Get())
	}
	_, data, _ = openclawRead(a.Path)
	if gjson.GetBytes(data, openclawProvider).Exists() || len(gjson.GetBytes(data, "agents.defaults.models").Map()) != 1 ||
		gjson.GetBytes(data, openclawModel+".fallbacks.0").String() != "mine/backup" ||
		!gjson.GetBytes(data, "models.providers.mine").Exists() || !gjson.GetBytes(data, "channels.telegram").Exists() {
		t.Fatalf("disconnect changed user config: %s", read())
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "magpie/relay/m2.1" {
		t.Fatal("reconnect didn't bring back last pick")
	}
}

func TestOpenClawModelShapes(t *testing.T) {
	for _, model := range []string{`"mine/original"`, `{"primary":"mine/original"}`, `{"fallbacks":["mine/backup"]}`, `null`} {
		t.Run(model, func(t *testing.T) {
			config := `{"agents":{"defaults":{"model":` + model + `}}}`
			if model == "null" {
				config = `{}`
			}
			a, _ := openclawHome(t, config)
			f := a.Field("model")
			if err := f.Set("magpie/relay/m1"); err != nil {
				t.Fatal(err)
			}
			if err := f.Set("magpie/relay/m2.1"); err != nil {
				t.Fatal(err)
			}
			if err := f.Set(""); err != nil {
				t.Fatal(err)
			}
			_, data, _ := openclawRead(a.Path)
			got := gjson.GetBytes(data, openclawModel)
			if model == "null" {
				if got.Exists() {
					t.Fatalf("unset model was not removed: %s", data)
				}
			} else if !sameJSON(got.Raw, json.RawMessage(model)) {
				t.Fatalf("model shape changed: %s, want %s", got.Raw, model)
			}
			if gjson.GetBytes(data, "agents.defaults.models").Exists() {
				t.Fatal("an unrestricted model menu was restricted")
			}
		})
	}
}

func TestOpenClawDisconnectKeepsUserEdits(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original","models":{"mine/original":{}},"modelPolicy":{"allow":["mine/original"]}}}}`)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(a.Path,
		edit.KV{Path: openclawModel + ".primary", Value: "magpie/relay/m2.1"},
		edit.KV{Path: openclawModel + ".fallbacks", Value: []string{"mine/new-backup"}},
		edit.KV{Path: "agents.defaults.modelPolicy.allow", Value: []string{"mine/original", "magpie/*", "mine/new"}},
		edit.KV{Path: `agents.defaults.models.magpie/relay/m2\.1`, Value: map[string]any{"alias": "edited"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	_, data, _ := openclawRead(a.Path)
	if a.Field("model").Get() != "mine/original" || gjson.GetBytes(data, openclawModel+".fallbacks.0").String() != "mine/new-backup" ||
		gjson.GetBytes(data, `agents.defaults.models.magpie/relay/m2\.1.alias`).String() != "edited" ||
		gjson.GetBytes(data, "agents.defaults.modelPolicy.allow").Raw != `["mine/original","mine/new"]` {
		t.Fatalf("user edits lost: %s", read())
	}
}

func TestOpenClawSyncAndDrift(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original","models":{"mine/original":{}}}}}`)
	if err := a.Apply("model", "magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	before := read()
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, read()) {
		t.Fatal("unchanged catalog rewrote the config")
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "test-key", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "new"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	_, data, _ := openclawRead(a.Path)
	if !gjson.GetBytes(data, "agents.defaults.models.magpie/relay/new").Exists() || gjson.GetBytes(data, `agents.defaults.models.magpie/relay/m2\.1`).Exists() {
		t.Fatalf("stale menu: %s", read())
	}
	if err := edit.SetJSON(a.Path, edit.KV{Path: openclawProvider + ".baseUrl", Value: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("changed endpoint didn't report drift: %+v", d)
	}
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	if a.Drift() != nil {
		t.Fatal("reapply didn't restore wiring")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "mine/original" {
		t.Fatal("sync lost the original shorthand")
	}
}

func TestOpenClawForeignProvider(t *testing.T) {
	a, read := openclawHome(t, `{"models":{"providers":{"magpie":{"baseUrl":"https://mine.example/v1","apiKey":"own-key","models":[]}}}}`)
	before := read()
	if err := a.Sync(); err != nil || !bytes.Equal(before, read()) {
		t.Fatalf("sync rewrote user's provider: %v", err)
	}
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("mine/picked"); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "mine/picked" || !strings.Contains(string(read()), "own-key") {
		t.Fatal("picking an own model didn't restore own provider")
	}
}

func TestOpenClawJSON5AndUnsafeConfigs(t *testing.T) {
	const original = `// my notes: keep port for the bot
{
  // primary model
  agents: {defaults: {model: 'mine/original'}},
  gateway: {port: 18789},
}
`
	a, read := openclawHome(t, original)
	if a.Field("model").Get() != "mine/original" {
		t.Fatal("JSON5 was not read")
	}
	if _, err := os.Stat(a.Path + ".before-magpie"); !os.IsNotExist(err) {
		t.Fatal("reading JSON5 rewrote or copied the file")
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(read()) || gjson.GetBytes(read(), "gateway.port").Type != gjson.Number || gjson.GetBytes(read(), "gateway.port").Int() != 18789 {
		t.Fatal("JSON5 normalization changed a numeric setting")
	}
	connected := read()
	if bytes.Contains(connected, []byte("my notes")) || bytes.Contains(connected, []byte("primary model")) ||
		gjson.GetBytes(connected, "agents.defaults.model.primary").String() != "magpie/relay/m1" {
		t.Fatalf("connected JSON5 was not normalized: %s", connected)
	}
	saved, err := os.ReadFile(a.Path + ".before-magpie")
	if err != nil || string(saved) != original {
		t.Fatalf("original JSON5 was not kept: %v\n%s", err, saved)
	}
	if err := a.Disconnect(); err != nil || a.Field("model").Get() != "mine/original" {
		t.Fatalf("JSON5 restoration failed: %v", err)
	}
	if string(read()) != original {
		t.Fatalf("disconnect changed JSON5:\n%s", read())
	}
	if _, err := os.Stat(a.Path + ".before-magpie"); !os.IsNotExist(err) {
		t.Fatal("restored JSON5 kept its backup")
	}
	for _, config := range []string{`{"agents":`, `{"$include":"other.json"}`, `{"agents":{"$include":"agents.json"}}`, `{} trailing`} {
		t.Run(config, func(t *testing.T) {
			a, read := openclawHome(t, config)
			if err := a.Field("model").Set("magpie/relay/m1"); err == nil {
				t.Fatal("unsafe config was accepted")
			}
			if string(read()) != config {
				t.Fatal("unsafe config was overwritten")
			}
		})
	}
}

func TestOpenClawJSON5ReconnectAfterUserEdit(t *testing.T) {
	const original = "// first connection\n{agents:{defaults:{model:'mine/original'}},gateway:{port:18789}}\n"
	for _, port := range []string{"18790", "18789"} {
		t.Run(port, func(t *testing.T) {
			a, read := openclawHome(t, original)
			if err := a.Connect(); err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(read(), []byte("18789"), []byte("18790"), 1)
			if err := os.WriteFile(a.Path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(read(), "gateway.port").Int() != 18790 {
				t.Fatal("disconnect lost the user's port edit")
			}
			if _, err := os.Stat(openclawOriginal(a.Path)); !os.IsNotExist(err) {
				t.Error("disconnect kept an obsolete JSON5 backup")
			}
			// A fresh JSON5 may have different values, or the same values as the
			// first backup but new comments. Both must survive the next cycle.
			next := "// new notes for the second connection\n{agents:{defaults:{model:'mine/original'}},gateway:{port:" + port + "}}\n"
			if err := os.WriteFile(a.Path, []byte(next), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := a.Connect(); err != nil {
				t.Fatal(err)
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if string(read()) != next {
				t.Fatalf("reconnect lost the new JSON5 bytes:\n%s", read())
			}
			if _, err := os.Stat(openclawOriginal(a.Path)); !os.IsNotExist(err) {
				t.Fatal("second disconnect kept its backup")
			}
		})
	}
}

func TestOpenClawPathsAndReadOnly(t *testing.T) {
	a, _ := openclawHome(t, "")
	home := filepath.Dir(a.Dir)
	other := t.TempDir()
	t.Setenv("OPENCLAW_HOME", other)
	if got := openclaw(home).Dir; got != filepath.Join(other, ".openclaw") {
		t.Fatalf("OPENCLAW_HOME ignored: %s", got)
	}
	t.Setenv("OPENCLAW_STATE_DIR", filepath.Join(other, "state"))
	if got := openclaw(home).Path; got != filepath.Join(other, "state", "openclaw.json") {
		t.Fatalf("OPENCLAW_STATE_DIR ignored: %s", got)
	}
	t.Setenv("OPENCLAW_CONFIG_PATH", filepath.Join(other, "custom.json"))
	a = openclaw(home)
	if a.Path != filepath.Join(other, "custom.json") {
		t.Fatal("OPENCLAW_CONFIG_PATH ignored")
	}
	for _, env := range []string{"OPENCLAW_CONFIG_READONLY", "OPENCLAW_NIX_MODE"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			if err := a.Field("model").Set("magpie/relay/m1"); err == nil {
				t.Fatal("read-only config was written")
			}
			if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
				t.Fatal("read-only config was created")
			}
		})
	}
}

func TestOpenClawTildePaths(t *testing.T) {
	a, _ := openclawHome(t, "")
	home := filepath.Dir(a.Dir)
	for _, override := range []string{"", filepath.Join(home, "other")} {
		t.Run("home="+override, func(t *testing.T) {
			t.Setenv("OPENCLAW_HOME", override)
			base := override
			if base == "" {
				base = home
			}
			t.Setenv("OPENCLAW_STATE_DIR", "~/state")
			if got := openclaw(home); got.Dir != filepath.Join(base, "state") || got.Path != filepath.Join(base, "state", "openclaw.json") {
				t.Fatalf("tilde state directory: %s, %s", got.Dir, got.Path)
			}
			t.Setenv("OPENCLAW_CONFIG_PATH", "~/custom.json")
			if got := openclaw(home).Path; got != filepath.Join(base, "custom.json") {
				t.Fatalf("tilde config path: %s", got)
			}
			t.Setenv("OPENCLAW_STATE_DIR", "~")
			if got := openclaw(home).Dir; got != base {
				t.Fatalf("bare tilde state directory: %s", got)
			}
		})
	}
	t.Setenv("OPENCLAW_HOME", "~/other")
	if got := openclaw(home).Dir; got != filepath.Join(home, "other", ".openclaw") {
		t.Fatalf("tilde home: %s", got)
	}
}

// Sync's address filter runs inside WriteAtomic. The saved provider must
// match those final bytes, so Disconnect can still restore the user's own.
func TestOpenClawSyncKeepsProxy(t *testing.T) {
	for _, proxy := range []string{corpProxy, localProxy} {
		for _, original := range []string{"", `{"baseUrl":"https://mine.example/v1","apiKey":"own-key","models":[]}`} {
			t.Run(proxy+original, func(t *testing.T) {
				a, read := openclawHome(t, `{}`)
				if original != "" {
					if err := edit.SetJSON(a.Path, edit.KV{Path: openclawProvider, Value: json.RawMessage(original)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := a.Connect(); err != nil {
					t.Fatal(err)
				}
				if err := edit.SetJSON(a.Path, edit.KV{Path: openclawProvider + ".baseUrl", Value: proxy + "/v1"}); err != nil {
					t.Fatal(err)
				}
				fakeMagpieAt(t, map[string]bool{proxy: true})
				for range 2 {
					if err := synced(a.Sync); err != nil {
						t.Fatal(err)
					}
					cur := gjson.GetBytes(read(), openclawProvider)
					if cur.Get("baseUrl").String() != proxy+"/v1" {
						t.Fatalf("sync lost proxy: %s", cur.Raw)
					}
					rec, err := openclawSaved("openclaw.config:.openclaw/openclaw.json")
					if err != nil {
						t.Fatal(err)
					}
					if !sameJSON(cur.Raw, rec.Edits[openclawProvider].After) {
						t.Error("saved provider differs from the provider written through the proxy")
					}
				}
				if err := a.Disconnect(); err != nil {
					t.Fatal(err)
				}
				if got := gjson.GetBytes(read(), openclawProvider); got.Exists() != (original != "") || original != "" && !sameJSON(got.Raw, json.RawMessage(original)) {
					t.Fatalf("disconnect left %s, want original provider %s", got.Raw, original)
				}
			})
		}
	}
}

func TestOpenClawJSON5Numbers(t *testing.T) {
	const original = `{gateway:{port:0x4965},agents:{defaults:{model:'mine/original',timeoutSeconds:+120,contextTokens:123456789012345}}}`
	a, read := openclawHome(t, original)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	connected := read()
	for k, want := range map[string]int64{"gateway.port": 18789, "agents.defaults.timeoutSeconds": 120, "agents.defaults.contextTokens": 123456789012345} {
		if v := gjson.GetBytes(connected, k); v.Type != gjson.Number || v.Int() != want {
			t.Fatalf("%s changed type or precision while connected: %s", k, connected)
		}
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if string(read()) != original {
		t.Fatalf("JSON5 number syntax was not restored:\n%s", read())
	}
}

func TestOpenClawNativeAPIs(t *testing.T) {
	a, _ := openclawHome(t, "")
	for _, p := range []provider.Provider{
		{ID: "responses-fixture", Name: "Responses", Responses: "https://example.com/v1", Key: "fake", Models: []string{"gpt"}},
		{ID: "anthropic-fixture", Name: "Anthropic", Anthropic: "https://example.com", Key: "fake", Models: []string{"claude"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	_, data, _ := openclawRead(a.Path)
	entries := map[string]gjson.Result{}
	gjson.GetBytes(data, openclawProvider+".models").ForEach(func(_, m gjson.Result) bool {
		entries[m.Get("id").String()] = m
		return true
	})
	if entries["responses-fixture/gpt"].Get("api").String() != "openai-responses" ||
		entries["anthropic-fixture/claude"].Get("api").String() != "anthropic-messages" ||
		entries["anthropic-fixture/claude"].Get("baseUrl").String() != gateway.URL() {
		t.Fatalf("native API adapters missing: %s", data)
	}
}

func TestOpenClawModelMetadata(t *testing.T) {
	m := catalog.Model{ID: "relay/reasoner", Name: "Reasoner", Context: 32768, Output: 65536, Images: true, Efforts: []string{"low", "high"}, Price: &catalog.Price{Input: 1, Output: 2}}
	e := openclawModelJSON(m)
	if e["contextWindow"] != 32768 || e["maxTokens"] != 32768 || e["reasoning"] != true || !slices.Contains(e["input"].([]string), "image") {
		t.Fatalf("wrong model capabilities: %v", e)
	}
	if e["cost"].(map[string]any)["input"] != float64(1) {
		t.Fatalf("missing model price: %v", e)
	}
}

func TestOpenClawWSL(t *testing.T) {
	a, _ := openclawHome(t, "")
	home := filepath.Dir(a.Dir)
	t.Setenv("OPENCLAW_CONFIG_PATH", filepath.Join(t.TempDir(), "windows.json"))
	t.Setenv("OPENCLAW_STATE_DIR", filepath.Join(t.TempDir(), "windows-state"))
	t.Setenv("OPENCLAW_HOME", t.TempDir())
	t.Setenv("OPENCLAW_CONFIG_READONLY", "1")
	t.Setenv("OPENCLAW_NIX_MODE", "1")
	wsl := openclawIn(place{home: home, id: "openclaw@wsl:Ubuntu", spell: func(p string) string { return p }, base: func() string { return "http://172.22.0.1:20186" }})
	if wsl.Path != a.Path {
		t.Fatal("WSL read Windows' config variable")
	}
	if err := wsl.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	_, data, _ := openclawRead(wsl.Path)
	if gjson.GetBytes(data, openclawProvider+".baseUrl").String() != "http://172.22.0.1:20186/v1" || wsl.Check() != "" {
		t.Fatalf("WSL gateway is wrong: %s", data)
	}
	if err := wsl.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
}

func TestOpenClawConcurrentSyncAndPick(t *testing.T) {
	a, _ := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original"}}}`)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			if i%2 == 0 {
				if err := a.Sync(); err != nil {
					t.Error(err)
				}
			} else if err := a.Field("model").Set("magpie/relay/m2.1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := a.Disconnect(); err != nil || a.Field("model").Get() != "mine/original" {
		t.Fatalf("concurrent edits lost original model: %v, %q", err, a.Field("model").Get())
	}
}

func TestOpenClawCorruptSavedConfig(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original"}}}`)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	before := read()
	values := stashLoad()
	for k := range values {
		if strings.HasPrefix(k, "openclaw.config:") {
			values[k] = "broken"
		}
	}
	stashPut(values)
	if err := a.Disconnect(); err == nil || !bytes.Equal(before, read()) {
		t.Fatal("unreadable saved config was treated as empty")
	}
}

func TestOpenClawDisconnectPreview(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original"}}}`)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	before := read()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := DisconnectPreview(a, exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 || !bytes.Equal(before, read()) {
		t.Fatalf("preview changed files or showed no restore: %+v", changes)
	}
	found := false
	for _, c := range changes {
		for _, line := range c.Lines {
			if strings.Contains(line.Text, "mine/original") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("preview didn't restore original model: %+v", changes)
	}
}

func TestOpenClawModelChain(t *testing.T) {
	for _, original := range []string{`"mine/original"`, `{"primary":"mine/original","fallbacks":["mine/backup"],"other":true}`} {
		t.Run(original, func(t *testing.T) {
			a, read := openclawHome(t, `{"agents":{"defaults":{"model":`+original+`}},"other":42}`)
			c := ModelChain{Primary: "magpie/group/auto", Fallbacks: []string{"magpie/group/fast", "anthropic/claude-opus-5"}}
			if err := a.ApplyModelChain(c); err != nil {
				t.Fatal(err)
			}
			got, err := a.ReadModelChain()
			if err != nil || got.Primary != c.Primary || !slices.Equal(got.Fallbacks, c.Fallbacks) {
				t.Fatalf("read %v %v", got, err)
			}
			c.Primary = "anthropic/claude-opus-5"
			c.Fallbacks = []string{"magpie/group/fast"}
			if err := a.ApplyModelChain(c); err != nil {
				t.Fatal(err)
			}
			if !a.Wired() {
				t.Fatal("fallback-only connection lost")
			}
			c.Fallbacks = []string{}
			if err := a.ApplyModelChain(c); err != nil {
				t.Fatal(err)
			}
			if got, _ := a.ReadModelChain(); len(got.Fallbacks) != 0 {
				t.Fatal(got)
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if !sameJSON(gjson.GetBytes(read(), openclawModel).Raw, json.RawMessage(original)) {
				t.Fatalf("restore: %s", read())
			}
			if gjson.GetBytes(read(), "other").Int() != 42 {
				t.Fatal("unrelated config changed")
			}
		})
	}
}

func TestOpenClawModelChainInvalid(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original"}}}`)
	before := read()
	for _, c := range []ModelChain{{Primary: ""}, {Primary: "invalid"}, {Primary: "mine/a", Fallbacks: []string{"mine/a"}}, {Primary: "mine/a", Fallbacks: []string{""}}} {
		if err := a.ApplyModelChain(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
		if !bytes.Equal(before, read()) {
			t.Fatal("invalid chain changed config")
		}
	}
}

func TestOpenClawModelChainReconnect(t *testing.T) {
	a, _ := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original"}}}`)
	want := ModelChain{Primary: "anthropic/claude-opus-5", Fallbacks: []string{"magpie/group/fast"}}
	if err := a.ApplyModelChain(want); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	got, err := a.ReadModelChain()
	if err != nil || got.Primary != want.Primary || !slices.Equal(got.Fallbacks, want.Fallbacks) {
		t.Fatalf("reconnect %+v %v", got, err)
	}
}

func TestOpenClawModelChainRestricted(t *testing.T) {
	a, read := openclawHome(t, `{"agents":{"defaults":{"model":"mine/original","models":{"mine/original":{}},"modelPolicy":{"allow":["mine/*"]}}}}`)
	want := ModelChain{Primary: "magpie/group/auto", Fallbacks: []string{"anthropic/claude-opus-5"}}
	if err := a.ApplyModelChain(want); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(read(), "agents.defaults.models.anthropic/claude-opus-5").Exists() {
		t.Fatal("fallback absent from restricted menu")
	}
	if !strings.Contains(gjson.GetBytes(read(), "agents.defaults.modelPolicy.allow").Raw, "anthropic/claude-opus-5") {
		t.Fatal("fallback excluded by policy")
	}
	out, err := edit.PatchJSON(read(), []edit.KV{{Path: openclawModel + ".fallbacks", Value: []string{"mine/changed"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(read(), openclawModel+".fallbacks.0").String() != "mine/changed" {
		t.Fatal("external edit lost")
	}
}

func TestOpenClawAgentModelOverride(t *testing.T) {
	for _, tc := range []struct{ roster, target string }{
		{`"entries":{"main":{"model":{"primary":"mine/override"}},"other":{"model":"other/keep"}},"defaults":{"systemAgent":{"agentId":"main"},"model":{"primary":"mine/default","fallbacks":["mine/backup"]}}`, "agents.entries.main.model"},
		{`"entries":{"main":{"model":"mine/override"}},"defaults":{"model":{"primary":"mine/default","fallbacks":["mine/backup"]}}`, "agents.entries.main.model"},
		{`"list":[{"id":"other","model":"other/keep"},{"id":"main","default":true,"model":{"primary":"mine/override"}}],"defaults":{"model":{"primary":"mine/default","fallbacks":["mine/backup"]}}`, "agents.list.1.model"},
	} {
		t.Run(tc.target+tc.roster, func(t *testing.T) {
			a, read := openclawHome(t, `{"agents":{`+tc.roster+`}}`)
			before := gjson.GetBytes(read(), tc.target).Raw
			c, err := a.ReadModelChain()
			if err != nil || c.Primary != "mine/override" || len(c.Fallbacks) != 0 {
				t.Fatalf("effective model: %+v, %v", c, err)
			}
			c = ModelChain{Primary: "magpie/group/auto", Fallbacks: []string{}}
			if err := a.ApplyModelChain(c); err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(read(), tc.target+".primary").String(); got != c.Primary {
				t.Fatalf("override still wins: %s", got)
			}
			got, err := a.ReadModelChain()
			if err != nil || got.Primary != c.Primary || len(got.Fallbacks) != 0 {
				t.Fatalf("effective saved chain: %+v %v", got, err)
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if !sameJSON(gjson.GetBytes(read(), tc.target).Raw, json.RawMessage(before)) {
				t.Fatalf("restore override: %s", read())
			}
		})
	}
}
