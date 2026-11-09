package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// jzw690 on X: a key provider's live list has a chat model and
// gpt-image-2, and the user put gpt-image-2 in a routing group from the
// group editor, which offers each provider's image models beside the chat
// models. Picks are of chat models, so a provider with picks leaves the
// image model out of them; the group still draws on it, at
// /v1/images/generations with model group/<id> and as the Settings ›
// Images pick, the next member asked when one answers 5xx. Such a group
// is no model agents are offered for a conversation.
func TestImageGroupOfUnpickedImageModelsDraws(t *testing.T) {
	s, e := easeled(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	var brokeAsked atomic.Int32
	broke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brokeAsked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		io.WriteString(w, `{"error":{"message":"upstream error: bad gateway","type":"upstream_error"}}`)
	}))
	t.Cleanup(broke.Close)
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	live := []catalog.Model{{ID: "gpt-5.5"}, {ID: "gpt-image-2", Draws: true}}
	// relay: no picks; pix: its chat model picked, as most users have it
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Chat: broke.URL + "/v1", Key: "key"},
		{ID: "pix", Name: "Pix", Chat: up.URL + "/v1", Key: "key", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "images", Name: "Images", Members: []string{"relay/gpt-image-2", "pix/gpt-image-2"}, Routing: "order"},
		{ID: "pix-images", Name: "Pix images", Members: []string{"pix/gpt-image-2"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{"group/images", "group/pix-images"} {
		if !GroupDraws(ref) {
			t.Fatalf("%s doesn't draw", ref)
		}
	}
	for _, en := range provider.Catalog() {
		if en.ID == "group/pix-images" || strings.Contains(en.Model, "gpt-image") && en.Provider.ID == "pix" {
			t.Fatalf("an image model is offered to agents: %s (%s)", en.ID, en.Model)
		}
	}

	// named at the images API: the first member's 502, then pix draws
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"group/images","prompt":"a magpie"}`)
	if code != 200 || a.Model != "pix/gpt-image-2" || brokeAsked.Load() == 0 {
		t.Fatalf("group/images: %d %s (first member asked %d)", code, raw, brokeAsked.Load())
	}
	if got := e.got("/v1/images/generations"); len(got) != 1 || !strings.Contains(got[0], `"model":"gpt-image-2"`) {
		t.Fatalf("pix asked %v", got)
	}
	code, a, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"group/pix-images","prompt":"a magpie"}`)
	if code != 200 || a.Model != "pix/gpt-image-2" {
		t.Fatalf("group/pix-images: %d %s", code, raw)
	}

	// picked in Settings › Models › Images, it draws a request naming no model
	st := settings.Load()
	st.ImageGen = "group/pix-images"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if Drawer() != "group/pix-images" || DrawerMissing() != "" {
		t.Fatalf("the group pick isn't what draws: %q missing %q", Drawer(), DrawerMissing())
	}
	code, a, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 200 || a.Model != "pix/gpt-image-2" {
		t.Fatalf("Settings pick: %d %s", code, raw)
	}

	// gone from pix's list, it draws no more, and the pick is missing
	if err := catalog.SaveLive("pix", up.URL+"/v1", []catalog.Model{{ID: "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if GroupDraws("group/pix-images") || DrawerMissing() != "group/pix-images" {
		t.Fatalf("a group whose image model is gone still draws: missing %q", DrawerMissing())
	}
}
