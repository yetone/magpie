package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// TestClaudeAliasShown (StringKe on Discord): a settings.json saying
// "model": "sonnet", one of Claude Code's own aliases, showed as a bare
// "sonnet" with no logo and matched none of the models offered. The alias
// is now offered, first, as the model Claude Code takes it for — the
// catalog's newest Sonnet, or the model the user's env gives the tier —
// with Claude's logo, and the file is left as it is.
func TestClaudeAliasShown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"models":{
		"claude-sonnet-5-5":{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","release_date":"2026-09-28"},
		"claude-opus-5-5":{"id":"claude-opus-5-5","name":"Claude Opus 5.5","release_date":"2026-09-22"},
		"claude-sonnet-5":{"id":"claude-sonnet-5","name":"Claude Sonnet 5","release_date":"2026-06-29"},
		"claude-haiku-4-5-20251001":{"id":"claude-haiku-4-5-20251001","name":"Claude Haiku 4.5","release_date":"2025-10-15"},
		"claude-haiku-4-5":{"id":"claude-haiku-4-5","name":"Claude Haiku 4.5 (latest)","release_date":"2025-10-15"}
	}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	path := filepath.Join(home, ".claude", "settings.json")
	a := claude(home)

	for _, c := range []struct{ file, label, note string }{
		{`{"model":"sonnet"}`, "sonnet · claude-sonnet-5-5", "Claude Sonnet 5.5"},
		{`{"model":"opus[1m]"}`, "opus[1m] · claude-opus-5-5[1m]", "Claude Opus 5.5"},
		{`{"model":"best"}`, "best · claude-opus-5-5", "Claude Opus 5.5"},
		{`{"model":"haiku"}`, "haiku · claude-haiku-4-5", "Claude Haiku 4.5 (latest)"},
		{`{"model":"opusplan"}`, "opusplan · claude-opus-5-5 / claude-sonnet-5-5", "Claude Opus 5.5 / Claude Sonnet 5.5"},
		{`{"model":"default"}`, "", ""},
		// the user's own env moves the tier, and the alias with it
		{`{"model":"sonnet","env":{"ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-sonnet-5"}}`, "sonnet · claude-sonnet-5", "claude-sonnet-5"},
	} {
		writeFile(t, path, c.file)
		f := a.Field("model")
		v := f.Get()
		opts := f.Options(a.Values())
		if len(opts) == 0 || opts[0].Value != v {
			t.Fatalf("%s: the first option is not %q: %+v", c.file, v, opts)
		}
		o := opts[0]
		if o.Label != c.label || o.Note != c.note || o.Icon != "claude-color" || o.Group != "Claude Code" || o.Direct != "Anthropic" {
			t.Fatalf("%s: %+v, want label %q, note %q, Claude's logo, straight to Anthropic", c.file, o, c.label, c.note)
		}
		n := 0
		for _, x := range opts {
			if x.Value == v {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s: %q offered %d times", c.file, v, n)
		}
		// read, never written
		if b, _ := os.ReadFile(path); string(b) != c.file {
			t.Fatalf("reading wrote the file: %s", b)
		}
	}

	// an id is no alias: offered once, as ever
	writeFile(t, path, `{"model":"claude-sonnet-5-5"}`)
	opts := a.Field("model").Options(a.Values())
	if opts[0].Value != "claude-sonnet-5-5" || opts[0].Label != "" || opts[0].Direct != "Anthropic" {
		t.Fatalf("an id got an alias's option: %+v", opts[0])
	}
	// through another endpoint of the user's, it is that one Claude Code asks
	writeFile(t, path, `{"model":"sonnet","env":{"ANTHROPIC_BASE_URL":"https://relay.example.test"}}`)
	opts = a.Field("model").Options(a.Values())
	if opts[0].Direct != "relay.example.test" {
		t.Fatalf("a relay's model said to go to %q", opts[0].Direct)
	}
	// picking one in magpie writes it as before
	if err := a.Field("model").Set("claude-sonnet-5-5"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "model"); v != "claude-sonnet-5-5" {
		t.Fatalf("model = %q", v)
	}
}
