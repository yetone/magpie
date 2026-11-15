package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// LolingVerse on X: picking a model in agy's own /model, which writes it
// into settings.json, cost the user their own model and provider there.
// magpie's next Sync took a model of magpie's in settings.json for an
// older magpie's wiring and put back that wiring's stash: a stale one, or
// none, which took the user's own out. Now it is agy's pick, kept in
// magpie's file, and settings.json is given back as it was when magpie
// was switched on. And no thinking depth in agy: /effort is turned away
// for custom models, so each level a model takes is a custom model of
// its own, asking it at that level (thinkingLevel), and the row still
// shows the model, on magpie.
func TestAgyOwnPickKeepsSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://think.test/v1", Key: "key", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://think.test/v1", []catalog.Model{{ID: "m1", Efforts: []string{"low", "medium", "high", "xhigh"}}, {ID: "m2", Efforts: []string{"high"}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"model":"mine","modelProvider":"vertex","trustedWorkspaces":["/w"]}`), 0o644)
	// an older magpie's stash, left behind
	stash(map[string]string{"agy:" + path + ":model": "stale", "agy:" + path + ":modelProvider": "gemini"})
	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	over := filepath.Join(filepath.Dir(path), agyOverride)
	base := gjson.Get(agyRead(t, over), "model").String()
	if !strings.HasPrefix(base, "magpie/think/") {
		t.Fatalf("connected on %q", base)
	}

	// each level m1 takes that agy has, its own custom model; m2 has one
	// level, which is no choice
	cm := gjson.Get(agyRead(t, over), "customModelsConfig.customModels")
	for _, l := range []string{"low", "medium", "high"} {
		e := cm.Get(gjson.Escape("magpie/think/m1:" + l))
		if e.Get("modelName").String() != "think/m1" || e.Get("thinkingLevel").String() != l {
			t.Fatalf("level %s: %s", l, e.Raw)
		}
	}
	if cm.Get(gjson.Escape("magpie/think/m1:xhigh")).Exists() || cm.Get(gjson.Escape("magpie/think/m2:high")).Exists() || cm.Get(gjson.Escape("magpie/think/m1")).Get("thinkingLevel").Exists() {
		t.Fatalf("custom models: %s", cm.Raw)
	}

	// agy's /model writes its pick into settings.json
	b, _ := os.ReadFile(path)
	var s map[string]any
	json.Unmarshal(b, &s)
	s["model"] = "magpie/think/m1:high"
	b, _ = json.Marshal(s)
	os.WriteFile(path, b, 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	got := agyRead(t, path)
	if gjson.Get(got, "model").String() != "mine" || gjson.Get(got, "modelProvider").String() != "vertex" || !gjson.Get(got, "trustedWorkspaces").Exists() {
		t.Fatalf("settings.json not the user's:\n%s", got)
	}
	if m := gjson.Get(agyRead(t, over), "model").String(); m != "magpie/think/m1:high" {
		t.Fatalf("agy's pick not kept: %q", m)
	}
	if !strings.Contains(a.Launch(), "--model 'magpie/think/m1:high'") {
		t.Fatalf("launch: %q", a.Launch())
	}
	if got := a.Field("model").Get(); got != "magpie/think/m1" {
		t.Fatalf("row's model: %q", got)
	}
	if !a.Wired() || a.Drift() != nil || a.Check() != "" {
		t.Fatalf("not on magpie: wired %v, drift %+v, check %q", a.Wired(), a.Drift(), a.Check())
	}
	// the user's own model picked in agy stays theirs, magpie stays on
	s["model"] = "gemini-3.1-pro"
	b, _ = json.Marshal(s)
	os.WriteFile(path, b, 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if gjson.Get(agyRead(t, path), "model").String() != "gemini-3.1-pro" || !a.Wired() {
		t.Fatalf("own pick: %s wired %v", agyRead(t, path), a.Wired())
	}

	// switched off: magpie's file gone, settings.json the user's
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(over); err == nil {
		t.Fatal("magpie's file left")
	}
	if got := agyRead(t, path); gjson.Get(got, "modelProvider").String() != "vertex" {
		t.Fatalf("after switching off:\n%s", got)
	}
}
