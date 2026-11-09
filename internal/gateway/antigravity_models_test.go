package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
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
	// a model with levels is listed once a level, named as the app's
	// picker gathers into one row with an effort menu
	if by["ds/big"] != nil {
		t.Errorf("ds/big listed without a level: %s", body)
	}
	big := by["ds/big:high"]
	if big == nil || by["ds/big:low"] == nil {
		t.Fatalf("ds/big's levels not listed: %s", body)
	}
	low, _ := by["ds/big:low"]["displayName"].(string)
	high, _ := big["displayName"].(string)
	name, ok := strings.CutSuffix(high, " (High)")
	if !ok || low != name+" (Low)" {
		t.Errorf("ds/big's levels named %q and %q", low, high)
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
		if d.ID == "ds/big" {
			want = append(want, "ds/big:high", "ds/big:low")
			continue
		}
		want = append(want, d.ID)
	}
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("catalog %v, list %v", ids, want)
	}
}

// Antigravity's picker (2.29.0) gathers "<name> (Low)", "(Medium)",
// "(High)", "(XHigh)", "(Max)" into one row with an effort menu; a
// level it has no word for is left out, and a model with one level left
// is listed once, as a menu of one is no choice (YujunT46308 on X).
func TestAntigravityLevels(t *testing.T) {
	for _, c := range []struct {
		in, want []string
	}{
		{[]string{"high", "low"}, []string{"low", "high"}},
		{[]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, []string{"low", "medium", "high", "xhigh", "max"}},
		{[]string{"minimal", "high"}, nil},
		{[]string{"high"}, nil},
		{nil, nil},
	} {
		if got := antigravityLevels(c.in); !slices.Equal(got, c.want) {
			t.Errorf("antigravityLevels(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The model the app starts on is listed as its catalog lists it: at the
// level asked, fitted to the model's, else at medium, as the app's own
// picker starts one, else its weakest; a model with no levels as it is.
func TestAntigravityModelStartsOnALevel(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"three", "two", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ds", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "three", Efforts: []string{"low", "medium", "high"}}, {ID: "two", Efforts: []string{"low", "high"}}, {ID: "plain"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ref, want, got string }{
		{"ds/three", "", "ds/three:medium"},
		{"ds/three", "high", "ds/three:high"},
		{"ds/three", "xhigh", "ds/three:high"},
		{"ds/two", "", "ds/two:low"},
		{"ds/two", "medium", "ds/two:high"},
		{"ds/plain", "high", "ds/plain"},
		{"ds/gone", "high", "ds/gone"},
	} {
		if got := AntigravityModel(c.ref, c.want); got != c.got {
			t.Errorf("AntigravityModel(%q, %q) = %q, want %q", c.ref, c.want, got, c.got)
		}
	}
}
