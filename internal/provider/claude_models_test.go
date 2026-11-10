package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// The models.dev catalog's anthropic rows, as models.dev served them on
// 2026-10-10: Opus 5.5, and Haiku 5.5, which it listed from 2026-10-07.
const (
	mdOpus55  = `"claude-opus-5-5":{"id":"claude-opus-5-5","name":"Claude Opus 5.5","family":"claude-opus","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}],"tool_call":true,"structured_output":true,"temperature":false,"knowledge":"2026-06","release_date":"2026-09-22","last_updated":"2026-09-22","modalities":{"input":["text","image","pdf"],"output":["text"]},"open_weights":false,"limit":{"context":1000000,"output":128000},"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}`
	mdHaiku55 = `"claude-haiku-5-5":{"id":"claude-haiku-5-5","name":"Claude Haiku 5.5","family":"claude-haiku","attachment":true,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","medium","high","xhigh","max"]}],"tool_call":true,"structured_output":true,"temperature":false,"knowledge":"2026-06","release_date":"2026-10-07","last_updated":"2026-10-07","modalities":{"input":["text","image","pdf"],"output":["text"]},"open_weights":false,"limit":{"context":1000000,"output":128000},"cost":{"input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125,"tiers":[{"input":0.5,"output":2.5,"cache_read":0.05,"cache_write":0.625,"tier":{"type":"context","size":100000}}]}}`
)

func writeAnthropicCatalog(t *testing.T, rows ...string) {
	t.Helper()
	body := `{"anthropic":{"id":"anthropic","name":"Anthropic","models":{`
	for i, r := range rows {
		if i > 0 {
			body += ","
		}
		body += r
	}
	body += `}}}`
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
}

// A Claude model models.dev lists after the Claude account's list was
// last fetched is served at once, as the catalog has it: the copy a fetch
// keeps doesn't hold it out until the next Refresh (wakaka on Discord:
// Haiku 5.5 in Claude Code on the same account, not in magpie).
func TestClaudeAccountServesModelsListedSinceItsFetch(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	t.Cleanup(catalog.Reset)

	writeAnthropicCatalog(t, mdOpus55)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil {
		t.Fatalf("claude: %+v %v", p, ok)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// models.dev lists Haiku 5.5 now; the hourly sync wrote it
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)
	p, _ = find(All(), "claude")
	if _, ok := p.Fetched(); !ok {
		t.Fatal("the fetch left no list")
	}
	i := slices.IndexFunc(p.Available(), func(m catalog.Model) bool { return m.ID == "claude-haiku-5-5" })
	if i < 0 {
		t.Fatalf("Haiku 5.5 not served on the Claude account: %v", modelIDs(p.Available()))
	}
	h := p.Available()[i]
	if h.Name != "Claude Haiku 5.5" || h.Context != 1_000_000 || h.Output != 128_000 || !h.Reasoning ||
		!slices.Equal(h.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("Haiku 5.5: %+v", h)
	}
	if h.Price == nil || h.Price.Input != 0.1 || h.Price.Output != 0.5 {
		t.Fatalf("Haiku 5.5's price: %+v", h.Price)
	}
	var listed []string
	for _, e := range Catalog() {
		listed = append(listed, e.ID)
	}
	if !slices.Contains(listed, "claude/claude-haiku-5-5") {
		t.Fatalf("not among the models agents are offered: %v", listed)
	}
	if rp, model, ok := Resolve("claude/claude-haiku-5-5"); !ok || rp.ID != "claude" || model != "claude-haiku-5-5" {
		t.Fatalf("resolve: %+v %q %v", rp, model, ok)
	}

	// and a model models.dev no longer lists leaves the account's list too
	writeAnthropicCatalog(t, mdHaiku55)
	p, _ = find(All(), "claude")
	if got := modelIDs(p.Available()); !slices.Equal(got, []string{"claude-haiku-5-5"}) {
		t.Fatalf("after Opus 5.5 left the catalog: %v", got)
	}
}

// The picks wakaka's and the owner's Claude accounts had saved: every
// model listed then, newest first, with no PickedFrom kept beside them, as
// providers.json held them on 2026-10-10. Haiku 5.5, listed since, was
// never served: agents are given the picks alone.
var claudeAllPicked = []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-1", "claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-5", "claude-opus-4-5-20251101", "claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929"}

