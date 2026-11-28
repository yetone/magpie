package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// An agent's model pickers offer each provider's models the newest
// release first, by the day models.dev gives them (elis_pina on X: an
// older model added later was listed above the newer ones), while the
// models magpie writes into the agent's own files keep the catalog's
// order. An order the user dragged stays theirs.
func TestPickersPutTheNewestFirst(t *testing.T) {
	syncHome(t)
	body, err := os.ReadFile(filepath.Join("..", "catalog", "testdata", "models.dev-2026-11-28-released.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	// listed in the order they were added: the older one last
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-4-1", "my-own", "claude-fable-5-2"}}); err != nil {
		t.Fatal(err)
	}
	others := func(ids []string) (out []string) {
		for _, id := range ids {
			if strings.HasPrefix(id, "other/") {
				out = append(out, id)
			}
		}
		return out
	}
	picked := func(agent string) (out []string) {
		for _, o := range viaMagpie(agent, "") {
			out = append(out, o.Ref)
		}
		return others(out)
	}
	written := func(agent string) (out []string) {
		for _, m := range magpieModels(agent) {
			out = append(out, m.ID)
		}
		return others(out)
	}
	newest := []string{"other/claude-fable-5-2", "other/claude-sonnet-5-5", "other/claude-opus-5-5", "other/claude-opus-4-1", "other/my-own"}
	if got := picked("opencode"); !slices.Equal(got, newest) {
		t.Errorf("picker\n got %v\nwant %v", got, newest)
	}
	listed := []string{"other/claude-opus-5-5", "other/claude-sonnet-5-5", "other/claude-opus-4-1", "other/my-own", "other/claude-fable-5-2"}
	if got := written("opencode"); !slices.Equal(got, listed) {
		t.Errorf("the agent's own models\n got %v\nwant %v, the catalog's order", got, listed)
	}
	if err := provider.SetModelOrder("opencode", listed); err != nil {
		t.Fatal(err)
	}
	if got := picked("opencode"); !slices.Equal(got, listed) {
		t.Errorf("the user's order\n got %v\nwant %v", got, listed)
	}
	if got := picked("crush"); !slices.Equal(got, newest) {
		t.Errorf("another agent's picker\n got %v\nwant %v", got, newest)
	}
}
