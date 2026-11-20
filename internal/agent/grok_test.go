package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestGrok(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "grok", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# mine\n[ui]\ntheme = \"dark\"\n\n[model.my-own]\nmodel = \"x\"\nbase_url = \"https://x/v1\"\n\n[models]\ndefault = \"grok-4.6\"\n"), 0o644)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	table := func(name string) map[string]string {
		t.Helper()
		got, err := edit.GetTOMLTable(path, name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a := grok(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "grok-4.6" {
		t.Fatalf("get: %q", f.Get())
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	raw := read()
	m := table(`model."magpie/deepseek/pro"`)
	if m["model"] != "deepseek/pro" || m["api_key"] != "magpie" || !strings.HasSuffix(m["base_url"], "/v1") ||
		m["api_backend"] != "chat_completions" || table(`model."magpie/deepseek/flash"`) == nil {
		t.Fatalf("tables:\n%s", raw)
	}
	// a campaign of xAI's would set the default over it
	if table("features")["campaigns"] != "false" {
		t.Fatalf("campaigns:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || !strings.Contains(raw, "# mine") || !strings.Contains(raw, "[model.my-own]") ||
		table("ui")["theme"] != "dark" {
		t.Fatalf("config:\n%s", raw)
	}

	// picked again, and synced: the tables are replaced, never repeated
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if n := strings.Count(raw, `[model."magpie/deepseek/pro"]`); n != 1 {
		t.Fatalf("%d tables:\n%s", n, raw)
	}

	// a provider removed leaves Grok's list on the next sync
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"flash"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), `"magpie/deepseek/pro"]`) {
		t.Fatalf("stale:\n%s", read())
	}

	// its own model: magpie steps out, the user's own model stays
	if err := f.Set("grok-4.6"); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if f.Get() != "grok-4.6" || strings.Contains(raw, "magpie") || !strings.Contains(raw, "[model.my-own]") {
		t.Fatalf("own:\n%s", raw)
	}
	// nothing through magpie: sync leaves the file alone
	if err := a.Sync(); err != nil || read() != raw {
		t.Fatalf("sync touched it:\n%s", read())
	}

	e := a.Field("effort")
	f.Set("magpie/deepseek/flash")
	if err := e.Set("high"); err != nil || e.Get() != "high" {
		t.Fatalf("effort: %v %q", err, e.Get())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if f.Get() != "" || strings.Contains(raw, "magpie") || strings.Contains(raw, "campaigns") || e.Get() != "high" {
		t.Fatalf("reset:\n%s", raw)
	}
}

func TestGrokReadErrorStopsChanges(t *testing.T) {
	const wired = "[model.\"magpie/keep\"]\nmodel = \"keep\"\n\n"
	for _, tc := range []struct {
		name, input string
	}{
		{"models", wired + "[models]\ndefault = [\n"},
		{"features", "[models]\ndefault = \"magpie/keep\"\n\n" + wired + "[features]\ncampaigns = [\n"},
	} {
		for _, action := range []string{"set", "clear", "sync"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("GROK_HOME", home)
				path := filepath.Join(home, "config.toml")
				if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
					t.Fatal(err)
				}
				a := grok(home)
				var err error
				switch action {
				case "set":
					err = a.Field("model").Set("grok-native")
				case "clear":
					err = a.Field("model").Set("")
				case "sync":
					err = a.Sync()
				}
				if err == nil || !strings.HasPrefix(err.Error(), path+": ") {
					t.Fatalf("expected the config path in the error, got %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.input {
					t.Fatalf("changed config after a read error:\n%s", got)
				}
			})
		}
	}
}

func TestGrokSyncRequiresOrdinaryModelTable(t *testing.T) {
	for _, body := range []string{
		"note = '''\n[model.\"magpie/fake\"]\n'''\n",
		"[[model.\"magpie/array\"]]\nmodel = \"array\"\n",
	} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GROK_HOME", home)
			path := filepath.Join(home, "config.toml")
			input := "[models]\ndefault = \"grok-native\"\n" + body
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := grok(home).Sync(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != input {
				t.Fatalf("synced a config without a magpie model table:\n%s", got)
			}
		})
	}
}

func TestGrokCheckReportsTOMLParseErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	input := "[models]\ndefault = \"magpie/fake\"\n\n[model.\"magpie/fake\"]\ninvalid = [\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	message := grok(home).Check()
	if !strings.HasPrefix(message, path+": line 5, column 11:") || !strings.Contains(message, "array is incomplete") {
		t.Fatalf("check did not report the parse location: %q", message)
	}
}

// The models magpie serves on the account Grok Build is signed in to are
// Grok's own a second time, beside its own: they fold into one row of the
// picker (Fate on Discord: grokbuild 在登录态下会加载重复的模型), the
// other providers' rows left as they are.
func TestGrokFoldsItsOwnAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	exe := provider.GrokExecutable
	provider.GrokExecutable = func() string { return filepath.Join(home, "grok", "bin", "grok") }
	t.Cleanup(func() { provider.GrokExecutable = exe })
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	os.MkdirAll(filepath.Join(home, "grok"), 0o755)
	os.WriteFile(filepath.Join(home, "grok", "auth.json"), []byte(`{"x":{"key":"k","email":"me@x.ai"}}`), 0o600)
	// the Grok plugin serves the account, taken from Grok Build's own
	spec := "@magpie-community/opencode-grok-auth"
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
	write("plugin-auth.json", map[string]map[string]any{"grok": {"type": "oauth", "refresh": filepath.Join(home, "grok")}})
	plugin.UseCached([]plugin.Provider{{ID: "grok", Spec: spec, Name: "Grok",
		Models: []plugin.Model{{ID: "grok-4.3", Name: "Grok 4.3"}}, Accounts: []plugin.Account{{Key: "grok", Type: "oauth"}}}})
	t.Cleanup(plugin.Settle)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	// models_cache.json as Grok Build writes it, a few hundred models
	var cache strings.Builder
	cache.WriteString(`{"models":{"grok-x":{"info":{"name":"Grok X"}},"grok-h":{"info":{"hidden":true}}`)
	for i := range 300 {
		fmt.Fprintf(&cache, `,"old-%d":{"info":{"hidden":true,"name":"Old %d","description":"a model Grok Build no longer shows"}}`, i, i)
	}
	cache.WriteString(`}}`)
	os.WriteFile(filepath.Join(home, "grok", "models_cache.json"), []byte(cache.String()), 0o600)
	a := grok(home)
	var same, others []string
	opts := a.Field("model").Options(a.Values())
	var cached []string
	for _, o := range opts {
		if o.Value == "grok-x" || o.Value == "grok-h" {
			cached = append(cached, o.Value+"="+o.Label)
		}
	}
	if !slices.Equal(cached, []string{"grok-x=Grok X"}) {
		t.Errorf("models_cache.json's shown models: %v, want grok-x alone", cached)
	}
	// a look again within a request reads nothing again and marks no copy
	// of magpie's rows (the window's state looked at every field each time)
	release := provider.Hold()
	vals := a.Values()
	first := a.Field("model").Options(vals)
	if again := a.Field("model").Options(vals); len(again) == 0 || &again[0] != &first[0] {
		t.Error("a look again made the list again, want the one kept")
	}
	if n := testing.AllocsPerRun(20, func() { a.Field("model").Options(vals) }); n > 40 {
		t.Errorf("a look again allocates %v times, want at most 40", n)
	}
	release()
	for _, o := range opts {
		if o.Same {
			same = append(same, o.Value)
		} else if strings.HasPrefix(o.Value, "magpie/") {
			others = append(others, o.Value)
		}
	}
	if len(same) == 0 {
		t.Fatalf("nothing folded; magpie's rows: %v", others)
	}
	for _, v := range same {
		if !strings.HasPrefix(v, "magpie/grok/") {
			t.Errorf("folded %s, not on the Grok account", v)
		}
	}
	for _, v := range others {
		if strings.HasPrefix(v, "magpie/grok/") {
			t.Errorf("%s on the Grok account not folded", v)
		}
	}
	if !slices.Contains(others, "magpie/deepseek/pro") {
		t.Errorf("DeepSeek's row: %v", others)
	}
}

