package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// OpenCode's magpie plugin fills magpie's provider from
// /v1/models?format=opencode (opencode2 for OpenCode 2): {"models": [{id,
// entry}]}, the agent package's entries in magpie's order, never cached,
// and only the models of OpenCode's catalog the key asking may use.
func TestOpenCodeModelsList(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	for _, id := range []string{"ds", "other"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"big", "plain"}}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(id, "http://127.0.0.1:1/v1", []catalog.Model{{ID: "big", Context: 1048576}, {ID: "plain"}}); err != nil {
			t.Fatal(err)
		}
	}
	was := OpenCodeModels
	t.Cleanup(func() { OpenCodeModels = was })
	var asked []bool
	OpenCodeModels = func(two bool) []OpenCodeModel {
		asked = append(asked, two)
		return []OpenCodeModel{
			{ID: "other/plain", Entry: map[string]any{"name": "Plain", "two": two}},
			{ID: "ds/big", Entry: map[string]any{"name": "Big"}},
			{ID: "nowhere/x", Entry: map[string]any{"name": "not in OpenCode's catalog"}},
		}
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"ds/*"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	get := func(format, secret string) []string {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?format="+format, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", format, rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control %q", cc)
		}
		var got struct {
			Models []struct {
				ID    string         `json:"id"`
				Entry map[string]any `json:"entry"`
			} `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Models == nil {
			t.Fatalf("not OpenCode's list: %s", rec.Body)
		}
		var ids []string
		for _, m := range got.Models {
			if m.Entry["name"] == nil {
				t.Errorf("%s has no entry: %s", m.ID, rec.Body)
			}
			ids = append(ids, m.ID)
		}
		return ids
	}
	local := TokenFor("opencode")
	if ids := get(OpenCodeFormat, local); !slices.Equal(ids, []string{"other/plain", "ds/big"}) {
		t.Fatalf("opencode lists %v", ids)
	}
	if ids := get(OpenCode2Format, local); !slices.Equal(ids, []string{"other/plain", "ds/big"}) {
		t.Fatalf("opencode2 lists %v", ids)
	}
	if !slices.Equal(asked, []bool{false, true}) {
		t.Fatalf("asked for %v", asked)
	}
	if ids := get(OpenCodeFormat, secrets[0]); !slices.Equal(ids, []string{"ds/big"}) {
		t.Fatalf("a key held to ds/* is told of %v", ids)
	}
}
