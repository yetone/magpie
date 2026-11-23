package usage

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Winnie7572 on X (#7321): an OpenRouter provider's embedding calls were
// counted unpriced, 415 of 420, as models.dev's v1 catalog lists no
// embedding model of OpenRouter's. Calls already recorded are priced again
// once the catalog has v2's embedding prices: the ledger prices its rows
// as it reads them, and the catalog's key names the v2 file too, so the
// summary and the request index kept from before are not answered from.
func TestRecordedEmbeddingCallsPricedAfterSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "catalog", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	v1, v2 := read("models.dev-2026-11-23-embeddings-v1.json"), read("models.dev-v2-2026-11-23-embeddings.json")
	v2On := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api.json":
			w.Write(v1)
		case r.URL.Path == "/experimental/v2.0/api.json" && v2On:
			w.Write(v2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	restore := catalog.KeepFreshForTest(srv.URL + "/api.json")
	t.Cleanup(restore)
	t.Cleanup(catalog.Reset)
	if err := catalog.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	p, err := provider.FromPreset("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "sk-or-test"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	Append(Record{Time: now.Add(-2 * time.Minute), Agent: "claude-code", Provider: p.ID, Model: "qwen/qwen3-embedding-8b",
		Input: 120_000, Status: 200})
	Append(Record{Time: now.Add(-time.Minute), Agent: "claude-code", Provider: p.ID, Model: "openai/gpt-4o-mini",
		Input: 10_000, Output: 1_000, Status: 200})
	chat := (10_000*0.15 + 1_000*0.6) / 1e6

	rows, _, _ := Ledger(Month, Filter{})
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	if s := Summarize(Month); s.Unpriced != 1 || math.Abs(s.Cost-chat) > 1e-12 {
		t.Fatalf("before v2: cost %v unpriced %d, want %v and 1 (the fixture or the old pricing changed)", s.Cost, s.Unpriced, chat)
	}

	v2On = true
	if err := catalog.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	emb := 120_000 * 0.01 / 1e6
	rows, sum, _ := Ledger(Month, Filter{})
	for _, r := range rows {
		if r.Model == "qwen/qwen3-embedding-8b" && (!r.Priced || math.Abs(r.Cost-emb) > 1e-12) {
			t.Errorf("the recorded embedding call: priced=%v cost=%v, want %v", r.Priced, r.Cost, emb)
		}
	}
	if math.Abs(sum.Cost-(chat+emb)) > 1e-12 {
		t.Errorf("ledger cost %v, want %v", sum.Cost, chat+emb)
	}
	if s := Summarize(Month); s.Unpriced != 0 || math.Abs(s.Cost-(chat+emb)) > 1e-12 {
		t.Errorf("after v2: cost %v unpriced %d, want %v and 0", s.Cost, s.Unpriced, chat+emb)
	}
}