// Each of magpie's models is asked on the API its provider serves it on
// natively (uk.jent on Discord): a model served on OpenAI's Responses API
// on "responses", one on Anthropic's Messages API on "messages", and the
// rest on Chat Completions, Grok Build's default. All three go to the
// gateway's one /v1, where Grok adds /responses, /messages or
// /chat/completions.
func TestGrokAsksEachModelNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro"}},
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-sol"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "grok", "config.toml")
	if err := grok(home).Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for id, want := range map[string]string{"deepseek/pro": "chat_completions", "resp/gpt-6-sol": "responses", "anth/claude-sonnet-5": "messages"} {
		m, err := edit.GetTOMLTable(path, `model."magpie/`+id+`"`)
		if err != nil {
			t.Fatal(err)
		}
		if m["api_backend"] != want || !strings.HasSuffix(m["base_url"], "/v1") {
			t.Errorf("%s: api_backend %q, base_url %q, want %q on /v1\n%s", id, m["api_backend"], m["base_url"], want, raw)
		}
	}
}

// grokSandbox is a sandbox HOME with a DeepSeek of a 1M and a 200K model,
// windows the user typed, and the path of Grok Build's config there.
func grokSandbox(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"pro", "flash"},
		Contexts: map[string]int{"pro": 1000000, "flash": 200000}}); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, "grok", "config.toml")
}

// grokWindows is the context_window of magpie's tables for pro and flash.
func grokWindows(t *testing.T, path string) (int, int) {
	t.Helper()
	got, err := grokWindowsIn(path)
	if err != nil {
		t.Fatal(err)
	}
	return got["deepseek/pro"], got["deepseek/flash"]
}

// grokPercents is the auto_compact_threshold_percent of magpie's tables for
// pro and flash, -1 for none.
func grokPercents(t *testing.T, path string) (int, int) {
	t.Helper()
	had, err := grokHadIn(path)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) int {
		if n, ok := had.percents[id]; ok {
			return n
		}
		return -1
	}
	return get("deepseek/pro"), get("deepseek/flash")
}

// A context_window the user set by hand in one of magpie's tables stays
// through a sync and a new pick (uk.jent on Discord: dozens of models told
// 1M, set to 300K one by one, and written over on the next sync), while
// the tables they didn't touch follow magpie's catalog.
func TestGrokKeepsAWindowSetByHand(t *testing.T) {
	home, path := grokSandbox(t)
	a := grok(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokWindows(t, path); pro != 1000000 || flash != 200000 {
		t.Fatalf("magpie's windows: pro %d, flash %d", pro, flash)
	}
	if err := edit.SetTOMLKey(path, `model."magpie/deepseek/pro"`, "context_window", 300000); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokWindows(t, path); pro != 300000 || flash != 200000 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("after a sync: pro %d (set by hand to 300000), flash %d\n%s", pro, flash, raw)
	}
	// the provider's window changed in magpie: the one set by hand stays,
	// the other follows
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"pro", "flash"},
		Contexts: map[string]int{"pro": 1000000, "flash": 128000}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokWindows(t, path); pro != 300000 || flash != 128000 {
		t.Fatalf("after a window changed: pro %d, flash %d", pro, flash)
	}
	// taken out and put back, magpie's tables start from its own again
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if pro, _ := grokWindows(t, path); pro != 1000000 {
		t.Fatalf("connected again: pro %d", pro)
	}
}

// A config an older magpie wrote, with no record of what it gave: a window
// other than the model's whole one is the user's and stays; the whole one,
// which that magpie wrote, stays the whole window, and Long conversations
// goes in its auto_compact_threshold_percent.
func TestGrokKeepsAWindowSetBeforeTheRecord(t *testing.T) {
	home, path := grokSandbox(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	old := "[models]\ndefault = \"magpie/deepseek/pro\"\n\n" +
		"[model.\"magpie/deepseek/pro\"]\nmodel = \"deepseek/pro\"\nbase_url = \"http://127.0.0.1:3425/v1\"\napi_key = \"magpie\"\ncontext_window = 300000\n\n" +
		"[model.\"magpie/deepseek/flash\"]\nmodel = \"deepseek/flash\"\nbase_url = \"http://127.0.0.1:3425/v1\"\napi_key = \"magpie\"\ncontext_window = 200000\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.CompactAt = 150000
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := grok(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokWindows(t, path); pro != 300000 || flash != 200000 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("pro %d (the user's 300000), flash %d (its whole 200000)\n%s", pro, flash, raw)
	}
	if pro, flash := grokPercents(t, path); pro != 50 || flash != 75 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("compact at 150K: pro %d%% of the user's 300K, flash %d%% of 200K\n%s", pro, flash, raw)
	}
}

