package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strconv"
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
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
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
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
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
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
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

// Antigravity's app takes no more than 32 of a gateway's catalog entries
// and skips the rest. Listed once a level, 25 models were some 50, and
// the Gemini ones last in the list never reached its menu (0xBCD18E on
// X). Every model now gets one entry while they fit, levels only in the
// room left, and the model the app starts on is the entry it lists.
func TestAntigravityCatalogFitsTheApp(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, n := range []int{25, 40} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			setup(t, provider.Anthropic, &fake{})
			var ids []string
			var models []catalog.Model
			for i := range n {
				id := fmt.Sprintf("m%02d", i)
				if i >= n-6 {
					id = fmt.Sprintf("gemini-4-%02d", i)
				}
				ids = append(ids, id)
				models = append(models, catalog.Model{ID: id, Efforts: []string{"low", "medium", "high"}})
			}
			if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: ids}); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive("ds", "http://127.0.0.1:1/v1", models); err != nil {
				t.Fatal(err)
			}
			shown, _ := provider.CatalogFor("antigravity")
			req := httptest.NewRequest("GET", "/v1/models?format="+AntigravityFormat, nil)
			req.Header.Set("Authorization", "Bearer "+TokenFor("antigravity"))
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			var got struct {
				List []struct{ ModelID string } `json:"list"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(got.List) > AntigravityMenuMax {
				t.Fatalf("%d entries for %d models; the app takes %d", len(got.List), len(shown), AntigravityMenuMax)
			}
			// each of the first 32 models once, in order, at its levels or by its id
			var bases []string
			levels := map[string]int{}
			for _, m := range got.List {
				b, _, _ := strings.Cut(m.ModelID, ":")
				if len(bases) == 0 || bases[len(bases)-1] != b {
					bases = append(bases, b)
				}
				levels[b]++
			}
			var want []string
			for _, e := range shown[:min(len(shown), AntigravityMenuMax)] {
				want = append(want, e.ID)
			}
			if !slices.Equal(bases, want) {
				t.Fatalf("listed %v\nwant %v", bases, want)
			}
			if n == 25 && !slices.Contains(bases, "ds/gemini-4-24") {
				t.Errorf("the last model is missing: %v", bases)
			}
			// the room left goes to levels, and the app starts on the entry listed
			if len(shown) < AntigravityMenuMax && len(got.List) < AntigravityMenuMax-1 {
				t.Errorf("%d entries for %d models: levels left out with room for them", len(got.List), len(shown))
			}
			listed := map[string]bool{}
			for _, m := range got.List {
				listed[m.ModelID] = true
			}
			for _, b := range bases {
				start := AntigravityModel(b, "high")
				if !listed[start] {
					t.Errorf("AntigravityModel(%q) = %q, not in the catalog", b, start)
				}
				if (levels[b] > 1) != strings.Contains(start, ":") {
					t.Errorf("AntigravityModel(%q) = %q with %d entries", b, start, levels[b])
				}
			}
		})
	}
}
