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
