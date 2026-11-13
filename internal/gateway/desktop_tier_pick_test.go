package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// desktopPickSetup is a provider serving m1 and claude-sonnet-5-5, both
// with reasoning levels (so Desktop lists them as mythos-magpie-<n> and
// magpie-<n>.anthropic.claude-sonnet-5-5), on a fake Anthropic upstream
// that notes the model of the last request.
func desktopPickSetup(t *testing.T) *desktopSubagentUpstream {
	t.Helper()
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	u := &desktopSubagentUpstream{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := modelOf(body)
		u.mu.Lock()
		u.model, u.path = model, r.URL.Path
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"reply","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: []string{"m1", "claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"fake/m1", "fake/claude-sonnet-5-5"} {
		if err := provider.SetModelEfforts(ref, []string{"low", "medium", "high"}); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func shownEntry(t *testing.T, match func(provider.Entry) bool) provider.Entry {
	t.Helper()
	shown, _ := provider.CatalogFor("claude-desktop")
	for _, e := range shown {
		if match(e) {
			return e
		}
	}
	t.Fatalf("not shown to Claude Desktop: %v", shown)
	return provider.Entry{}
}

// tierEntries are the entries of an inferenceModels list by the tier they
// are tagged with; a tier tagged twice fails.
func tierEntries(t *testing.T, list []map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, e := range list {
		if tier, _ := e["anthropicFamilyTier"].(string); tier != "" {
			if out[tier] != nil {
				t.Fatalf("%s tagged twice: %v", tier, list)
			}
			out[tier] = e
		}
	}
	return out
}

// desktopListed is Claude Desktop's /v1/models, asked with its key, as
// raw rows.
func desktopListed(t *testing.T) []map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
	req.Header.Set("x-api-key", TokenFor("claude-desktop"))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(rec.Body.String(), err)
	}
	return out.Data
}

// rowTiers are the /v1/models rows by the tier they carry; a tier carried
// by two rows fails.
func rowTiers(t *testing.T, rows []map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range rows {
		if tier, _ := r["anthropic_family_tier"].(string); tier != "" {
			if out[tier] != nil {
				t.Fatalf("%s carried twice: %v", tier, rows)
			}
			out[tier] = r
		}
	}
	return out
}

// noTierEntry fails when the inferenceModels list holds an entry beyond
// the rows: Desktop's model menu is that list, so such an entry showed as
// a model of its own, "Gemini 4 Hydrogen · haiku" beside "Gemini 4
// Hydrogen" (yanghonghang on X).
func noTierEntry(t *testing.T, list []map[string]any) {
	t.Helper()
	shown, _ := provider.CatalogFor("claude-desktop")
	if len(list) != len(shown) {
		t.Fatalf("%d entries in Desktop's model menu for %d models: %v", len(list), len(shown), list)
	}
	for _, e := range list {
		if l, _ := e["labelOverride"].(string); strings.Contains(l, " · ") && regexp.MustCompile(` · (opus|sonnet|haiku)$`).MatchString(l) {
			t.Fatalf("a tier's own entry in Desktop's model menu: %v", e)
		}
	}
}

