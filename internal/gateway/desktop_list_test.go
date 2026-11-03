package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// moer2831 on Discord: a model picked for one of Claude Desktop's tiers ran
// in its Code tab on Claude Code's generic prompt, not the one of the
// Claude model of that tier ("直接蠢了一个度"). Desktop 4.29 hands Claude Code
// behavesAs only from a profile's inferenceModels, so while a tier is
// picked magpie lists Desktop's models there: every row /v1/models gives
// Desktop, by its id and in its order, a pick tagged with its tier and
// run as that tier's newest Claude model, a Claude model as itself.
// Nothing picked, no list: Desktop finds the models on its own, as before.
// Picks that can't be read are unknown, and leave what is written alone.
func TestDesktopInferenceModels(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "https://fake.example",
		Models: []string{"m1", "claude-sonnet-5", "m2"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetContext(*p, "m2", 1_000_000); err != nil {
		t.Fatal(err)
	}
	served := func() []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", "Mozilla/5.0 Claude/4.4212.0 Chrome/140")
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var list struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list.Data
	}
	byName := func(list []map[string]any, id string) map[string]any {
		t.Helper()
		for _, e := range list {
			if e["name"] == id {
				return e
			}
		}
		t.Fatalf("%s not listed: %v", id, list)
		return nil
	}
	ref := func(id string) string {
		for _, e := range provider.Catalog() {
			if e.ID == id {
				return DesktopID(e)
			}
		}
		t.Fatalf("%s not in the catalog", id)
		return ""
	}

	// nothing picked: no list
	if list, ok := DesktopInferenceModels(); !ok || list != nil {
		t.Fatalf("nothing picked: %v %v", list, ok)
	}

	// m1 for sonnet, claude-sonnet-5 for haiku
	if err := SetDesktopTier("sonnet", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := SetDesktopTier("haiku", "fake/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	list, ok := DesktopInferenceModels()
	if !ok || list == nil {
		t.Fatalf("picked: %v %v", list, ok)
	}
	rows := served()
	if len(list) != len(rows) {
		t.Fatalf("%d entries for %d rows of /v1/models", len(list), len(rows))
	}
	for i, r := range rows {
		if want := strings.TrimSuffix(r["id"].(string), "[1m]"); list[i]["name"] != want {
			t.Fatalf("entry %d is %v, /v1/models has %s there", i, list[i]["name"], want)
		}
	}
	m1 := byName(list, ref("fake/m1"))
	if m1["behavesAs"] != "claude-sonnet-5-5" || m1["anthropicFamilyTier"] != "sonnet" || m1["isFamilyDefault"] != true {
		t.Fatalf("m1 for sonnet: %v", m1)
	}
	claude := byName(list, ref("fake/claude-sonnet-5"))
	if _, has := claude["behavesAs"]; has || claude["anthropicFamilyTier"] != "haiku" {
		t.Fatalf("a Claude model for haiku runs as itself: %v", claude)
	}
	m2 := byName(list, ref("fake/m2"))
	if _, has := m2["behavesAs"]; has || m2["anthropicFamilyTier"] != nil || m2["supports1m"] != true || m2["prefer1m"] != nil {
		t.Fatalf("m2, 1M, unpicked: %v", m2)
	}
	if as, ok := DesktopBehavesAs(); !ok || len(as) != 1 || as[ref("fake/m1")] != "claude-sonnet-5-5" {
		t.Fatalf("behaves as: %v %v", as, ok)
	}

	// listed by its 1M id: named by it without the suffix, which Desktop
	// adds back, and labelled as /v1/models labels it
	s := settings.Load()
	s.DesktopLongest = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	list, _ = DesktopInferenceModels()
	m2 = byName(list, ref("fake/m2"))
	var shown string
	for _, r := range served() {
		if r["id"] == ref("fake/m2")+"[1m]" {
			shown, _ = r["display_name"].(string)
		}
	}
	if shown == "" || m2["supports1m"] != true || m2["prefer1m"] != true || m2["labelOverride"] != shown {
		t.Fatalf("m2 by its [1m] id (%q): %v", shown, m2)
	}

	// a pick of a model no longer listed is no pick
	if err := SetDesktopTier("sonnet", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "https://fake.example",
		Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if list, ok := DesktopInferenceModels(); !ok || list != nil {
		t.Fatalf("haiku's model gone: %v %v", list, ok)
	}

	// picks that don't parse are unknown
	if err := os.WriteFile(desktopTiersPath(), []byte(`{"sonnet":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, ok := DesktopInferenceModels(); ok || list != nil {
		t.Fatalf("unreadable picks: %v %v", list, ok)
	}
	if _, ok := DesktopBehavesAs(); ok {
		t.Fatal("unreadable picks read as known")
	}
}
