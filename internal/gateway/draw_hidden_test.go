package gateway

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// 321sssrt on X: an image model unticked in its provider's editor
// (HiddenDrawers) leaves what lists image models to pick from, and
// Automatic, but still draws when named or picked in Settings › Models › Images.
func TestHiddenDrawerLeavesListsButStillDraws(t *testing.T) {
	fresh(t)
	drawingRelay(t, "hd", "gpt-image-hidden", "gpt-image-shown")
	found, err := provider.Find("hd")
	if err != nil {
		t.Fatal(err)
	}
	p := *found
	p.HiddenDrawers = []string{"gpt-image-hidden"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	found, _ = provider.Find("hd")
	p = *found
	ids := func(ms []catalog.Model) (out []string) {
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return
	}
	if got := ids(OfferedDrawers(p)); !slices.Equal(got, []string{"gpt-image-shown"}) {
		t.Fatalf("offered %v", got)
	}
	if got := ids(Drawers(p)); !slices.Contains(got, "gpt-image-hidden") {
		t.Fatalf("drawers lost the hidden one: %v", got)
	}
	if !DrawerHidden("hd/gpt-image-hidden") || DrawerHidden("hd/gpt-image-shown") || DrawerHidden("nope/gpt-image-hidden") {
		t.Fatal("DrawerHidden")
	}

	var listed []string
	for _, o := range drawerObjects(httptest.NewRequest(http.MethodGet, "/v1/models", nil)) {
		listed = append(listed, o["id"].(string))
	}
	if slices.Contains(listed, "hd/gpt-image-hidden") || !slices.Contains(listed, "hd/gpt-image-shown") {
		t.Fatalf("drawer objects %v", listed)
	}

	// Automatic never lands on the hidden one, whichever order it reads
	for range 3 {
		if a := AutoDrawer(); a == "hd/gpt-image-hidden" {
			t.Fatalf("automatic picked the hidden model")
		}
	}
	p.HiddenDrawers = []string{"gpt-image-hidden", "gpt-image-shown"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if a := AutoDrawer(); a != "" {
		t.Fatalf("automatic with every model hidden: %q", a)
	}

	// named, or picked in Settings, it still draws
	if _, id, ok := resolveDrawing("hd/gpt-image-hidden"); !ok || id != "gpt-image-hidden" {
		t.Fatalf("resolve named hidden: %q %v", id, ok)
	}
	if _, id, ok := resolveDrawing("gpt-image-hidden"); !ok || id != "gpt-image-hidden" {
		t.Fatalf("resolve bare hidden: %q %v", id, ok)
	}
	st := settings.Load()
	st.ImageGen = "hd/gpt-image-hidden"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if Drawer() != "hd/gpt-image-hidden" || DrawerMissing() != "" {
		t.Fatalf("a hidden pick isn't what draws: %q missing %q", Drawer(), DrawerMissing())
	}
	s := New()
	drawingMainTurn(t, s, "hd/text", "session", "turn", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != "hd/gpt-image-hidden" {
		t.Fatalf("settings pick hidden: %d %s", code, raw)
	}
}