// Grok Build compacts at a percent of context_window (85 by default,
// grok-build's DEFAULT_AUTO_COMPACT_THRESHOLD_PERCENT), and context_window
// is the model's window. magpie wrote Long conversations' threshold as the
// window, so a 1M model told 300K compacted at 255K and never had its 1M
// (@xiancrong on X). The window is the model's own and the threshold is a
// percent of it, and a config the old magpie wrote, its threshold on
// record, is brought up the same.
func TestGrokWindowIsTheModelsAndCompactsAtThePercent(t *testing.T) {
	home, path := grokSandbox(t)
	s := settings.Load()
	s.CompactAt = 300000
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	a := grok(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		if pro, flash := grokWindows(t, path); pro != 1000000 || flash != 200000 {
			raw, _ := os.ReadFile(path)
			t.Fatalf("%s: windows pro %d, flash %d, want the models' 1M and 200K\n%s", when, pro, flash, raw)
		}
		if pro, flash := grokPercents(t, path); pro != 30 || flash != -1 {
			raw, _ := os.ReadFile(path)
			t.Fatalf("%s: percents pro %d (want 30: 300K of 1M), flash %d (want none)\n%s", when, pro, flash, raw)
		}
	}
	check("new")
	// what the old magpie left: the threshold as pro's window, on record
	if err := edit.SetTOMLTables(path, []string{grokModelTable}, nil); err != nil {
		t.Fatal(err)
	}
	old := "[model.\"magpie/deepseek/pro\"]\nmodel = \"deepseek/pro\"\ncontext_window = 300000\n\n" +
		"[model.\"magpie/deepseek/flash\"]\nmodel = \"deepseek/flash\"\ncontext_window = 200000\n"
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(b, []byte("\n"+old)...), 0o644); err != nil {
		t.Fatal(err)
	}
	forget("grok:" + path + ":percents")
	stash(map[string]string{"grok:" + path + ":windows": `{"deepseek/pro":300000,"deepseek/flash":200000}`})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	check("the old magpie's config")
}

// An auto_compact_threshold_percent the user set in one of magpie's tables
// stays, and one in [session], the user's for every model, is not
// outranked by a per-model one of magpie's.
func TestGrokKeepsTheUsersCompactPercent(t *testing.T) {
	home, path := grokSandbox(t)
	s := settings.Load()
	s.CompactAt = 150000
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	a := grok(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLKey(path, `model."magpie/deepseek/pro"`, "auto_compact_threshold_percent", 60); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokPercents(t, path); pro != 60 || flash != 75 {
		t.Fatalf("pro set by hand to 60: pro %d, flash %d (want magpie's 75)", pro, flash)
	}
	if err := edit.SetTOMLKey(path, "session", "auto_compact_threshold_percent", 70); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if pro, flash := grokPercents(t, path); pro != 60 || flash != -1 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("[session] at 70: pro %d (want the user's 60), flash %d (want none of magpie's)\n%s", pro, flash, raw)
	}
}

