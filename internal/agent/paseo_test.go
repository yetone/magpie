package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Paseo lists only settings.json's model and tiers, so magpie writes its
// models into ~/.paseo/config.json's agents.providers.claude.additionalModels
// (ProviderProfileModelSchema: id, label, description), leaves the user's own
// entries and keys, and takes out only its own (@TheMist016 on X).
func TestPaseoAdditionalModels(t *testing.T) {
	home := syncHome(t)
	t.Setenv("PASEO_HOME", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".paseo")
	path := filepath.Join(dir, "config.json")
	a := paseo(home)
	if a.Detected() {
		t.Fatal("found with no ~/.paseo")
	}
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "paseo" }) {
		t.Fatal("Paseo is not among the agents")
	}
	os.MkdirAll(dir, 0o755)
	user := `{"version":1,"daemon":{"listen":"127.0.0.1:6767"},"agents":{"providers":{"claude":{"additionalModels":[{"id":"mine/x","label":"Mine"}],"env":{"A":"b"}},"codex":{"enabled":true}}}}`
	os.WriteFile(path, []byte(user), 0o600)
	f := a.Field("provider")
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if f.Get() != magpieID {
		t.Fatalf("get: %q", f.Get())
	}
	type doc struct {
		Daemon map[string]any `json:"daemon"`
		Agents struct {
			Providers map[string]struct {
				Additional []struct {
					ID    *string `json:"id"`
					Label *string `json:"label"`
					Desc  string  `json:"description"`
				} `json:"additionalModels"`
				Env     map[string]string `json:"env"`
				Enabled bool              `json:"enabled"`
			} `json:"providers"`
		} `json:"agents"`
	}
	read := func() doc {
		var d doc
		if err := json.Unmarshal([]byte(readFile(path)), &d); err != nil {
			t.Fatalf("%v\n%s", err, readFile(path))
		}
		return d
	}
	d := read()
	c := d.Agents.Providers["claude"]
	if d.Daemon["listen"] != "127.0.0.1:6767" || c.Env["A"] != "b" || !d.Agents.Providers["codex"].Enabled {
		t.Errorf("the user's own keys changed: %s", readFile(path))
	}
	var ids []string
	for _, e := range c.Additional {
		if e.ID == nil || e.Label == nil || *e.ID == "" || *e.Label == "" {
			t.Errorf("an entry Paseo's schema turns away: %+v", e)
		}
		ids = append(ids, *e.ID)
	}
	if len(ids) < 2 || ids[0] != "mine/x" || !slices.Contains(ids, "deepseek/pro") {
		t.Errorf("ids: %v", ids)
	}
	for _, e := range c.Additional[1:] {
		if !strings.HasSuffix(e.Desc, paseoMark) {
			t.Errorf("not marked as magpie's: %+v", e)
		}
	}
	n := len(ids)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(read().Agents.Providers["claude"].Additional) != n {
		t.Error("a sync changed the list")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatal("still wired after off")
	}
	c = read().Agents.Providers["claude"]
	if len(c.Additional) != 1 || *c.Additional[0].ID != "mine/x" || c.Env["A"] != "b" {
		t.Errorf("after off: %s", readFile(path))
	}
	// off, a sync leaves it off
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(read().Agents.Providers["claude"].Additional) != 1 {
		t.Error("a sync put it back")
	}
	// on again with no file at all, then off leaves no empty key behind
	os.Remove(path)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if len(read().Agents.Providers["claude"].Additional) < 1 {
		t.Error("no models in a new file")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(path), "additionalModels") {
		t.Errorf("left behind: %s", readFile(path))
	}
}
