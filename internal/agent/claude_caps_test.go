package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// TestClaudeModelCapabilities: Claude Code on a gateway (Claude Desktop's
// Code tab) takes no model it doesn't know for one with xhigh, so
// "ultracode": true did nothing on magpie's models (#430). magpie tells it
// in CLAUDE_CODE_MODEL_CAPABILITIES what each of its models (and Desktop's
// mythos-magpie-<number> for it) can do, only the levels the model has, the
// model Claude Code is set to first; a value of the user's is left alone,
// and magpie's goes when Claude Code is taken off magpie.
func TestClaudeModelCapabilities(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k",
		Models: []string{"Deep", "mid", "plain", "claude-opus-4-6"}}); err != nil {
		t.Fatal(err)
	}
	for id, levels := range map[string][]string{
		"v/Deep": {"low", "medium", "high", "xhigh", "max"},
		"v/mid":  {"low", "medium", "high"},
		// Claude Code sends Opus 4.6 no xhigh, whatever the vendor says
		"v/claude-opus-4-6": {"low", "medium", "high", "xhigh", "max"},
	} {
		if err := provider.SetModelEfforts(id, levels); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "both", Members: []string{"v/Deep", "v/mid"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)
	a := claude(home)
	caps := func() string { v, _ := edit.GetJSON(path, "env."+claudeCapsEnv); return v }
	segs := func() map[string]string {
		out := map[string]string{}
		for _, s := range strings.Split(caps(), ";") {
			if k, v, ok := strings.Cut(s, "="); ok {
				out[k] = v
			}
		}
		return out
	}
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}

	set("model", "v/mid")
	got := segs()
	for id, want := range map[string]string{
		"v/deep":            "effort,xhigh_effort,max_effort",
		"v/mid":             "effort",
		"group/both":        "effort",
		"v/claude-opus-4-6": "effort,max_effort",
	} {
		if got[id] != want {
			t.Errorf("%s: %q, want %q (%s)", id, got[id], want, caps())
		}
	}
	if _, ok := got["v/plain"]; ok {
		t.Errorf("a model of no known levels was given some: %s", caps())
	}
	if !strings.HasPrefix(caps(), "v/mid=") {
		t.Errorf("the model Claude Code is on isn't first: %s", caps())
	}
	if len(caps()) > claudeCapsMax {
		t.Errorf("too long: %d", len(caps()))
	}
	set("model", "v/Deep")
	if !strings.HasPrefix(caps(), "v/deep=") {
		t.Errorf("switched to Deep: %s", caps())
	}

	// Claude Desktop on magpie: its ids for magpie's models, as Claude Code
	// gets them from its Code tab
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var deep provider.Entry
	for _, e := range provider.Catalog() {
		if e.ID == "v/Deep" {
			deep = e
		}
	}
	alias := gateway.DesktopID(deep)
	if !strings.HasPrefix(alias, "mythos-magpie-") || segs()[alias] != "effort,xhigh_effort,max_effort" {
		t.Errorf("Desktop's %s: %s", alias, caps())
	}
	if err := claudeDesktop(home).Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := segs()[alias]; ok {
		t.Errorf("Desktop off kept its ids: %s", caps())
	}

	// off magpie, and back to Claude Code as installed: magpie's value goes
	set("model", "claude-opus-4-8")
	if caps() != "" {
		t.Fatalf("Claude's own model kept magpie's capabilities: %q", caps())
	}
	set("model", "v/mid")
	set("model", "")
	if caps() != "" {
		t.Fatalf("reset kept magpie's capabilities: %q", caps())
	}

	// the user's own value is theirs, through a switch and back
	const mine = "my-model=effort"
	if err := edit.SetJSON(path, edit.KV{Path: "env." + claudeCapsEnv, Value: mine}); err != nil {
		t.Fatal(err)
	}
	set("model", "v/Deep")
	if caps() != mine {
		t.Fatalf("the user's value was overwritten: %q", caps())
	}
	if err := a.Sync(); err != nil || caps() != mine {
		t.Fatalf("Sync overwrote the user's value: %q %v", caps(), err)
	}
	set("model", "")
	if caps() != mine {
		t.Fatalf("the user's value was taken away: %q", caps())
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatal("theme lost")
	}
}

// TestClaudeCapabilitiesSync: a model added to the catalog later reaches
// Claude Code's capabilities with the rest of the catalog.
func TestClaudeCapabilitiesSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("v/a", []string{"high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	if err := a.Field("model").Set("v/a"); err != nil {
		t.Fatal(err)
	}
	caps := func() string { v, _ := edit.GetJSON(path, "env."+claudeCapsEnv); return v }
	if caps() != "v/a=effort,xhigh_effort" {
		t.Fatalf("v/a: %q", caps())
	}
	if err := provider.Save(provider.Provider{ID: "w", Name: "W", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("w/b", []string{"low", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if caps() != "v/a=effort,xhigh_effort;w/b=effort,max_effort" {
		t.Fatalf("after w/b was added: %q", caps())
	}
}
