package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestRemoteMagpieDecisionRefresh(t *testing.T) {
	azureHome(t)
	var mu sync.Mutex
	list := `{"data":[{"id":"relay/typesafe/jev-router"},{"id":"judge/custom-image-decision","kind":"decision","context_length":65536,"modalities":{"input":["text","image"]}}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get(DecidersHeader) != "1" || r.Header.Get(DrawersHeader) != "1" || r.Header.Get(VideomakersHeader) != "1" || r.Header.Get("Authorization") != "Bearer remote-key" {
			t.Errorf("remote discovery: %s %v", r.URL.Path, r.Header)
		}
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, list)
	}))
	t.Cleanup(up.Close)
	if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "remote-key", Chat: up.URL, Models: []string{"relay/typesafe/jev-router"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("office")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.DecidesModel("judge/custom-image-decision") || p.DecidesModel("relay/typesafe/jev-router") || p.DecidesModel("jev-latest") {
		t.Fatal("remote decisions must come from the list's explicit kind")
	}
	if ms := p.DecisionModels(); len(ms) != 1 || !ms[0].Decides || ms[0].Context != 65536 || !ms[0].Images {
		t.Fatalf("decision facts lost: %v", ms)
	}
	if ds := Deciders(); len(ds) != 1 || ds[0].ID != "office/judge/custom-image-decision" || ds[0].Context != 65536 || !ds[0].Images {
		t.Fatalf("conversation picks hid decision models: %v", ds)
	}
	if _, model, err := RouteDecider("office"); err != nil || model != "judge/custom-image-decision" {
		t.Fatalf("default remote decision: %s %v", model, err)
	}
	if len(catalog.LiveDrawers("office")) != 0 {
		t.Fatal("a decision with image in its name became an image generator")
	}
	mu.Lock()
	list = `{"data":[{"id":"relay/typesafe/jev-router"}]}`
	mu.Unlock()
	if _, _, err := p.Refetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.DecidesModel("judge/custom-image-decision") || len(Deciders()) != 0 {
		t.Fatal("removed remote decisions survived refresh")
	}
	for _, id := range []string{"office", "office/judge/custom-image-decision", "office/jev-latest"} {
		if _, _, err := RouteDecider(id); err == nil {
			t.Errorf("unlisted remote decision accepted: %s", id)
		}
	}
	if e, ok := EntryOf("office/relay/typesafe/jev-router"); !ok || e.Model != "relay/typesafe/jev-router" {
		t.Fatal("Jev Router chat model was removed")
	}
	// Older peers with no decision metadata still serve their chat list.
	live, _, _ := catalog.Live("office")
	if len(live) != 1 || !strings.Contains(live[0].ID, "jev-router") {
		t.Fatalf("old list: %v", live)
	}
	// All URL fields can be the entry point; the remote itself always
	// speaks System One even behind paths resembling a vendor's API.
	for _, p := range []Provider{
		{Preset: RemoteMagpiePreset, Responses: up.URL + "/v1"},
		{Preset: RemoteMagpiePreset, Anthropic: up.URL},
		{Preset: RemoteMagpiePreset, Decide: up.URL + "/v1/systemone"},
		{Preset: RemoteMagpiePreset, Chat: up.URL + "/typesafe"},
	} {
		p = normalize(p)
		u, err := p.DecideURL(context.Background())
		if err != nil || u != p.Chat+"/systemone" || p.DecideVia() != ViaSystemOne || !slices.Contains(p.Speaks(), Chat) {
			t.Errorf("remote endpoints: %+v %s %v", p, u, err)
		}
	}
}
