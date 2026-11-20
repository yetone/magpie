package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// kigiSandbox is a sandbox HOME with a DeepSeek of a 1M and a 200K model,
// Kigi's home moved to kigi under it, and the path of its config.toml.
func kigiSandbox(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	t.Setenv("KIGI_SHARE_DIR", filepath.Join(home, "kigi"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"pro", "flash"},
		Contexts: map[string]int{"pro": 1000000, "flash": 200000}}); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, "kigi", "config.toml")
}

// Kigi keeps Grok Build's config.toml in its own home ($KIGI_SHARE_DIR,
// else ~/.kigi): a model picked in magpie is one [model."magpie/…"] table
// per catalog model, as Kigi 0.1.22 reads them (a real kigi -p on such a
// table reached the gateway), its default, and none of it in Grok Build's
// home. Its own model puts magpie's tables away again.
func TestKigi(t *testing.T) {
	home, path := kigiSandbox(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# mine\n[model.my-own]\nmodel = \"k3\"\nbase_url = \"https://api.kimi.com/coding/v1\"\ncontext_window = 262144\n\n[models]\ndefault = \"k3\"\n"), 0o644)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	a, err := Find("kigi")
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != path || a.Name != "Kigi" || !slices.Contains(a.UA, "kigi") {
		t.Fatalf("agent: %s %s %v", a.Path, a.Name, a.UA)
	}
	f := a.Field("model")
	if f.Get() != "k3" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	m, err := edit.GetTOMLTable(path, `model."magpie/deepseek/pro"`)
	if err != nil {
		t.Fatal(err)
	}
	if m["model"] != "deepseek/pro" || m["api_key"] != "magpie" || !strings.HasSuffix(m["base_url"], "/v1") ||
		m["api_backend"] != "chat_completions" || m["context_window"] != "1000000" {
		t.Fatalf("table:\n%s", read())
	}
	// Kigi compacts at 85% of the window itself, as Grok Build does: magpie
	// gives no percent of its own under its default Long conversations
	if _, ok := m["auto_compact_threshold_percent"]; ok {
		t.Fatalf("a percent under magpie's default:\n%s", read())
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" || !strings.Contains(read(), "# mine") {
		t.Fatalf("picked: %q check %q\n%s", f.Get(), a.Check(), read())
	}
	if err := a.Connect(); err != nil || !a.Wired() {
		t.Fatalf("connect: %v, wired %v", err, a.Wired())
	}
	if _, err := os.Stat(filepath.Join(home, "grok", "config.toml")); err == nil {
		t.Fatal("Grok Build's config written")
	}
	// an effort Kigi (and Grok Build, whose parser it has) doesn't know
	// stops it at start-up: refused, the config left as it was; a known one
	// is written
	for _, id := range []string{"kigi", "grok"} {
		b, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		before := read()
		if err := b.Field("effort").Set("ultra"); err == nil || (id == "kigi" && read() != before) {
			t.Fatalf("%s took effort ultra: %v\n%s", id, err, read())
		}
	}
	if e := a.Field("effort"); e.Set("xhigh") != nil || e.Get() != "xhigh" {
		t.Fatalf("effort xhigh: %q", e.Get())
	}
	// the user's own table and models_cache.json's models are offered as
	// Kigi's own, beside magpie's
	os.WriteFile(filepath.Join(filepath.Dir(path), "models_cache.json"),
		[]byte(`{"fetched_at":"2026-11-20T00:00:00Z","models":{"kimi-for-coding":{"info":{"model":"kimi-for-coding","name":"Kimi for Coding","hidden":false}},"secret":{"info":{"hidden":true}}}}`), 0o644)
	var own, via []string
	for _, o := range f.Options(a.Values()) {
		if o.Direct != "" {
			own = append(own, o.Value)
		} else if strings.HasPrefix(o.Value, magpieID+"/") {
			via = append(via, o.Value)
		}
	}
	if !slices.Contains(own, "kimi-for-coding") || !slices.Contains(own, "my-own") || slices.Contains(own, "secret") || !slices.Contains(via, "magpie/deepseek/pro") {
		t.Fatalf("options: own %v via %v", own, via)
	}

	// its own model: magpie's tables go, the user's stays
	if err := f.Set("my-own"); err != nil {
		t.Fatal(err)
	}
	if raw := read(); strings.Contains(raw, "magpie") || !strings.Contains(raw, "[model.my-own]") || a.Wired() {
		t.Fatalf("own:\n%s", raw)
	}
}

// Kigi's requests are told by its User-Agent, and a change while kigi runs
// says to restart it, in words of its own.
func TestKigiNamedAsItself(t *testing.T) {
	kigiSandbox(t)
	a, err := Find("kigi")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "kigi" || !slices.Contains(a.Aliases, "kigi-cli") {
		t.Fatalf("%s %v", a.ID, a.Aliases)
	}
	if !slices.Contains(Notices(), noticeKigi.String()) || strings.Contains(noticeKigi.String(), "grok") {
		t.Fatalf("notice: %q", noticeKigi)
	}
}

// Kigi compacts at 85% of a model's window itself, as Grok Build does, so
// magpie's default Long conversations (Codex's 272K) is no percent of its
// own there: written as one, it had a 1M model compact at 27%. A size typed
// for every agent still goes in.
func TestKigiCompactsAtItsOwnPercentByDefault(t *testing.T) {
	_, path := kigiSandbox(t)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k", Models: []string{"big"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	percent := func() int {
		t.Helper()
		had, err := grokHadIn(path)
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := had.percents["v/big"]; ok {
			return n
		}
		return -1
	}
	a, err := Find("kigi")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/v/big"); err != nil {
		t.Fatal(err)
	}
	if p := percent(); p != -1 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("magpie's default: %d%%, want none (Kigi's own 85)\n%s", p, raw)
	}
	if err := settings.Save(settings.Settings{CompactAt: 900000}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := percent(); p != 90 {
		t.Fatalf("900K for every agent: %d%%, want 90", p)
	}
}
