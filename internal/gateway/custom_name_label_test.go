package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// /v1/models' magpie_label names a model as the agents' lists do, by the
// Provider in model names setting: on, as by default, a name the user gave
// a model has its provider's after it like any other (#203); own, that
// name is just as they wrote it and the vendor's keep theirs (#92: "Opus
// 5.5", not "Opus 5.5 · Claude Code"); off, none has it (#335).
// display_name is the name alone throughout.
func TestModelsListSuffixModes(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"a":{"models":{"sol":{"id":"sol","name":"Sol"}}},"b":{"models":{"luna":{"id":"luna","name":"Luna"}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for id, m := range map[string]string{"a": "sol", "b": "luna"} {
		if err := provider.Save(provider.Provider{ID: id, Name: "Vendor " + strings.ToUpper(id), Catalog: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{m}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelName("a/sol", "Sol 5"); err != nil {
		t.Fatal(err)
	}
	list := func() map[string]string {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
		var out struct {
			Data []struct {
				ID    string `json:"id"`
				Name  string `json:"display_name"`
				Label string `json:"magpie_label"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, m := range out.Data {
			got[m.ID] = m.Name + "|" + m.Label
		}
		return got
	}
	for _, c := range []struct{ mode, sol, luna string }{
		{provider.SuffixOn, "Sol 5|Sol 5 · Vendor A", "Luna|Luna · Vendor B"},
		{provider.SuffixOwn, "Sol 5|Sol 5", "Luna|Luna · Vendor B"},
		{provider.SuffixOff, "Sol 5|Sol 5", "Luna|Luna"},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		if got := list(); got["a/sol"] != c.sol || got["b/luna"] != c.luna {
			t.Fatalf("%s: %v", c.mode, got)
		}
	}
}
