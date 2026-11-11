package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// dshSettled is a profile with magpie's route, and keepDshWired's rounds
// once the route is what magpie writes: the first round writes it, the
// second finds nothing to write.
func dshSettled(t *testing.T) (r *dshRounds, web string) {
	t.Helper()
	_, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	r = &dshRounds{}
	r.once()
	r.once()
	if !r.done {
		t.Fatal("a round with nothing to write is not kept")
	}
	if !dshListed(web, "deepseek/pro") || !dshListed(web, "deepseek/flash") {
		b, _ := os.ReadFile(web)
		t.Fatalf("the route is not what magpie writes:\n%s", b)
	}
	return r, web
}

// dshListed reports whether the route in a patch list lists model.
func dshListed(web, model string) bool {
	b, _ := os.ReadFile(web)
	return strings.Contains(string(b), "- id: "+model+"\n")
}

// A user's magpie allocated 35.7 GB in 48 minutes with ~157 MB live, its
// heap peaking near 885 MB; 12 GB of it was keepDshWired's round, every 30
// seconds building magpie's whole catalog and every route again to write
// nothing. A round with nothing changed since does nothing of that.
func TestDshRoundWithNothingChangedBuildsNothing(t *testing.T) {
	r, web := dshSettled(t)
	before, _ := os.ReadFile(web)
	few := testing.AllocsPerRun(10, func() { r.once() })
	if after, _ := os.ReadFile(web); string(after) != string(before) {
		t.Fatalf("a round with nothing changed wrote:\n%s", after)
	}
	// the bar is the same round over a few models, not a count: reading the
	// files allocates differently on each system (421 on Windows CI against
	// a fixed 300), while building the catalog grows with the models
	models := []string{"pro", "flash"}
	for i := range 400 {
		models = append(models, fmt.Sprintf("m%d", i))
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: models}); err != nil {
		t.Fatal(err)
	}
	r.once()
	r.once()
	if !r.done {
		t.Fatal("a round with nothing to write over 400 models is not kept")
	}
	many := testing.AllocsPerRun(10, func() { r.once() })
	if many > few*1.5+50 {
		t.Fatalf("a round with nothing changed made %.0f allocations over 402 models, %.0f over 2", many, few)
	}
}

// What a round goes by changing has the next round write the route again:
// a provider saved, a setting saved, the patch list written over by dsh,
// and the providers' file written by another magpie (the CLI).
func TestDshRoundAfterAChangeWritesAgain(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		r, web := dshSettled(t)
		if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
			t.Fatal(err)
		}
		r.once()
		if !dshListed(web, "deepseek/max") {
			t.Fatal("a model added to a provider is not in dsh's route after the next round")
		}
	})
	t.Run("setting", func(t *testing.T) {
		r, web := dshSettled(t)
		s := settings.Load()
		s.HiddenModels = map[string][]string{"dsh": {"deepseek/pro"}}
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		r.once()
		if dshListed(web, "deepseek/pro") {
			t.Fatal("a model hidden from dsh is still in its route after the next round")
		}
	})
	t.Run("patch list", func(t *testing.T) {
		r, web := dshSettled(t)
		os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
			dshRouteFixture("deepseek/pro")+
			"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
		r.once()
		if !dshListed(web, "deepseek/flash") {
			t.Fatal("a route dsh wrote over is not written again by the next round")
		}
	})
	t.Run("another magpie", func(t *testing.T) {
		r, web := dshSettled(t)
		b, err := os.ReadFile(provider.Path())
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(b), `"flash"`, `"flash", "max"`, 1)
		if s == string(b) {
			t.Fatalf("no model list in the providers' file:\n%s", b)
		}
		if err := os.WriteFile(provider.Path(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		r.once()
		if !dshListed(web, "deepseek/max") {
			t.Fatal("a provider another magpie saved is not in dsh's route after the next round")
		}
	})
	// what the stamp can't see (another app's account file, a date) is
	// written by a round once the models are dshModelsFor old
	t.Run("unseen", func(t *testing.T) {
		r, web := dshSettled(t)
		if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
			t.Fatal(err)
		}
		r.stamp = provider.StampNow() // as if the stamp hadn't moved
		r.once()
		if dshListed(web, "deepseek/max") {
			t.Fatal("a round with its stamp unmoved built the models again: the test sees no skip")
		}
		r.at = time.Now().Add(-dshModelsFor)
		r.once()
		if !dshListed(web, "deepseek/max") {
			t.Fatal("models dshModelsFor old are not built again")
		}
	})
}

// A round that writes builds the catalog once, though it asks for public
// and renamed refs of an auto start and for every route: outside a Hold
// each of them built every provider's models again (158 MB in a real
// account's first minute).
func TestDshRoundBuildsTheCatalogOnce(t *testing.T) {
	r, web := dshSettled(t)
	b, _ := os.ReadFile(web)
	start := strings.NewReplacer("model: deepseek/flash\n", "model: auto/nothing-listed\n", `model: "deepseek/flash"`, "model: auto/nothing-listed").Replace(string(b))
	if !strings.Contains(start, "auto/nothing-listed") {
		t.Fatalf("no start in the patch list:\n%s", b)
	}
	os.WriteFile(web, []byte(start), 0o644)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	before := provider.EntriesBuilt()
	r.once()
	if !dshListed(web, "deepseek/max") {
		t.Fatal("the round wrote no route")
	}
	t.Logf("%d catalog builds", provider.EntriesBuilt()-before)
	if n := provider.EntriesBuilt() - before; n > 1 {
		t.Fatalf("a round built the catalog %d times", n)
	}
}
