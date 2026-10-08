package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's editor saves what it shows. What it doesn't show, set from
// the CLI or the TUI (`magpie provider set … family= website= keys=
// balance= balance.path= models.url=`), stays as the user set it: an
// editor's Save wrote it away, so a preset's came back as the preset's and
// a custom one's as nothing.
func TestProviderSaveKeepsWhatTheEditorDoesntShow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	type kept struct{ Family, Website, KeysURL, BalanceURL, BalancePath, ModelsURL string }
	keptOf := func(id string) kept {
		t.Helper()
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return kept{p.Family, p.Website, p.KeysURL, p.BalanceURL, p.BalancePath, p.ModelsURL}
	}

	// a preset's provider: the editor sends its key, picks and settings,
	// never these
	ds, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	ds.Key = "sk-ds"
	ds.Family = "cheap"
	ds.Website = "https://my.example.com"
	ds.KeysURL = "https://my.example.com/keys"
	ds.BalanceURL = "https://my.example.com/balance"
	ds.BalancePath = "data.left"
	ds.ModelsURL = "https://my.example.com/v1/models"
	if err := provider.Save(ds); err != nil {
		t.Fatal(err)
	}
	want := keptOf("deepseek")
	post(`{"id":"deepseek","from":"deepseek","name":"DeepSeek","preset":"deepseek","key":"","chat":"` + ds.Chat + `","responses":"` + ds.Responses + `","anthropic":"` + ds.Anthropic + `","catalog":"deepseek","models":[],"headers":{},"searches":false,"pinUpstream":false,"unredacted":false,"contexts":{},"outputs":{},"compacts":{},"proxy":"","maxConcurrency":null,"queueLimit":0,"queueWait":0,"priceRate":null}`)
	if got := keptOf("deepseek"); got != want {
		t.Errorf("preset's editor Save:\n got %+v\nwant %+v", got, want)
	}

	// a custom one: the editor sends its Balance and Models URLs, never
	// its tag, website or key page
	post(`{"id":"relay","name":"Relay","key":"sk-r","chat":"http://127.0.0.1:1/v1","icon":"generic","new":true}`)
	r, _ := provider.Find("relay")
	r.Family, r.Website, r.KeysURL = "relays", "https://relay.example.com", "https://relay.example.com/keys"
	if err := provider.Save(*r); err != nil {
		t.Fatal(err)
	}
	want = keptOf("relay")
	post(`{"id":"relay","from":"relay","name":"Relay","key":"","chat":"http://127.0.0.1:1/v1","icon":"generic","balanceURL":"","balancePath":"","modelsURL":"","models":[],"headers":{}}`)
	if got := keptOf("relay"); got != want {
		t.Errorf("custom editor Save:\n got %+v\nwant %+v", got, want)
	}
}