// A model picked for two of Claude Desktop's tiers (a user's
// copilot/claude-sonnet-5.5 for sonnet and haiku) was tagged with the
// first only: Desktop keys inferenceModels by name and reads one tier off
// an entry, so haiku had no model, Claude Code in the Code tab got an empty
// ANTHROPIC_DEFAULT_HAIKU_MODEL, and its small tasks ran on the session's
// model. The second tier was then listed once more in inferenceModels, and
// Desktop's model menu, which is that list, showed it as a model of its
// own (yanghonghang on X). The second tier now goes in /v1/models only,
// by the model's id form under a number of its own, tagged with the tier:
// Desktop's discovery pins the tier's variable to it, and the gateway reads
// it back to the model and serves it as the pick.
func TestDesktopTierSharedPick(t *testing.T) {
	t.Run("claude model", func(t *testing.T) {
		u := desktopPickSetup(t)
		sonnet := shownEntry(t, func(e provider.Entry) bool { return e.ID == "fake/claude-sonnet-5-5" })
		for _, tier := range []string{"sonnet", "haiku"} {
			if err := SetDesktopTier(tier, sonnet.ID); err != nil {
				t.Fatal(err)
			}
		}
		list, ok := DesktopInferenceModels()
		if !ok {
			t.Fatal("no list")
		}
		noTierEntry(t, list)
		if on, ok := DesktopDiscovers(); !on || !ok {
			t.Fatalf("a shared tier needs Desktop's discovery: %v %v", on, ok)
		}
		tiers := tierEntries(t, list)
		if e := tiers["sonnet"]; e == nil || e["name"] != DesktopID(sonnet) {
			t.Fatalf("sonnet: %v", list)
		}
		if tiers["haiku"] != nil {
			t.Fatalf("haiku in the list: %v", list)
		}
		rows := desktopListed(t)
		shown, _ := provider.CatalogFor("claude-desktop")
		if len(rows) != len(shown)+1 {
			t.Fatalf("%d rows for %d models and one more tier: %v", len(rows), len(shown), rows)
		}
		haiku := rowTiers(t, rows)["haiku"]
		if haiku == nil {
			t.Fatalf("haiku has no row, so Claude Code's ANTHROPIC_DEFAULT_HAIKU_MODEL is empty: %v", rows)
		}
		alias, _ := haiku["id"].(string)
		alias = strings.TrimSuffix(alias, "[1m]")
		if !regexp.MustCompile(`^magpie-\d{10}\.anthropic\.claude-sonnet-5-5$`).MatchString(alias) || alias == DesktopID(sonnet) ||
			haiku["is_family_default"] != true {
			t.Fatalf("haiku's row: %v (sonnet's name %s)", haiku, DesktopID(sonnet))
		}
		for _, id := range []string{alias, alias + "[1m]"} {
			if got := DesktopCatalogID(id); got != sonnet.ID {
				t.Fatalf("%s read back as %q", id, got)
			}
		}
		if a := DesktopTierAliases(); len(a) != 1 || len(a[sonnet.ID]) != 1 || a[sonnet.ID][0] != alias {
			t.Fatalf("tier aliases: %v", a)
		}

		// the session on m1; Claude Code's small task for the haiku tier,
		// by that row's id, goes to the pick, not the session's model
		s := New()
		m1 := shownEntry(t, func(e provider.Entry) bool { return e.ID == "fake/m1" })
		desktopSubagentAsk(t, s, Token+"-claude-desktop", DesktopID(m1), "", "/v1/messages", true)
		if m, _ := u.seen(); m != "m1" {
			t.Fatalf("the session's turn went to %q", m)
		}
		desktopSubagentAsk(t, s, Token+"-claude-desktop", alias, "", "/v1/messages", false)
		if m, _ := u.seen(); m != "claude-sonnet-5-5" {
			t.Fatalf("haiku's small task went to %q, want the haiku pick claude-sonnet-5-5", m)
		}

		// the picks apart again: no more rows, no discovery
		if err := SetDesktopTier("haiku", "fake/m1"); err != nil {
			t.Fatal(err)
		}
		if rows := desktopListed(t); len(rows) != len(shown) {
			t.Fatalf("picks apart: %v", rows)
		}
		if a := DesktopTierAliases(); a != nil {
			t.Fatalf("picks apart, tier aliases: %v", a)
		}
		if on, ok := DesktopDiscovers(); on || !ok {
			t.Fatalf("picks apart, discovery: %v %v", on, ok)
		}
	})

	t.Run("other model", func(t *testing.T) {
		desktopPickSetup(t)
		m1 := shownEntry(t, func(e provider.Entry) bool { return e.ID == "fake/m1" })
		for _, tier := range []string{"opus", "haiku"} {
			if err := SetDesktopTier(tier, m1.ID); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := DesktopInferenceModels()
		noTierEntry(t, list)
		opus := tierEntries(t, list)["opus"]
		if opus == nil || opus["name"] != DesktopID(m1) || opus["behavesAs"] != "claude-opus-5-5" {
			t.Fatalf("opus: %v", list)
		}
		haiku := rowTiers(t, desktopListed(t))["haiku"]
		if haiku == nil {
			t.Fatal("haiku has no row")
		}
		alias, _ := haiku["id"].(string)
		if !regexp.MustCompile(`^mythos-magpie-\d{10}$`).MatchString(alias) || alias == DesktopID(m1) {
			t.Fatalf("haiku's row: %v", haiku)
		}
		as, ok := DesktopBehavesAs()
		if !ok || as[DesktopID(m1)] != "claude-opus-5-5" || len(as) != 1 {
			t.Fatalf("behaves as: %v %v", as, ok)
		}
		if got := DesktopCatalogID(alias); got != m1.ID {
			t.Fatalf("%s read back as %q", alias, got)
		}
	})

	// one model for every tier: each tier after the first gets a row of its
	// own, by an id of its own, each served as the pick
	t.Run("every tier", func(t *testing.T) {
		u := desktopPickSetup(t)
		m1 := shownEntry(t, func(e provider.Entry) bool { return e.ID == "fake/m1" })
		sonnet := shownEntry(t, func(e provider.Entry) bool { return e.ID == "fake/claude-sonnet-5-5" })
		for _, tier := range DesktopTierNames {
			if err := SetDesktopTier(tier, m1.ID); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := DesktopInferenceModels()
		noTierEntry(t, list)
		if e := tierEntries(t, list)["opus"]; e == nil || e["name"] != DesktopID(m1) {
			t.Fatalf("opus, the first tier, on the entry: %v", list)
		}
		rows := desktopListed(t)
		shown, _ := provider.CatalogFor("claude-desktop")
		if len(rows) != len(shown)+len(DesktopTierNames)-1 {
			t.Fatalf("%d rows for %d models and %d more tiers: %v", len(rows), len(shown), len(DesktopTierNames)-1, rows)
		}
		ids := map[string]bool{}
		for _, r := range rows {
			id, _ := r["id"].(string)
			if ids[id] {
				t.Fatalf("%s listed twice: %v", id, rows)
			}
			ids[id] = true
		}
		tiers := rowTiers(t, rows)
		s := New()
		desktopSubagentAsk(t, s, Token+"-claude-desktop", DesktopID(sonnet), "", "/v1/messages", true)
		for _, tier := range DesktopTierNames {
			r := tiers[tier]
			if r == nil || r["is_family_default"] != true {
				t.Fatalf("%s: %v", tier, rows)
			}
			id, _ := r["id"].(string)
			if got := DesktopCatalogID(id); got != m1.ID {
				t.Fatalf("%s's %s read back as %q", tier, id, got)
			}
			desktopSubagentAsk(t, s, Token+"-claude-desktop", id, "", "/v1/messages", false)
			if m, _ := u.seen(); m != "m1" {
				t.Fatalf("%s's small task went to %q, want the pick m1", tier, m)
			}
		}
		if a := DesktopTierAliases(); len(a[m1.ID]) != len(DesktopTierNames)-1 {
			t.Fatalf("tier aliases: %v", a)
		}
	})

	// a Claude model with no reasoning levels is listed by its own id,
	// which has no number to change: the later tier's row is
	// anthropic/magpie-<n>
	t.Run("own id", func(t *testing.T) {
		desktopPickSetup(t)
		if err := provider.Save(provider.Provider{ID: "own", Name: "Own", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-haiku-4-5"}}); err != nil {
			t.Fatal(err)
		}
		h := shownEntry(t, func(e provider.Entry) bool { return e.ID == "own/claude-haiku-4-5" })
		for _, tier := range []string{"sonnet", "haiku"} {
			if err := SetDesktopTier(tier, h.ID); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := DesktopInferenceModels()
		noTierEntry(t, list)
		if e := tierEntries(t, list)["sonnet"]; e == nil || e["name"] != DesktopID(h) {
			t.Fatalf("sonnet: %v", list)
		}
		haiku := rowTiers(t, desktopListed(t))["haiku"]
		alias, _ := haiku["id"].(string)
		if !regexp.MustCompile(`^anthropic/magpie-\d{10}$`).MatchString(alias) {
			t.Fatalf("haiku's row: %v", haiku)
		}
		if got := DesktopCatalogID(alias); got != h.ID {
			t.Fatalf("%s read back as %q", alias, got)
		}
	})
}

// A routing group led by a Claude model, picked for a tier, is shown to
// Desktop by its mythos-magpie-<n> (claudeLooking: never as the model
// itself), which Claude Code doesn't know; its entry had no behavesAs, as
// the group's model is a Claude model, so the tier ran on Claude Code's
// generic prompt and defaults. It runs as the group's model.
func TestDesktopGroupPickRunsAsItsModel(t *testing.T) {
	desktopPickSetup(t)
	if err := provider.SaveGroup(provider.Group{ID: "duo", Name: "Duo", Members: []string{"fake/claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	g := shownEntry(t, func(e provider.Entry) bool { return e.Group == "duo" })
	if err := SetDesktopTier("haiku", g.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := DesktopInferenceModels()
	haiku := tierEntries(t, list)["haiku"]
	if haiku == nil || haiku["name"] != DesktopID(g) {
		t.Fatalf("the group for haiku: %v", list)
	}
	if strings.HasPrefix(DesktopID(g), "magpie-") || haiku["behavesAs"] != "claude-sonnet-5-5" {
		t.Fatalf("the group, shown as %s, runs as %v; want claude-sonnet-5-5: %v", DesktopID(g), haiku["behavesAs"], haiku)
	}
	if as, _ := DesktopBehavesAs(); as[DesktopID(g)] != "claude-sonnet-5-5" {
		t.Fatalf("behaves as: %v", as)
	}
}