// mdOpus41 is an older row, for a model listed before the picks were
// saved and left unpicked.
const mdOpus41 = `"claude-opus-4-1":{"id":"claude-opus-4-1","name":"Claude Opus 4.1","family":"claude-opus","reasoning":true,"tool_call":true,"release_date":"2025-08-05","last_updated":"2025-08-05","limit":{"context":200000,"output":32000}}`

func claudeExposed(t *testing.T) []string {
	t.Helper()
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil {
		t.Fatalf("claude: %+v %v", p, ok)
	}
	return modelIDs(p.Exposed())
}

// A model models.dev lists after the user picked every model the Claude
// account listed is served beside those picks (wakaka on Discord: Haiku
// 5.5 still missing after 9b51428e, whose list had it, as the picks held
// it out); one listed then and left unpicked stays out.
func TestClaudePicksOfEveryModelTakeOnesListedSince(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	t.Cleanup(catalog.Reset)
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)

	// picks saved before PickedFrom was kept, as the reporter's are
	if err := store(file{Providers: []Provider{{ID: "claude", Models: claudeAllPicked}}}); err != nil {
		t.Fatal(err)
	}
	if got := claudeExposed(t); !slices.Contains(got, "claude-haiku-5-5") || !slices.Contains(got, "claude-sonnet-5-5") {
		t.Fatalf("all picked before Haiku 5.5 was listed: %v", got)
	}
	if p, _ := find(All(), "claude"); !slices.Contains(p.Picks(), "claude-haiku-5-5") {
		t.Fatalf("the editor's picks: %v", p.Picks())
	}
	// an older model the user left unpicked: the picks aren't all of them
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55, mdOpus41)
	if got := claudeExposed(t); slices.Contains(got, "claude-haiku-5-5") || slices.Contains(got, "claude-opus-4-1") {
		t.Fatalf("picks that left one out took more: %v", got)
	}

	// picks saved now keep what was listed: Haiku 5.5 unticked stays out
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)
	if err := SetModels("claude", []string{"claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	if got := claudeExposed(t); !slices.Equal(got, []string{"claude-opus-5-5"}) {
		t.Fatalf("Haiku 5.5 left unpicked: %v", got)
	}
	// and every model picked, before Haiku 5.5 was listed, takes it then
	writeAnthropicCatalog(t, mdOpus55)
	if err := SetModels("claude", []string{"claude-opus-5-5", "claude-sonnet-5-5"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := find(All(), "claude"); !slices.Equal(p.PickedFrom, []string{"claude-opus-5-5"}) {
		t.Fatalf("listed with the picks: %v", p.PickedFrom)
	}
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)
	if got := claudeExposed(t); !slices.Equal(got, []string{"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5"}) {
		t.Fatalf("listed since every model was picked: %v", got)
	}
	// a Save of the same picks keeps the list they were picked from
	if err := SetModels("claude", []string{"claude-opus-5-5", "claude-sonnet-5-5", " claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	p, _ := find(All(), "claude")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if p, _ := find(All(), "claude"); !slices.Equal(p.PickedFrom, []string{"claude-opus-5-5"}) {
		t.Fatalf("listed after a Save of the same picks: %v", p.PickedFrom)
	}
}

// mdFable52 is models.dev's anthropic row for Claude Fable 5.2, as it
// served it on 2026-11-10, the day after the launch.
const mdFable52 = `"claude-fable-5-2":{"id":"claude-fable-5-2","name":"Claude Fable 5.2","description":"Claude model for demanding reasoning and long-horizon agentic work","family":"claude-fable","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}],"tool_call":true,"structured_output":true,"temperature":false,"knowledge":"2026-06","release_date":"2026-11-09","last_updated":"2026-11-09","modalities":{"input":["text","image","pdf"],"output":["text"]},"open_weights":false,"limit":{"context":1000000,"output":128000},"experimental":{"modes":{"fast":{"cost":{"input":16,"output":80,"cache_read":0.4,"cache_write":20},"provider":{"body":{"speed":"fast"}}}}},"cost":{"input":8,"output":40,"cache_read":0.2,"cache_write":10},"canonical_model_id":"anthropic/claude-fable-5-2"}`

// Claude Fable 5.2, once the catalog sync has models.dev's row, is on a
// Claude account's list first (newest), at Anthropic's price, window and
// efforts, and served beside picks that held every model listed before
// it (thelegendof2414 on X: Fable 5.2 not in magpie's list).
func TestClaudeAccountListsFable52(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	t.Cleanup(catalog.Reset)
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)
	if err := store(file{Providers: []Provider{{ID: "claude", Models: claudeAllPicked}}}); err != nil {
		t.Fatal(err)
	}
	if got := claudeExposed(t); slices.Contains(got, "claude-fable-5-2") {
		t.Fatalf("listed before models.dev had it: %v", got)
	}

	writeAnthropicCatalog(t, mdOpus55, mdHaiku55, mdFable52)
	p, _ := find(All(), "claude")
	av := p.Available()
	if len(av) == 0 || av[0].ID != "claude-fable-5-2" {
		t.Fatalf("Fable 5.2 not first: %v", modelIDs(av))
	}
	f := av[0]
	if f.Name != "Claude Fable 5.2" || f.Context != 1_000_000 || f.Output != 128_000 || !f.Reasoning ||
		!slices.Equal(f.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("Fable 5.2: %+v", f)
	}
	if f.Price == nil || f.Price.Input != 8 || f.Price.Output != 40 || f.Price.CacheRead != 0.2 || f.Price.CacheWrite != 10 {
		t.Fatalf("Fable 5.2's price: %+v", f.Price)
	}
	if got := claudeExposed(t); !slices.Contains(got, "claude-fable-5-2") {
		t.Fatalf("not served beside picks of every model listed before: %v", got)
	}
}

// Fetch models again on a Claude subscription fetches models.dev's list
// first when the catalog magpie keeps is older than a few minutes: a
// model launched since the day's sync is on the list the user asked for
// (hhdaxia on X: Fable 5.2 in Claude Code's /model, on models.dev, and
// not in the vendor's list "just now" fetched). Outside a process that
// keeps the catalog fresh (a CLI command, a test) nothing is fetched.
func TestClaudeFetchAsksModelsDevForANewModel(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	t.Cleanup(catalog.Reset)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"anthropic":{"id":"anthropic","name":"Anthropic","models":{` + mdOpus55 + "," + mdHaiku55 + "," + mdFable52 + `}}}`))
	}))
	defer srv.Close()

	// the day's sync, two hours before Fable 5.2 reached models.dev
	writeAnthropicCatalog(t, mdOpus55, mdHaiku55)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(catalog.CachePath(), old, old); err != nil {
		t.Fatal(err)
	}
	fetch := func() []string {
		t.Helper()
		p, ok := find(All(), "claude")
		if !ok || p.Account == nil {
			t.Fatalf("claude: %+v %v", p, ok)
		}
		ms, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return modelIDs(ms)
	}

	t.Cleanup(catalog.KeepFreshForTest(srv.URL))
	if got := fetch(); len(got) == 0 || got[0] != "claude-fable-5-2" {
		t.Fatalf("Fable 5.2 not first after a fetch: %v (%d asks of models.dev)", got, hits.Load())
	}
	p, _ := find(All(), "claude")
	if !slices.ContainsFunc(p.Available(), func(m catalog.Model) bool { return m.ID == "claude-fable-5-2" }) {
		t.Fatalf("Fable 5.2 not served: %v", modelIDs(p.Available()))
	}
	// fetched again at once: models.dev is asked nothing more
	fetch()
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d asks of models.dev, want 1", n)
	}

	// a process that doesn't keep the catalog fresh asks nothing
	catalog.KeepFreshForTest(srv.URL)()
	writeAnthropicCatalog(t, mdOpus55)
	os.Chtimes(catalog.CachePath(), old, old)
	if got := fetch(); slices.Contains(got, "claude-fable-5-2") {
		t.Fatalf("models.dev asked outside KeepFresh: %v", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d asks of models.dev, want 1", n)
	}
}
