package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// plugged has plugins.json list a plugin that gives one search API, as the
// host notes it after a load, with no host.
func plugged(t *testing.T, spec string, off bool) {
	t.Helper()
	offs := ""
	if off {
		offs = `,"off":true`
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	write("plugins.json", `{"plugins":[{"spec":"`+spec+`"`+offs+`}]}`)
	write("plugin-searches.json", `{"`+spec+`":[{"id":"mojeek","name":"Mojeek","keysURL":"https://www.mojeek.com/services/api/"}]}`)
}

// A search API a plugin gives is asked on the same path as Tavily and the
// rest (#treeman on Discord): it is added and keyed like them under its
// own name (<plugin>:<id>), asked with the masked query, its keys tried in
// turn with a refused one resting, its pages cut and cleaned, and every ask
// a row of the request log. Turned off, the plugin's API is not asked, and
// not listed.
func TestPluginSearchAPI(t *testing.T) {
	fresh(t)
	const spec = "@acme/opencode-mojeek-search@1.0.0"
	const id = "mojeek-search:mojeek"
	plugged(t, spec, false)
	var asked []string
	old := runPluginSearch
	t.Cleanup(func() { runPluginSearch = old })
	runPluginSearch = func(_ context.Context, sp, pid, query string, count int, key, url string) ([]plugin.SearchPage, error) {
		asked = append(asked, sp+" "+pid+" "+key+" "+query)
		switch key {
		case "spent-key-0001":
			return nil, plugin.SearchRefused{Status: 429, Msg: "slow down, " + key, RetryAfter: "30"}
		case "broken-key-0002":
			return nil, errors.New("boom with " + key)
		}
		return []plugin.SearchPage{
			{Title: "Go <b>downloads</b>", URL: "https://go.dev/dl/", Text: "Go   1.27.1\n is out", Age: "2026-10-01"},
			{Title: "no address"},
		}, nil
	}

	v, ok := provider.SearchVendorOf(id)
	if !ok || v.Name != "Mojeek" || v.Plugin != spec || v.PluginID != "mojeek" || v.KeysURL == "" {
		t.Fatalf("vendor %+v %v", v, ok)
	}
	if got := provider.AllSearchVendors(); got[len(got)-1].ID != id || len(got) != len(provider.SearchVendors)+1 {
		t.Fatalf("not listed after the built-ins: %v", got)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: id}); err == nil || !strings.Contains(err.Error(), "needs its API key") {
		t.Errorf("no key: %v", err)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "nobody:none", Key: "k"}); err == nil || !strings.Contains(err.Error(), id) {
		t.Errorf("an unknown plugin's API: %v", err)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: id, Key: "spent-key-0001,good-key-0003"}); err != nil {
		t.Fatal(err)
	}
	if !canSearch() {
		t.Fatal("a plugin's search API alone doesn't let magpie search")
	}

	s := New()
	ctx := context.WithValue(context.Background(), searchForKey{}, &CallFor{Agent: "claude"})
	said, hits, err := s.apiSearch(ctx, "latest go")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].URL != "https://go.dev/dl/" || hits[0].Title != "Go downloads" || hits[0].PageAge != "2026-10-01" ||
		!strings.Contains(said, "Go downloads — https://go.dev/dl/\nGo 1.27.1 is out") {
		t.Fatalf("said %q hits %+v", said, hits)
	}
	if len(asked) != 2 || asked[0] != spec+" mojeek spent-key-0001 latest go" || asked[1] != spec+" mojeek good-key-0003 latest go" {
		t.Fatalf("asked %q", asked)
	}
	// the refused key rests: the next search starts from the other
	asked = nil
	if _, _, err := s.apiSearch(ctx, "again"); err != nil || len(asked) != 1 || !strings.Contains(asked[0], "good-key-0003") {
		t.Fatalf("after the refusal: %v %q", err, asked)
	}
	rows, _ := searchRows(t)
	if len(rows) != 3 || rows[0].Requested != "Mojeek" || rows[0].Status != 429 || rows[0].ProviderKeyName == "spent-key-0001" ||
		rows[1].Status != 200 || rows[1].Source != usage.SourceSearch {
		t.Fatalf("log %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.Error, "spent-key-0001") || strings.Contains(r.Error, "latest go") {
			t.Errorf("row says the key or the query: %+v", r)
		}
	}

	// a failure that isn't about the key says so, without the key
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: id, Key: "broken-key-0002"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.apiSearch(ctx, "q"); err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "broken-key-0002") {
		t.Errorf("a failure: %v", err)
	}

	// the plugin off: its API isn't asked, nor listed
	plugged(t, spec, true)
	asked = nil
	if _, ok := provider.SearchVendorOf(id); ok || len(provider.SearchAPIs()) != 0 || canSearch() {
		t.Error("the plugin is off, and its search API is still there")
	}
	if s.apiSearch(ctx, "q"); len(asked) != 0 {
		t.Errorf("asked with the plugin off: %q", asked)
	}
}

// The query is masked before a plugin's search API sees it, as before
// Tavily's.
func TestPluginSearchAPISeesMaskedQuery(t *testing.T) {
	fresh(t)
	plugged(t, "mojeek-search", false)
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "mojeek-search:mojeek", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	old := runPluginSearch
	t.Cleanup(func() { runPluginSearch = old })
	var got string
	runPluginSearch = func(_ context.Context, _, _, q string, _ int, _, _ string) ([]plugin.SearchPage, error) {
		got = q
		return []plugin.SearchPage{{Title: "t", URL: "https://x.dev"}}, nil
	}
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New().apiSearch(context.Background(), "who called from 13812345678"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "{{PHONE_") || strings.Contains(got, "13812345678") {
		t.Errorf("the plugin saw %q", got)
	}
}
