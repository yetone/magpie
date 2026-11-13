package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// @czhhcz on X: a relay's /v1/models listed a Claude model with
// reasoning_levels: [], `magpie model efforts` showed five levels for it
// with nothing saying where they came from, and setting all five printed a
// tick while nothing was saved. The listing says the levels are the
// model's own from models.dev, the relay's list naming none, and a set
// that changes nothing says so.
func TestModelEffortsOfARelayThatListsNone(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{
	  "claude-opus-5-5":{"id":"claude-opus-5-5","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"claude-opus-5-5","object":"model","reasoning_levels":[],"supported_reasoning_levels":[]},{"id":"m","object":"model"}]}`))
	}))
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := []string{"low", "medium", "high", "xhigh", "max"}
	if p := mustFind(t, "relay"); !slices.Equal(p.Efforts("claude-opus-5-5"), all) || !p.KnownBorrowed("claude-opus-5-5") || p.KnownBorrowed("m") {
		t.Fatal(p.Efforts("claude-opus-5-5"), p.KnownBorrowed("claude-opus-5-5"), p.KnownBorrowed("m"))
	}
	ref := "relay/claude-opus-5-5"
	out := saidArgs(t, "efforts", ref)
	if !strings.Contains(out, "low medium high xhigh max") || !strings.Contains(out, "Relay's list names no reasoning levels for claude-opus-5-5") {
		t.Fatalf("listing:\n%s", out)
	}
	// every level it has: nothing to save, and said so, not a tick
	out = saidArgs(t, "efforts", ref, "low,medium,high,xhigh,max")
	if strings.Contains(out, "✓") || !strings.Contains(out, "nothing changed") || !strings.Contains(out, "Relay's list names no reasoning levels") {
		t.Fatalf("all of them again:\n%s", out)
	}
	if es := settings.Load().ModelEfforts; len(es) != 0 {
		t.Fatal(es)
	}
	// and through magpie model spec, which said "✓ saved" over it
	out = saidArgs(t, "spec", ref, "--reasoning", "low-max")
	if strings.Contains(out, "✓") || !strings.Contains(out, "nothing changed") {
		t.Fatalf("spec, all of them again:\n%s", out)
	}
	if out = saidArgs(t, "spec", ref, "--reasoning", "high-max"); !strings.Contains(out, "✓ saved") {
		t.Fatalf("spec, high-max:\n%s", out)
	}
	if out = saidArgs(t, "efforts", ref, "--reset"); !strings.Contains(out, "✓") {
		t.Fatalf("reset:\n%s", out)
	}
	// one of them is saved, and saying it again changes nothing
	if out = saidArgs(t, "efforts", ref, "max"); !strings.Contains(out, "✓") {
		t.Fatalf("max:\n%s", out)
	}
	if es := settings.Load().ModelEfforts[ref]; !slices.Equal(es, []string{"max"}) {
		t.Fatal(settings.Load().ModelEfforts)
	}
	if out = saidArgs(t, "efforts", ref, "max"); strings.Contains(out, "✓") || !strings.Contains(out, "already offers only max") {
		t.Fatalf("max again:\n%s", out)
	}
	// a model whose list names none and models.dev doesn't know: given
	if out = saidArgs(t, "efforts", "relay/m"); strings.Contains(out, "list names no reasoning levels") {
		t.Fatalf("unknown model:\n%s", out)
	}
	if out = saidArgs(t, "efforts", "relay/m", "--reset"); strings.Contains(out, "✓") || !strings.Contains(out, "nothing changed") {
		t.Fatalf("reset of nothing:\n%s", out)
	}
}
