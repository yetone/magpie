package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

func TestRemoteMagpieUnlistedDecisions(t *testing.T) {
	azureHome(t)
	p := normalize(Provider{ID: "office", Preset: RemoteMagpiePreset, Chat: "http://127.0.0.1:1/typesafe"})
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{
		{ID: "judge/custom", Decides: true},
		{ID: "judge/jev-preview"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolveDecideModel(p, "judge/custom"); !ok || got != "judge/custom" {
		t.Fatalf("listed decision: %q %v", got, ok)
	}
	for _, name := range []string{"judge/missing", "judge/jev-preview", "jev-latest", "jev-preview", "typesafe-ai/jev", "@cf/cloudflare/clef"} {
		if got, ok := resolveDecideModel(p, name); ok || got != "" {
			t.Errorf("unlisted decision %q resolved as %q", name, got)
		}
		if _, _, err := RouteDecider(p.ID + "/" + name); err == nil {
			t.Errorf("unlisted decision %q routed", name)
		}
	}
}

// Entry formatting for other decision providers keeps its earlier behavior;
// only a remote's entries inherit conversation model names and limits.
func TestRemoteMagpieDecidersKeepOtherEntries(t *testing.T) {
	azureHome(t)
	for _, p := range []Provider{
		{ID: "openrouter", Key: "k", Decide: "https://openrouter.ai/api/v1", Models: []string{OpenRouterJev}},
		{ID: "cloudflare", Key: "k", Decide: "https://api.cloudflare.com/client/v4/accounts/test/ai", Models: []string{CloudflareJev}},
		{ID: "vercel", Key: "k", Decide: "https://ai-gateway.vercel.sh/typesafe", Models: []string{"typesafe-ai/jev"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		s := settings.Load()
		if s.ModelNames == nil {
			s.ModelNames = map[string]string{}
		}
		s.ModelNames[p.ID+"/"+p.Models[0]] = "My judge"
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	entries := Deciders()
	if len(entries) != 3 {
		t.Fatalf("decision entries: %v", entries)
	}
	for _, e := range entries {
		if e.Name == "My judge" || e.Context != 0 || len(e.Efforts) != 0 {
			t.Errorf("non-remote entry gained conversation overrides: %+v", e)
		}
	}
}

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

func TestRemoteMagpieEndpointTests(t *testing.T) {
	for _, decisions := range []bool{false, true} {
		t.Run(fmt.Sprint(decisions), func(t *testing.T) {
			azureHome(t)
			var lists atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/models" {
					lists.Add(1)
					list := `{"data":[{"id":"lib/m1"}`
					if decisions {
						list += `,{"id":"judge/custom","kind":"decision"}`
					}
					io.WriteString(w, list+`]}`)
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"hello"}}]}`)
			}))
			t.Cleanup(up.Close)
			if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Chat: up.URL, Key: "remote-key"}); err != nil {
				t.Fatal(err)
			}
			p, _ := Find("office")
			var decide []Result
			for _, r := range p.Test(context.Background()) {
				if r.Protocol == "decide" {
					decide = append(decide, r)
				} else if !r.OK || r.Model != "lib/m1" {
					t.Errorf("conversation probe: %+v", r)
				}
			}
			if decisions {
				if len(decide) != 1 || !decide[0].OK || decide[0].Model != "judge/custom" {
					t.Errorf("listed decision probe: %+v", decide)
				}
			} else if len(decide) != 0 {
				t.Errorf("unlisted decision reported as tested: %+v", decide)
			}
			if n := lists.Load(); n != 1 {
				t.Errorf("model list fetched %d times; want once", n)
			}
		})
	}
}
