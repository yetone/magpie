package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// claudeSettings is a Claude Code home with settings.json as given.
func claudeSettings(t *testing.T, settings string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	path = filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

// Opus 5.5 and later ignore the effortLevel at the top of the user's
// settings.json and read modelSettings.<model>.effortLevel (#351): magpie
// writes the level there, beside the user's other models and keys, and a
// reset takes out only the level. Opus 5 and before, and another vendor's
// model, still read the top one.
func TestClaudeEffortModelSettings(t *testing.T) {
	home, path := claudeSettings(t, `{"model": "claude-opus-5-5[1m]", "effortLevel": "high",
  "modelSettings": {"claude-sonnet-4-6": {"effortLevel": "max"}, "claude-opus-5-5": {"maxEffortLevel": "xhigh"}}}`)
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	a := claude(home)
	e := a.Field("effort")
	// the top one isn't Opus 5.5's: it runs at its own default
	if e.Get() != "" {
		t.Fatalf("opus 5.5 with a top-level level only: %q", e.Get())
	}
	if err := e.Set("low"); err != nil {
		t.Fatal(err)
	}
	if e.Get() != "low" || get("modelSettings.claude-opus-5-5.effortLevel") != "low" || get("modelSettings.claude-opus-5-5.maxEffortLevel") != "xhigh" ||
		get("modelSettings.claude-sonnet-4-6.effortLevel") != "max" || get("effortLevel") != "high" {
		t.Fatalf("low:\n%s", readFile(path))
	}
	if err := e.Set(""); err != nil {
		t.Fatal(err)
	}
	if e.Get() != "" || get("modelSettings.claude-opus-5-5.effortLevel") != "" || get("modelSettings.claude-opus-5-5.maxEffortLevel") != "xhigh" ||
		get("modelSettings.claude-sonnet-4-6.effortLevel") != "max" {
		t.Fatalf("reset:\n%s", readFile(path))
	}

	// Opus 5 reads both: the top one for a Claude Code before 2.1.251, its
	// own, which wins over the top, for the rest
	home, path = claudeSettings(t, `{"model": "claude-opus-5", "modelSettings": {"claude-opus-5": {"effortLevel": "low"}}}`)
	e = claude(home).Field("effort")
	if e.Get() != "low" {
		t.Fatalf("opus 5's own: %q", e.Get())
	}
	if err := e.Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if get("effortLevel") != "xhigh" || get("modelSettings.claude-opus-5.effortLevel") != "xhigh" {
		t.Fatalf("opus 5:\n%s", readFile(path))
	}
	if err := e.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(path, "modelSettings"); ok || get("effortLevel") != "" {
		t.Fatalf("opus 5 reset:\n%s", readFile(path))
	}

	// another vendor's model through magpie: the top one only
	home, path = claudeSettings(t, `{}`)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	a = claude(home)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("medium"); err != nil {
		t.Fatal(err)
	}
	if get("effortLevel") != "medium" || strings.Contains(readFile(path), "modelSettings") || a.Field("effort").Get() != "medium" {
		t.Fatalf("routed:\n%s", readFile(path))
	}
}

// The levels offered follow the model, as Claude Code 2.1.285 sends them
// (#354): Opus 4.5 up to high, Opus and Sonnet 4.6 without xhigh, Haiku 4.5
// none, the rest all five. A level the model doesn't take is refused, and
// one set before shows as the level it runs at.
func TestClaudeEffortPerModel(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-4-5-20251101":    "low medium high",
		"claude-opus-4-6[1m]":         "low medium high max",
		"anthropic/claude-sonnet-4-6": "low medium high max",
		"claude-haiku-4-5":            "",
		"claude-sonnet-4-5":           "",
		"claude-opus-4-7":             "low medium high xhigh max",
		"claude-opus-5-5":             "low medium high xhigh max",
		"claude-sonnet-5-5":           "low medium high xhigh max",
		"claude-fable-5-1":            "low medium high xhigh max",
		"claude-opus-5-6":             "low medium high xhigh max",
		"deepseek/pro":                "low medium high xhigh max",
		"":                            "low medium high xhigh max",
	} {
		var got []string
		for _, o := range claude(t.TempDir()).Field("effort").Options(map[string]string{"model": model}) {
			got = append(got, o.Value)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s: %v, want %s", model, got, want)
		}
	}

	home, path := claudeSettings(t, `{"model": "claude-opus-4-5", "env": {"CLAUDE_CODE_EFFORT_LEVEL": "max"}}`)
	e := claude(home).Field("effort")
	if e.Get() != "high" {
		t.Fatalf("opus 4.5 at max runs at high, shown %q", e.Get())
	}
	if err := e.Set("xhigh"); err == nil || !strings.Contains(err.Error(), "low, medium, high") {
		t.Fatalf("xhigh on opus 4.5: %v", err)
	}
	if v, _ := edit.GetJSON(path, "env.CLAUDE_CODE_EFFORT_LEVEL"); v != "max" {
		t.Fatalf("a refused level wrote:\n%s", readFile(path))
	}
	home, _ = claudeSettings(t, `{"model": "claude-haiku-4-5", "effortLevel": "high"}`)
	e = claude(home).Field("effort")
	if e.Get() != "" || e.Set("low") == nil || e.Set("") != nil {
		t.Fatalf("haiku: %q", e.Get())
	}
}

