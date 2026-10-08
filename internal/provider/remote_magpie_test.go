package provider

import (
	"fmt"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Another magpie is added by its address however it was typed, and
// speaks every API there: Chat and Responses at /v1, Messages at the root.
// Its whole list is offered, being already what its user exposed.
func TestRemoteMagpie(t *testing.T) {
	azureHome(t)
	for _, in := range []string{
		"192.168.1.20:3425",
		"http://192.168.1.20:3425/",
		"http://192.168.1.20:3425/v1",
		"http://192.168.1.20:3425/v1/messages",
		"http://192.168.1.20:3425/v1/systemone",
		"http://192.168.1.20:3425/v1/embeddings",
		"http://192.168.1.20:3425/v1/rerank",
		"http://192.168.1.20:3425/v1/images/generations",
		"http://192.168.1.20:3425/v1/images/edits",
		"http://192.168.1.20:3425/v1/videos",
		"http://192.168.1.20:3425/v1/messages/count_tokens",
	} {
		p := normalize(Provider{Preset: RemoteMagpiePreset, Chat: in})
		if p.Chat != "http://192.168.1.20:3425/v1" || p.Responses != p.Chat || p.Anthropic != "http://192.168.1.20:3425" || p.Decide != p.Chat {
			t.Errorf("%q: %q %q %q", in, p.Chat, p.Responses, p.Anthropic)
		}
	}
	// a custom provider's URL is left as it was
	if p := normalize(Provider{Chat: "192.168.1.20:3425/v1"}); p.Anthropic != "" || p.Chat != "https://192.168.1.20:3425/v1" {
		t.Errorf("custom: %+v", p)
	}

	if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-x", Chat: "http://192.168.1.20:3425"}); err != nil {
		t.Fatal(err)
	}
	var ms []catalog.Model
	for i := range manyModels + 6 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("p/m%d", i)})
	}
	if err := catalog.SaveLive("office", "http://192.168.1.20:3425/v1", ms); err != nil {
		t.Fatal(err)
	}
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Exposed()); n != len(ms) {
		t.Errorf("exposed %d of %d", n, len(ms))
	}
	if !p.Ready() || p.Icon != "magpie" {
		t.Errorf("%+v", p)
	}
}
