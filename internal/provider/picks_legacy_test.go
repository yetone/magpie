package provider

// A provider saved before PickedFrom was kept: its picks are taken as they
// are. Read as "everything released after the newest pick", a single pick
// of an older model served every newer one, and the next Save wrote them
// all down as the user's picks — a relay's 3 models all picked after one
// was clicked, and "no matter how I save, it saves every model".

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestLegacyPicksServeWhatWasPicked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	const chat = "http://127.0.0.1:1/v1"
	var ms []catalog.Model
	for i := range 10 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m%d", i), Released: fmt.Sprintf("2026-%02d-01", i+1)})
	}
	if err := catalog.SaveLive("relay", chat, ms); err != nil {
		t.Fatal(err)
	}
	// the state of a provider saved before PickedFrom: one old pick, no
	// PickedFrom, the list fetched since
	dir := filepath.Join(home, ".config", "magpie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"providers": []map[string]any{{
		"id": "relay", "name": "Relay", "key": "k", "chat": chat,
		"models": []string{"m0"},
	}}})
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	served := func(p *Provider) []string {
		var ids []string
		for _, m := range p.Exposed() {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if got := served(p); !slices.Equal(got, []string{"m0"}) {
		t.Fatalf("picked m0 alone, %d models served: %v", len(got), got)
	}
	if got := p.Picks(); !slices.Equal(got, []string{"m0"}) {
		t.Fatalf("picked m0 alone, picks = %v", got)
	}
	// Save as an editor does, from the picks it was shown: m0 is still
	// all that is picked, and all that is served
	p.Models = p.Picks()
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	q, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.Models, []string{"m0"}) || !slices.Equal(served(q), []string{"m0"}) {
		t.Fatalf("after a Save: picks %v, served %v", q.Models, served(q))
	}
	// and the Save kept what was listed with them: from now on the
	// provider is read as any saved since PickedFrom was kept
	if len(q.PickedFrom) != len(ms) {
		t.Fatalf("listed with the picks after a Save: %v", q.PickedFrom)
	}
}

// A provider of keys whose every model was picked before PickedFrom was
// kept is served those picks alone until its next Save, which keeps the
// list with them; a model the vendor lists after that Save is added.
func TestLegacyPicksOfEveryModelGetPickedFromAtSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	const chat = "http://127.0.0.1:1/v1"
	var ms []catalog.Model
	var all []string
	for i := range 3 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m%d", i), Released: fmt.Sprintf("2026-%02d-01", i+1)})
		all = append(all, fmt.Sprintf("m%d", i))
	}
	if err := catalog.SaveLive("relay", chat, ms); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "magpie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"providers": []map[string]any{{
		"id": "relay", "name": "Relay", "key": "k", "chat": chat, "models": all,
	}}})
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	// Save with the same picks, as an editor left as it was does
	p.Models = p.Picks()
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay", chat, append([]catalog.Model{{ID: "m3", Released: "2026-04-01"}}, ms...)); err != nil {
		t.Fatal(err)
	}
	q, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Picks(); !slices.Equal(got, append([]string{"m3"}, all...)) {
		t.Fatalf("m3 listed after the Save: picks %v (listed with them %v)", got, q.PickedFrom)
	}
}
