package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Antigravity's app reads /v1/models by id alone and gives a model its
// catalog doesn't know a window of 200K tokens (YujunT46308 on X); its
// models.listSource catalog takes one per model. magpie's, at
// /v1/models?format=antigravity, is {"list": [...]} in the app's own
// fields, the models the key may use, each with its window and name.
func TestAntigravityCatalog(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"big", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ds", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "big", Context: 1048576, Output: 65536, Images: true, Efforts: []string{"low", "high"}}, {ID: "plain"}}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) []byte {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+TokenFor("antigravity"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.Bytes()
	}
	var got struct {
		List []map[string]any `json:"list"`
	}
	body := get("/v1/models?format=" + AntigravityFormat)
	if err := json.Unmarshal(body, &got); err != nil || got.List == nil {
		t.Fatalf("not the app's catalog: %s", body)
	}
	by := map[string]map[string]any{}
	var ids []string
	for _, m := range got.List {
		id, _ := m["modelId"].(string)
		by[id] = m
		ids = append(ids, id)
	}
	big := by["ds/big"]
	if big == nil {
		t.Fatalf("ds/big not listed: %s", body)
	}
	if big["maxTokens"] != float64(1048576) || big["maxOutputTokens"] != float64(65536) {
		t.Errorf("ds/big's window: %v", big)
	}
	if big["supportsImages"] != true || big["supportsThinking"] != true {
		t.Errorf("ds/big's abilities: %v", big)
	}
	if n, _ := big["displayName"].(string); n == "" {
		t.Errorf("ds/big has no name: %v", big)
	}
	// a window magpie doesn't know is left to the app's default
	if p := by["ds/plain"]; p == nil {
		t.Fatalf("ds/plain not listed: %s", body)
	} else if _, ok := p["maxTokens"]; ok {
		t.Errorf("ds/plain given a window: %v", p)
	}
	// the same models as the plain list asked with the key
	var plain struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.Unmarshal(get("/v1/models"), &plain)
	var want []string
	for _, d := range plain.Data {
		want = append(want, d.ID)
	}
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("catalog %v, list %v", ids, want)
	}
}
