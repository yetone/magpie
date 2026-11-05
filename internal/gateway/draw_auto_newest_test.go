package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// StarBits on X: Automatic draws with the newest GPT Image a key sets up.
// An OpenAI key's list names gpt-image-2.5 and 2.6 with no price, while
// models.dev prices gpt-image-2 and older; by cost alone Automatic took the
// cheapest old one.
func TestAutoDrawerTakesNewestGPTImage(t *testing.T) {
	fresh(t)
	priced := func(in, out float64) *catalog.Price { return &catalog.Price{Input: in, Output: out} }
	p := provider.Provider{ID: "oa", Name: "OpenAI", Chat: "http://127.0.0.1:9/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	save := func(ms ...catalog.Model) {
		t.Helper()
		for i := range ms {
			ms[i].Draws = true
		}
		if err := catalog.SaveLive("oa", p.Chat, ms); err != nil {
			t.Fatal(err)
		}
	}
	save(
		catalog.Model{ID: "dall-e-3", Price: priced(1, 1)},
		catalog.Model{ID: "gpt-image-1-mini", Price: priced(2, 8), Released: "2025-09-26"},
		catalog.Model{ID: "chatgpt-image-latest", Released: "2025-12-16"},
		catalog.Model{ID: "gpt-image-2", Price: priced(5, 30), Released: "2026-04-21"},
		catalog.Model{ID: "gpt-image-2.5-flare"},
		catalog.Model{ID: "gpt-image-2.6-flare"},
		catalog.Model{ID: "gpt-image-2.6-sunburst"},
		catalog.Model{ID: "gpt-image-2.5-sunburst-2026-09-08"},
	)
	if a := AutoDrawer(); a != "oa/gpt-image-2.6-sunburst" {
		t.Fatalf("automatic draws with %q, want oa/gpt-image-2.6-sunburst", a)
	}

	// unticked in its editor, the newest left is taken
	p.HiddenDrawers = []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if a := AutoDrawer(); a != "oa/gpt-image-2.5-sunburst-2026-09-08" {
		t.Fatalf("with 2.6 unticked automatic draws with %q", a)
	}
	p.HiddenDrawers = nil
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	// another kind of model, cheaper, is still the cheaper choice
	if err := provider.Save(provider.Provider{ID: "fx", Name: "Flux", Chat: "http://127.0.0.1:9/v1", Key: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("fx", "http://127.0.0.1:9/v1", []catalog.Model{{ID: "flux-schnell", Draws: true, Price: priced(1, 2)}}); err != nil {
		t.Fatal(err)
	}
	if a := AutoDrawer(); a != "fx/flux-schnell" {
		t.Fatalf("automatic draws with %q, want the cheaper fx/flux-schnell", a)
	}
}

// A ChatGPT account's one GPT Image is named gpt-image-2, as Codex CLI asks
// for it, but its backend picks the model itself (#1865): a key's newer GPT
// Image doesn't supersede it, and the plan's images stay Automatic's choice.
func TestAutoDrawerKeepsChatGPTsOwnPick(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "oa", Name: "OpenAI", Chat: "http://127.0.0.1:9/v1", Key: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("oa", "http://127.0.0.1:9/v1", []catalog.Model{{ID: "gpt-image-2.6-sunburst", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	if a := AutoDrawer(); a != "codex/"+codexDrawer {
		t.Fatalf("automatic draws with %q, want the ChatGPT plan's", a)
	}
}

func TestGPTImageGenOrder(t *testing.T) {
	order := []string{"dall-e-3", "gpt-image-1-mini", "gpt-image-1", "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "openai/gpt-image-2.5-sunburst", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst-2026-10-01"}
	for i := 1; i < len(order); i++ {
		a, ok1 := gptImageGenOf(order[i-1])
		b, ok2 := gptImageGenOf(order[i])
		if !ok1 || !ok2 || !a.less(b) {
			t.Errorf("%s (%v) should come before %s (%v)", order[i-1], a, order[i], b)
		}
	}
	for _, id := range []string{"flux-schnell", "grok-imagine-image", "gemini-3.1-flash-image"} {
		if _, ok := gptImageGenOf(id); ok {
			t.Errorf("%s taken for a GPT Image", id)
		}
	}
}
