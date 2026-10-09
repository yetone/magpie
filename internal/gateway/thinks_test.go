package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// thinksCatalog is models.dev as it has mimo-v2.6-flash: reasoning with a
// thinking switch alone (Xiaomi's own) or no options at all (OpenCode
// Go's), never levels; a model that doesn't think, and one with levels.
const thinksCatalog = `{
  "xiaomi": {"models": {
    "mimo-v2.6-flash": {"id":"mimo-v2.6-flash","reasoning":true,"reasoning_options":[{"type":"toggle"}]}}},
  "opencode-go": {"models": {
    "mimo-v2.6-flash": {"id":"mimo-v2.6-flash","reasoning":true,"reasoning_options":[]},
    "plain": {"id":"plain","reasoning":false},
    "levelled": {"id":"levelled","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}
}`

// A model that thinks but takes no levels is listed on /v1/models with
// reasoning true and no levels, told apart from one that doesn't think
// (#402) — Xiaomi's own, a relay's whose list says nothing of reasoning,
// and a group of them. A group reasons when a member does, whatever it
// offers as levels, and the gateway sends a member known not to think no
// reasoning ask (#950); a group none of whose members think says false. A
// model with levels still lists them.
func TestModelsReasoningWithoutLevels(t *testing.T) {
	fresh(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(thinksCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	xiaomi, err := provider.FromPreset("xiaomi")
	if err != nil {
		t.Fatal(err)
	}
	xiaomi.Key, xiaomi.Models = "k", []string{"mimo-v2.6-flash"}
	if err := provider.Save(xiaomi); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k",
		Models: []string{"mimo-v2.6-flash", "plain", "levelled", "mystery"}}); err != nil {
		t.Fatal(err)
	}
	// the relay's own list, as Xiaomi's API answers: ids alone
	if err := catalog.SaveLive("relay", "https://relay.test/v1", []catalog.Model{
		{ID: "mimo-v2.6-flash"}, {ID: "plain"}, {ID: "levelled"}, {ID: "mystery"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{Name: "Auto MiMo", Members: []string{"xiaomi/mimo-v2.6-flash", "relay/mimo-v2.6-flash"}, Routing: provider.Ordered},
		{Name: "Mixed", Members: []string{"relay/mimo-v2.6-flash", "relay/plain"}, Routing: provider.Ordered},
		{Name: "Quiet", Members: []string{"relay/plain", "relay/mystery"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []struct {
			ID        string            `json:"id"`
			Reasoning bool              `json:"reasoning"`
			Levels    []json.RawMessage `json:"supported_reasoning_levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	type want struct {
		reasoning bool
		levels    int
	}
	got := map[string]want{}
	for _, m := range list.Data {
		got[m.ID] = want{m.Reasoning, len(m.Levels)}
	}
	for id, w := range map[string]want{
		"xiaomi/mimo-v2.6-flash": {true, 0},
		"relay/mimo-v2.6-flash":  {true, 0},
		"group/auto-mimo":        {true, 0},
		// a member thinks, so the group does: the quiet member is sent no
		// reasoning ask rather than taking reasoning from the group (#950)
		"group/mixed":    {true, 0},
		"relay/plain":    {false, 0},
		"relay/mystery":  {false, 0},
		"relay/levelled": {true, 2},
		// none of its members thinks: still false
		"group/quiet": {false, 0},
	} {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("%s: reasoning %v with %d levels (listed %v), want %v with %d", id, g.reasoning, g.levels, ok, w.reasoning, w.levels)
		}
	}
}