// Grok Build is told where Long conversations has Codex and Claude Code
// compact, as a percent of the model's window: the size typed there on a
// longer window, none under Full window, and a threshold set on the model
// in its provider before either.
func TestGrokWindowFollowsLongConversations(t *testing.T) {
	home, path := grokSandbox(t)
	a := grok(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	set := func(f func(*settings.Settings)) {
		t.Helper()
		s := settings.Load()
		f(&s)
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	want := func(when string, pro, flash int) {
		t.Helper()
		if p, f := grokWindows(t, path); p != 1000000 || f != 200000 {
			t.Fatalf("%s: windows pro %d, flash %d, want the models' own", when, p, f)
		}
		if p, f := grokPercents(t, path); p != pro || f != flash {
			t.Fatalf("%s: percents pro %d, flash %d, want %d and %d", when, p, f, pro, flash)
		}
	}
	set(func(s *settings.Settings) {})
	want("magpie's default: Grok Build's own threshold", -1, -1)
	set(func(s *settings.Settings) { s.CompactAt = 300000 })
	want("compact at 300K", 30, -1)
	set(func(s *settings.Settings) { s.FullContext = true })
	want("full window", -1, -1)
	set(func(s *settings.Settings) {
		s.FullContext, s.ModelCompacts = false, map[string]int{"deepseek/pro": 400000}
	})
	want("pro's own threshold", 40, -1)
	// Grok Build's own Long conversations (molianyin4227) over every
	// agent's, a model's own still first; another agent's is not Grok's
	set(func(s *settings.Settings) {
		s.ModelCompacts, s.CompactAt = nil, 300000
		s.AgentCompacts = map[string]settings.AgentCompact{"grok": {At: 500000}, "codex": {At: 250000}}
	})
	want("grok's own 500K", 50, -1)
	set(func(s *settings.Settings) { s.AgentCompacts = map[string]settings.AgentCompact{"grok": {Full: true}} })
	want("grok's own full window", -1, -1)
	set(func(s *settings.Settings) { s.AgentCompacts = map[string]settings.AgentCompact{"codex": {Full: true}} })
	want("codex's full window reached grok", 30, -1)
}

// With no size typed for Grok Build or for every agent, Grok Build compacts
// where it does itself, 85% of the window: magpie's default for every
// agent, Codex's 272K, went in as a percent, so a 1M model compacted at 27%
// and a 512K one at 51% (@taktoshilite on X: "100 万的上下文用不了多少就开始
// 压缩了"). Windows from the vendor's list, none typed, as theirs were. A
// table magpie gave 27 on that default gets none, Grok Build's own; 272K
// picked for Grok Build itself, and a percent the user set, stay.
func TestGrokCompactsAtItsOwnPercentByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	path := filepath.Join(home, "grok", "config.toml")
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k", Models: []string{"big", "mid"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "mid", Context: 524288}}); err != nil {
		t.Fatal(err)
	}
	percents := func() (int, int) {
		t.Helper()
		had, err := grokHadIn(path)
		if err != nil {
			t.Fatal(err)
		}
		get := func(id string) int {
			if n, ok := had.percents[id]; ok {
				return n
			}
			return -1
		}
		return get("v/big"), get("v/mid")
	}
	sync := func() {
		t.Helper()
		if err := grok(home).Sync(); err != nil {
			t.Fatal(err)
		}
	}
	save := func(s settings.Settings) {
		t.Helper()
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	// what the magpie before this one wrote on the default: 27, on record
	stash(map[string]string{"grok:" + path + ":percents": `{"v/big":27,"v/mid":51}`})
	os.MkdirAll(filepath.Dir(path), 0o755)
	old := "[models]\ndefault = \"magpie/v/big\"\n\n" +
		"[model.\"magpie/v/big\"]\nmodel = \"v/big\"\nbase_url = \"http://127.0.0.1:3425/v1\"\napi_key = \"magpie\"\ncontext_window = 1000000\nauto_compact_threshold_percent = 27\n\n" +
		"[model.\"magpie/v/mid\"]\nmodel = \"v/mid\"\nbase_url = \"http://127.0.0.1:3425/v1\"\napi_key = \"magpie\"\ncontext_window = 524288\nauto_compact_threshold_percent = 51\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	sync()
	if big, mid := percents(); big != -1 || mid != -1 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("magpie's default: big %d%%, mid %d%%, want none (Grok Build's own 85)\n%s", big, mid, raw)
	}
	if w, _ := grokWindowsIn(path); w["v/big"] != 1000000 || w["v/mid"] != 524288 {
		t.Fatalf("windows %v", w)
	}
	// 272K picked for Grok Build itself is the user's
	save(settings.Settings{AgentCompacts: map[string]settings.AgentCompact{"grok": {}}})
	sync()
	if big, mid := percents(); big != 27 || mid != 51 {
		t.Fatalf("272K picked for Grok Build: big %d%%, mid %d%%, want 27 and 51", big, mid)
	}
	// so is a size typed for every agent
	save(settings.Settings{CompactAt: 900000})
	sync()
	if big, mid := percents(); big != 90 || mid != -1 {
		t.Fatalf("900K for every agent: big %d%%, mid %d%%, want 90 and none", big, mid)
	}
	// back to the default, a percent set by hand stays
	save(settings.Settings{})
	sync()
	if err := edit.SetTOMLKey(path, `model."magpie/v/big"`, "auto_compact_threshold_percent", 90); err != nil {
		t.Fatal(err)
	}
	sync()
	if big, mid := percents(); big != 90 || mid != -1 {
		t.Fatalf("big set by hand to 90: big %d%%, mid %d%%", big, mid)
	}
}
