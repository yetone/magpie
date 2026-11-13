package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// moer2831 on Discord: a model picked for a Claude Desktop tier ran in its
// Code tab on Claude Code's generic prompt. While a tier is picked,
// magpie's profile lists Desktop's models in inferenceModels, the pick's
// with behavesAs, and Claude Code is told the pick's window, since it
// would take the behavesAs model's. The list follows the picks and the
// catalog, is written only when it changes, goes when nothing is picked,
// and a list the user wrote is theirs. Picks that can't be read leave it
// as it is.
func TestClaudeDesktopModelsList(t *testing.T) {
	home, p := desktopSandbox(t)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "a", Context: 272000}, {ID: "b", Context: 128000}}); err != nil {
		t.Fatal(err)
	}
	cc := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, cc, `{"theme":"dark"}`)
	d := claudeDesktop(home)
	set := func(key, v string) {
		t.Helper()
		if err := d.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}
	id := func(ref string) string {
		for _, e := range provider.Catalog() {
			if e.ID == ref {
				return gateway.DesktopID(e)
			}
		}
		t.Fatalf("%s not in the catalog", ref)
		return ""
	}
	list := func() []map[string]any {
		t.Helper()
		raw, ok := edit.GetJSONRaw(p.prof, "inferenceModels")
		if !ok {
			return nil
		}
		var out []map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		return out
	}
	entry := func(ref string) map[string]any {
		t.Helper()
		for _, e := range list() {
			if e["name"] == id(ref) {
				return e
			}
		}
		t.Fatalf("%s not listed: %v", ref, list())
		return nil
	}

	set("provider", "magpie")
	if l := list(); l != nil {
		t.Fatalf("nothing picked: %v", l)
	}

	// a's pick for sonnet: the list, a as Sonnet 5.5, and its window told
	set("sonnet", "v/a")
	if l := list(); len(l) != 2 {
		t.Fatalf("a for sonnet: %v", l)
	}
	if e := entry("v/a"); e["behavesAs"] != "claude-sonnet-5-5" || e["anthropicFamilyTier"] != "sonnet" {
		t.Fatalf("a: %v", e)
	}
	if e := entry("v/b"); e["behavesAs"] != nil {
		t.Fatalf("b unpicked: %v", e)
	}
	// Desktop reads no /v1/models beside such a list: each entry is
	// named in it, not by its id (chengming0v0 on Discord)
	for _, e := range list() {
		if l, _ := e["labelOverride"].(string); l == "" || l == e["name"] {
			t.Fatalf("named by its id: %v", e)
		}
	}
	if v, _ := edit.GetJSON(cc, budgetPath(id("v/a"))); v != "272000" {
		t.Fatalf("a's window in Claude Code's settings: %q\n%s", v, readFile(cc))
	}

	// unchanged: not written again
	st, _ := os.Stat(p.prof)
	old := st.ModTime().Add(-2 * time.Second)
	os.Chtimes(p.prof, old, old)
	if err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p.prof); !st.ModTime().Equal(old) {
		t.Fatal("the profile was written with nothing changed")
	}

	// a model added reaches the list with the catalog
	if err := provider.Save(provider.Provider{ID: "w", Name: "W", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	if l := list(); len(l) != 3 {
		t.Fatalf("after w/c: %v", l)
	}

	// picks that can't be read: the list stays
	tiers := filepath.Join(settings.Dir(), "claude-desktop.tiers.json")
	good, err := os.ReadFile(tiers)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tiers, `{"sonnet":`)
	if err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	if l := list(); len(l) != 3 {
		t.Fatalf("unreadable picks: %v", l)
	}
	writeFile(t, tiers, string(good))

	// nothing picked: the list goes
	set("sonnet", "")
	if l := list(); l != nil {
		t.Fatalf("pick cleared: %v", l)
	}
	if _, ok := edit.GetJSONRaw(p.prof, "inferenceGatewayBaseUrl"); !ok {
		t.Fatal("the rest of the profile went with the list")
	}

	// the user's own list is theirs, through a pick and its clearing
	set("haiku", "v/b")
	if err := edit.SetJSON(p.prof, edit.KV{Path: "inferenceModels", Value: []any{map[string]any{"name": "mine"}}}); err != nil {
		t.Fatal(err)
	}
	mine, _ := edit.GetJSONRaw(p.prof, "inferenceModels")
	set("sonnet", "v/a")
	set("haiku", "")
	set("sonnet", "")
	if now, _ := edit.GetJSONRaw(p.prof, "inferenceModels"); now != mine {
		t.Fatalf("the user's list: %s, was %s", now, mine)
	}
}

