package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Packing1 on Discord: dsh through magpie alone, every web search failed
// asking for DEEPSEEK_API_KEY — dsh's web-search-deepseek plugin signs its
// own requests to DeepSeek. While dsh starts on a model of magpie's, the row
// points at the gateway with magpie's key and that model; it goes again with
// magpie's model, and a row or a DeepSeek key of the user's is left as it is.
func TestDshSearchThroughGateway(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	template := "# Your patch layer for this dsh profile.\n[]\n"
	os.WriteFile(web, []byte(template), 0o644)
	read := func() string { b, _ := os.ReadFile(web); return string(b) }
	a := dsh(home)
	f := a.Field("model")

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	want := strings.Join(dshSearchLines("deepseek/pro", gateway.URL()), "\n")
	if !strings.Contains(s, want) || strings.Count(s, "id: "+dshSearchRow) != 1 {
		t.Fatalf("search row:\n%s", s)
	}
	for _, w := range []string{"- id: web-search-deepseek # magpie", "apiKeyEnv: " + dshKeyRef, `baseURL: "` + gatewayV1() + `"`, `model: "deepseek/pro"`, `name: "@deepseek-ai/dsh-web-search-deepseek"`} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q in\n%s", w, s)
		}
	}
	if a.Check() != "" {
		t.Fatalf("check: %q", a.Check())
	}

	// another model of magpie's: the search asks it, still one row
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, `model: "deepseek/flash"`) || strings.Count(s, "id: "+dshSearchRow) != 1 {
		t.Fatalf("model moved:\n%s", s)
	}

	// the gateway's address moved: the round that writes the route again
	// writes the search's too, its model kept
	if _, err := dshRouteAgain(web, magpieModels("dsh"), "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, `baseURL: "http://127.0.0.1:9/v1"`) || !strings.Contains(s, `model: "deepseek/flash"`) {
		t.Fatalf("gateway moved:\n%s", s)
	}

	// dsh's own model: its own search back, as it ships
	if err := f.Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); strings.Contains(s, dshSearchRow) {
		t.Fatalf("own model kept magpie's search:\n%s", s)
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != template {
		t.Fatalf("reset:\n%s", s)
	}

	// a DeepSeek key of dsh's own: its search works as it is
	env := filepath.Join(dir, ".env")
	os.WriteFile(env, []byte("DEEPSEEK_API_KEY=sk-mine\n"), 0o600)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); strings.Contains(s, dshSearchRow) {
		t.Fatalf("own DeepSeek key, search taken:\n%s", s)
	}
	f.Set("")
	os.Remove(env)
	os.WriteFile(filepath.Join(dir, ".credentials.yaml"), []byte("version: 1\nrecords:\n  DEEPSEEK_API_KEY:\n    kind: secret\n    payload:\n      value: sk-mine\n"), 0o600)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); strings.Contains(s, dshSearchRow) {
		t.Fatalf("own DeepSeek key in dsh's store, search taken:\n%s", s)
	}
	f.Set("")
	os.Remove(filepath.Join(dir, ".credentials.yaml"))

	// a row of the user's stays through connect and disconnect
	own := "# Your patch layer for this dsh profile.\n- id: web-search-deepseek\n  config:\n    apiKeyEnv: MY_KEY\n    baseURL: https://search.example/v1\n"
	os.WriteFile(web, []byte(own), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.HasPrefix(s, own) || strings.Count(s, dshSearchRow) != 1 {
		t.Fatalf("user's row:\n%s", s)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != own {
		t.Fatalf("user's row after reset:\n%s", s)
	}
}

// 我只会玩单机 on Discord: with a searcher named under Settings › Models › Web search
// (GPT-6 Luna), dsh's search row still asked the model dsh starts on, which
// can't search by itself and asked the searcher in turn: two requests a
// search, and a model that takes one call at a time timed out. The row
// asks the named searcher, follows it being picked and cleared on the
// round that writes the route again, and a row of the user's stays.
func TestDshSearchAsksNamedSearcher(t *testing.T) {
	home, _, web := dshRouteHome(t)
	if err := provider.Save(provider.Provider{ID: "oai", Name: "OpenAI", Responses: "https://api.openai.com/v1", Key: "k", Models: []string{"gpt-6-luna", "gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	name := func(v string) {
		t.Helper()
		st := settings.Load()
		st.Searcher = v
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	template := "# Your patch layer for this dsh profile.\n[]\n"
	os.WriteFile(web, []byte(template), 0o644)
	read := func() string { b, _ := os.ReadFile(web); return string(b) }
	f := dsh(home).Field("model")

	name("oai/gpt-6-luna")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	if !strings.Contains(s, `model: "oai/gpt-6-luna"`) || strings.Count(s, "id: "+dshSearchRow) != 1 {
		t.Fatalf("named searcher not asked:\n%s", s)
	}
	if !strings.Contains(s, "model: deepseek/pro") && !strings.Contains(s, `model: "deepseek/pro"`) {
		t.Fatalf("dsh's own start lost:\n%s", s)
	}

	// cleared in Settings: the next round asks dsh's start again
	name("")
	if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, `model: "deepseek/pro"`) || strings.Contains(s, `model: "oai/gpt-6-luna"`) {
		t.Fatalf("searcher cleared:\n%s", s)
	}
	// named again, while dsh already runs through magpie
	name("oai/gpt-6-luna")
	if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, `model: "oai/gpt-6-luna"`) {
		t.Fatalf("searcher named later:\n%s", s)
	}
	// provider search off: the start model, as before
	name("off")
	if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, `model: "deepseek/pro"`) {
		t.Fatalf("search off:\n%s", s)
	}
	name("oai/gpt-6-luna")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != template {
		t.Fatalf("reset:\n%s", s)
	}

	// a row of the user's, its own model, stays with a searcher named
	own := "# Your patch layer for this dsh profile.\n- id: web-search-deepseek\n  config:\n    apiKeyEnv: MY_KEY\n    baseURL: https://search.example/v1\n    model: my-search\n"
	os.WriteFile(web, []byte(own), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.HasPrefix(s, own) || strings.Count(s, dshSearchRow) != 1 || strings.Contains(s, `model: "oai/gpt-6-luna"`) {
		t.Fatalf("user's row:\n%s", s)
	}
}

// #7461 (oranguice): from DSH 0.2.2 dsh-web's base names no search
// provider, and dsh-web picks DeepSeek's paid Search API on every route but
// dsh's own DeepSeek ones, so a session on a model of magpie's searched on
// the user's DeepSeek balance, past magpie's web-search-deepseek row. While
// that row is magpie's, magpie's web row names deepseek-official beside the
// fetchProvider every base names. A provider the user picks (a web row, its
// "Automatic" taking magpie's row out, the home patch or
// DSH_WEB_SEARCH_PROVIDER) stays theirs, and a reset leaves the file as it
// was.
func TestDshSearchPinsDeepSeekOfficial(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	t.Setenv(dshWebSearchEnv, "")
	template := "# Your patch layer for this dsh profile.\n[]\n"
	os.WriteFile(web, []byte(template), 0o644)
	read := func() string { b, _ := os.ReadFile(web); return string(b) }
	pin := strings.Join(dshWebLines(), "\n") + "\n"
	f := dsh(home).Field("model")
	again := func() {
		t.Helper()
		if _, err := dshRouteAgain(web, magpieModels("dsh"), gateway.URL()); err != nil {
			t.Fatal(err)
		}
	}

	// the reporter's case: magpie's search row, and dsh told to use it
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	if !strings.Contains(s, pin) || strings.Count(s, "- id: web #") != 1 || !strings.Contains(s, "- id: "+dshSearchRow+" #") {
		t.Fatalf("no pin beside magpie's search:\n%s", s)
	}
	_, items, err := dshLook(web)
	if err != nil {
		t.Fatal(err)
	}
	if c := dshConfig(items[dshFindLast(items, dshWebRow)]); c["searchProvider"] != "deepseek-official" || c["fetchProvider"] != "http" {
		t.Fatalf("pin reads %v", c)
	}
	again()
	if s = read(); strings.Count(s, "- id: web #") != 1 {
		t.Fatalf("pin twice:\n%s", s)
	}
	// dsh's own model: dsh picks deepseek-official there itself
	if err := f.Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); strings.Contains(s, "- id: web #") {
		t.Fatalf("own model kept the pin:\n%s", s)
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != template {
		t.Fatalf("reset:\n%s", s)
	}

	// "Automatic" on dsh's Web search page takes magpie's row out: it stays
	// out on the next round, and magpie's search row is still there
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(web, []byte(strings.Replace(read(), pin, "", 1)), 0o644)
	again()
	if s = read(); strings.Contains(s, "- id: web #") || !strings.Contains(s, "- id: "+dshSearchRow+" #") {
		t.Fatalf("Automatic undone:\n%s", s)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != template {
		t.Fatalf("reset after Automatic:\n%s", s)
	}
	stash(map[string]string{dshWebPinKey(web): ""}) // a fresh start

	// another provider picked on that page, in magpie's row: kept, on the
	// next round and after a reset
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	picked := strings.Replace(read(), "searchProvider: deepseek-official", "searchProvider: exa", 1)
	os.WriteFile(web, []byte(picked), 0o644)
	again()
	if s = read(); !strings.Contains(s, "searchProvider: exa") || strings.Contains(s, "deepseek-official") {
		t.Fatalf("picked provider written over:\n%s", s)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.Contains(s, "searchProvider: exa") {
		t.Fatalf("picked provider taken out on reset:\n%s", s)
	}
	stash(map[string]string{dshWebPinKey(web): ""})

	// a web row of the user's stays as it is
	own := "# Your patch layer for this dsh profile.\n- id: web\n  config:\n    fetchProvider: jina\n    searchProvider: deepseek-search-api\n"
	os.WriteFile(web, []byte(own), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if s = read(); !strings.HasPrefix(s, own) || strings.Contains(s, "deepseek-official") {
		t.Fatalf("user's web row:\n%s", s)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != own {
		t.Fatalf("user's web row after reset:\n%s", s)
	}

	// a provider picked outside the profile: the home patch, dsh's .env, or
	// the environment dsh starts in
	os.WriteFile(web, []byte(template), 0o644)
	for _, c := range []struct {
		name string
		set  func()
		undo func()
	}{
		{"home patch", func() {
			os.WriteFile(filepath.Join(dir, "cordis.patch.yml"), []byte("- id: web\n  config:\n    fetchProvider: http\n    searchProvider: exa\n"), 0o644)
		}, func() { os.Remove(filepath.Join(dir, "cordis.patch.yml")) }},
		{".env", func() {
			os.WriteFile(filepath.Join(dir, ".env"), []byte(dshWebSearchEnv+"=exa\n"), 0o600)
		}, func() { os.Remove(filepath.Join(dir, ".env")) }},
		{"environment", func() { os.Setenv(dshWebSearchEnv, "exa") }, func() { os.Setenv(dshWebSearchEnv, "") }},
	} {
		c.set()
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if s = read(); strings.Contains(s, "- id: web #") || !strings.Contains(s, "- id: "+dshSearchRow+" #") {
			t.Fatalf("%s: pinned over the user's pick:\n%s", c.name, s)
		}
		if err := f.Set(""); err != nil {
			t.Fatal(err)
		}
		if s = read(); s != template {
			t.Fatalf("%s: reset:\n%s", c.name, s)
		}
		c.undo()
	}
}
