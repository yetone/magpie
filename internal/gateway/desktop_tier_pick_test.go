package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
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