// A model picked for two of Desktop's tiers gets an id of its own for the
// second (gateway.DesktopTierAliases): Desktop keys the list by name and
// reads one tier off an entry, so before, the second tier had no model and
// Claude Code ran it on the session's model. That id was then listed in
// inferenceModels, which is Desktop's model menu, and showed as a model of
// its own, "<name> · haiku" (yanghonghang on X). It goes in /v1/models
// only, so the profile turns Desktop's discovery on beside the list while
// a tier is shared, and off again after, unless the user set it. Claude
// Code holds that id to the pick's window too.
func TestClaudeDesktopModelsSharedTier(t *testing.T) {
	home, p := desktopSandbox(t)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "a", Context: 272000}, {ID: "b", Context: 128000}}); err != nil {
		t.Fatal(err)
	}
	cc := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, cc, `{"theme":"dark"}`)
	d := claudeDesktop(home)
	set := func(k, v string) {
		t.Helper()
		if err := d.Field(k).Set(v); err != nil {
			t.Fatal(err)
		}
	}
	discovery := func() string {
		raw, _ := edit.GetJSONRaw(p.prof, "modelDiscoveryEnabled")
		return raw
	}
	for _, kv := range [][2]string{{"provider", "magpie"}, {"sonnet", "v/a"}, {"haiku", "v/a"}} {
		set(kv[0], kv[1])
	}
	raw, ok := edit.GetJSONRaw(p.prof, "inferenceModels")
	if !ok {
		t.Fatal("no list")
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	again := gateway.DesktopTierAliases()["v/a"]
	if len(list) != 2 || len(again) != 1 {
		t.Fatalf("a for sonnet and haiku, in Desktop's menu: %v, aliases %v", list, again)
	}
	for _, e := range list {
		if e["name"] == again[0] || e["anthropicFamilyTier"] == "haiku" {
			t.Fatalf("haiku's own entry in Desktop's menu: %v", list)
		}
	}
	if discovery() != "true" {
		t.Fatalf("a shared tier, discovery: %q\n%s", discovery(), readFile(p.prof))
	}
	// haiku's id runs as itself, an id Claude Code doesn't know, so at
	// its own entry's window, else at the session's top
	v, _ := edit.GetJSON(cc, budgetPath(again[0]))
	if top, _ := edit.GetJSON(cc, "env.CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v != "272000" && (v != "" || top != "272000") {
		t.Fatalf("a's window under haiku's id in Claude Code's settings: %q\n%s", v, readFile(cc))
	}

	// the picks apart: discovery off again
	set("haiku", "v/b")
	if v := discovery(); v != "" {
		t.Fatalf("picks apart, discovery: %q", v)
	}

	// the user's own value stays, shared or not
	if err := edit.SetJSON(p.prof, edit.KV{Path: "modelDiscoveryEnabled", Value: false}); err != nil {
		t.Fatal(err)
	}
	set("haiku", "v/a")
	if v := discovery(); v != "false" {
		t.Fatalf("the user's false: %q", v)
	}
	set("haiku", "v/b")
	if err := edit.SetJSON(p.prof, edit.KV{Path: "modelDiscoveryEnabled", Value: true}); err != nil {
		t.Fatal(err)
	}
	set("haiku", "v/a")
	set("haiku", "v/b")
	if v := discovery(); v != "true" {
		t.Fatalf("the user's true: %q", v)
	}
}