// ultracode is settings.json's "ultracode": true (#352), there for a model
// that takes xhigh; magpie claude ultra is refused, not written as the model.
func TestClaudeUltracode(t *testing.T) {
	home, path := claudeSettings(t, `{"model": "claude-opus-5-5"}`)
	a := claude(home)
	u := a.Field("ultracode")
	if len(u.Options(a.Values())) != 1 {
		t.Fatalf("opus 5.5 options %v", u.Options(a.Values()))
	}
	if err := u.Set("on"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "ultracode"); v != "true" || u.Get() != "on" {
		t.Fatalf("on:\n%s", readFile(path))
	}
	for _, v := range []string{"ultra", "ultracode", "banana"} {
		if err := a.Field("model").Set(v); err == nil {
			t.Fatalf("model %q taken", v)
		}
	}
	if a.Field("model").Get() != "claude-opus-5-5" {
		t.Fatalf("model clobbered:\n%s", readFile(path))
	}
	for _, v := range []string{"opus", "sonnet[1m]", "claude-opus-5"} {
		if err := a.Field("model").Set(v); err != nil {
			t.Fatalf("model %q: %v", v, err)
		}
	}
	if err := a.Field("model").Set("claude-opus-4-6"); err != nil {
		t.Fatal(err)
	}
	if len(u.Options(a.Values())) != 0 || u.Set("on") == nil {
		t.Fatal("opus 4.6 has no ultracode")
	}
	// the max Opus 4.6 doesn't take goes to the model field from the CLI
	if err := a.Field("model").Set("xhigh"); err == nil || !strings.Contains(err.Error(), "low, medium, high, max") {
		t.Fatalf("xhigh as a model: %v", err)
	}
	if err := u.Set("off"); err != nil || u.Get() != "" || strings.Contains(readFile(path), "ultracode") {
		t.Fatalf("off: %v\n%s", err, readFile(path))
	}
}

// A level written to settings.json reaches an open Claude Code session only
// once it's started again, as does the env's level taken away; a new one in
// the env reaches it on its next request (#353).
func TestClaudeEffortNotice(t *testing.T) {
	old := claudeRunning
	running := true
	claudeRunning = func() bool { return running }
	t.Cleanup(func() { claudeRunning = old })
	home, _ := claudeSettings(t, `{"model": "claude-opus-5-5"}`)
	a := claude(home)
	if a.Notice() != "" {
		t.Fatalf("before anything: %q", a.Notice())
	}
	a.Field("effort").Set("high")
	if !strings.Contains(a.Notice(), "restart") {
		t.Fatalf("high: %q", a.Notice())
	}
	a.Field("effort").Set("max")
	if a.Notice() != "" {
		t.Fatalf("max: %q", a.Notice())
	}
	a.Field("ultracode").Set("on")
	if !strings.Contains(a.Notice(), "ultracode") {
		t.Fatalf("ultracode: %q", a.Notice())
	}
	running = false
	if a.Notice() != "" {
		t.Fatalf("nothing open: %q", a.Notice())
	}
}
