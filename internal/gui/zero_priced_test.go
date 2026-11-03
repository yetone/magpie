package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// jiangguochao666 on X: the provider editor's model list and the Routing
// page's add-a-model picker say free by a model its provider lists at $0
// (zero, provider.PricedFree), as OpenRouter's foo:free, and not by the
// paid foo beside it; a paid model sends no zero at all.
func TestZeroPricedModelsSayFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"zfree":{"models":{
		"foo:free":{"id":"foo:free","cost":{"input":0,"output":0}},
		"foo":{"id":"foo","cost":{"input":1,"output":2}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	p := provider.Provider{ID: "zf", Name: "ZF", Catalog: "zfree", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"foo:free", "foo"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	ms := []catalog.Model{{ID: "foo:free"}, {ID: "foo"}}
	zero := map[string]bool{}
	for _, m := range providerInfoWith(p, provider.Listing{Available: ms, Exposed: ms}, nil, nil).Models {
		zero[m.ID] = m.Zero
		b, _ := json.Marshal(m)
		if !m.Zero && strings.Contains(string(b), `"zero"`) {
			t.Errorf("a paid model sends zero: %s", b)
		}
	}
	if !zero["foo:free"] || zero["foo"] {
		t.Fatalf("the editor's zero: %v, want foo:free only", zero)
	}

	zero = map[string]bool{}
	for _, m := range groupsState().Models {
		zero[m.ID] = m.Zero
	}
	if !zero["zf/foo:free"] || zero["zf/foo"] {
		t.Fatalf("the Routing page's zero: %v, want zf/foo:free only", zero)
	}
}
