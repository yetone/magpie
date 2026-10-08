package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestVolcengineArkPlans(t *testing.T) {
	p, err := FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	// a Coding Plan's own endpoints by default: its quota isn't spent at
	// Ark's pay-as-you-go /api/v3
	if p.Chat != "https://ark.cn-beijing.volces.com/api/coding/v3" || p.Responses != p.Chat || p.Anthropic != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	pr := Preset("volcengine")
	if len(pr.Regions) != 3 || pr.Regions[0].Chat != pr.Chat || pr.Regions[0].Anthropic != pr.Anthropic ||
		pr.Regions[1].ID != "agent" || pr.Regions[1].Chat != "https://ark.cn-beijing.volces.com/api/plan/v3" || pr.Regions[1].Anthropic != "https://ark.cn-beijing.volces.com/api/plan" ||
		pr.Regions[2].Chat != "https://ark.cn-beijing.volces.com/api/v3" || pr.Regions[2].Anthropic != "" {
		t.Fatalf("plans: %+v", pr.Regions)
	}
	coding, agent, payg := pr.Regions[0], pr.Regions[1], pr.Regions[2]
	if pr.Models != nil || coding.Models == nil || agent.Models == nil || payg.Models != nil {
		t.Fatalf("model sources aren't defined per plan: coding %v, agent %v, pay as you go %v", coding.Models, agent.Models, payg.Models)
	}
	if len(coding.Embeddings) == 0 || len(agent.Embeddings) == 0 || len(payg.Embeddings) != 0 {
		t.Fatalf("embedding models by plan: coding %v, agent %v, pay as you go %v", coding.Embeddings, agent.Embeddings, payg.Embeddings)
	}
	if len(coding.Drawers) != 0 || len(coding.Videos) != 0 || len(agent.Drawers) == 0 || len(agent.Videos) == 0 || len(payg.Drawers) != 0 || len(payg.Videos) != 0 {
		t.Fatalf("media models aren't confined to Agent Plan: coding %+v, agent %+v, pay as you go %+v", coding, agent, payg)
	}
	for _, models := range [][]string{coding.Models, coding.Embeddings, agent.Models, agent.Embeddings, agent.Drawers, agent.Videos} {
		for _, id := range models {
			if id != strings.ToLower(id) {
				t.Fatalf("model ids are lowercase, as the quick-start page has them: %q", id)
			}
		}
	}
	if got := modelIDs(p.planModels(nil)); !slices.Equal(got, coding.Models) {
		t.Fatalf("Coding Plan models: %v, want preset region's %v", got, coding.Models)
	}
	if got := modelIDs(p.PlanEmbeddings()); !slices.Equal(got, coding.Embeddings) {
		t.Fatalf("Coding Plan embeddings: %v, want preset region's %v", got, coding.Embeddings)
	}
	for _, id := range coding.Embeddings {
		if slices.Contains(modelIDs(p.Available()), id) || slices.Contains(modelIDs(p.Exposed()), id) {
			t.Fatalf("embedding model %q exposed as a chat model", id)
		}
	}
	p.atRegionOf(pr, agent)
	if got := modelIDs(p.planModels(nil)); !slices.Equal(got, agent.Models) {
		t.Fatalf("Agent Plan models: %v, want preset region's %v", got, agent.Models)
	}
	if got := modelIDs(p.PlanEmbeddings()); !slices.Equal(got, agent.Embeddings) {
		t.Fatalf("Agent Plan embeddings: %v, want preset region's %v", got, agent.Embeddings)
	}
	if got := modelIDs(p.PlanDrawers()); !slices.Equal(got, agent.Drawers) {
		t.Fatalf("Agent Plan drawers: %v, want preset region's %v", got, agent.Drawers)
	}
	if got := modelIDs(p.PlanVideos()); !slices.Equal(got, agent.Videos) {
		t.Fatalf("Agent Plan videos: %v, want preset region's %v", got, agent.Videos)
	}
	p.atRegionOf(pr, pr.Regions[2])
	if got := p.planModels(nil); len(got) != 0 {
		t.Fatalf("pay as you go preset models: %+v", got)
	}
	// an entry imported from another app at an Ark endpoint is known as Ark
	im, _ := imported("Ark", "k", endpoints{chat: "https://ark.cn-beijing.volces.com/api/plan/v3"}, nil)
	if im.Icon != "volcengine-color" || im.Chat != "https://ark.cn-beijing.volces.com/api/plan/v3" {
		t.Fatalf("imported: %+v", im)
	}
}

func TestVolcengineModelLists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		// Only pay-as-you-go and an explicit list URL may reach this server.
		w.Write([]byte(`{"data":[{"id":"doubao-seed-2-0-pro-260215"}]}`))
	}))
	t.Cleanup(srv.Close)
	pr := Preset("volcengine")
	for _, r := range pr.Regions {
		for _, messagesOnly := range []bool{false, true} {
			if messagesOnly && r.Anthropic == "" {
				continue
			}
			name := r.ID
			if messagesOnly {
				name += "-messages"
			}
			t.Run(name, func(t *testing.T) {
				p, err := FromPreset("volcengine")
				if err != nil {
					t.Fatal(err)
				}
				p.ID, p.Key = "ark-"+name, "test-key"
				p.Chat, p.Responses = srv.URL+basePath(r.Chat), srv.URL+basePath(r.Responses)
				p.Anthropic = ""
				if r.Anthropic != "" {
					p.Anthropic = srv.URL + basePath(r.Anthropic)
				}
				if messagesOnly {
					p.Chat, p.Responses = "", ""
				}
				listed := []catalog.Model{{ID: "doubao-seed-2-0-pro-260215"}}
				if r.ID != "api" {
					listed = append(listed, catalog.Model{ID: "ark-code-latest", APIs: []string{"anthropic"}, Keys: []string{keyID("other-key")}})
				}
				if err := catalog.SaveLive(p.ID, p.Chat, listed); err != nil {
					t.Fatal(err)
				}
				want := r.Models
				if r.ID == "api" {
					want = []string{"doubao-seed-2-0-pro-260215"}
				}
				check := func(where string, ms []catalog.Model, err error) {
					t.Helper()
					var ids []string
					for _, m := range ms {
						ids = append(ids, m.ID)
					}
					if err != nil || !slices.Equal(ids, want) {
						t.Errorf("%s: models %v, want %v, err %v", where, ids, want, err)
					}
				}
				check("available with cached list", p.Available(), nil)
				check("exposed with cached list", p.Exposed(), nil)
				if _, ok := p.Fetched(); ok != (r.ID == "api") {
					t.Errorf("fetched=%v for %s", ok, r.ID)
				}
				if r.ID != "api" {
					if apis := p.ListedAPIs("ark-code-latest"); len(apis) != 0 {
						t.Errorf("protocols from the unsupported list: %v", apis)
					}
					if !p.Serves(KeyAccount{Key: p.Key}, "ark-code-latest") {
						t.Error("key refused by the unsupported list")
					}
				}
				before := asked.Load()
				ms, err := p.List(context.Background())
				check("list", ms, err)
				ms, err = p.Fetch(context.Background())
				check("fetch", ms, err)
				check("available after refresh", p.Available(), nil)
				if n := asked.Load() - before; r.ID != "api" && n != 0 || r.ID == "api" && n != 2 {
					t.Errorf("made %d model requests for %s", n, r.ID)
				}
				// An explicit model-list URL remains the user's source.
				p.ModelsURL = srv.URL + "/custom/models"
				want = []string{"doubao-seed-2-0-pro-260215"}
				ms, err = p.Fetch(context.Background())
				check("explicit list URL", ms, err)
				check("available from explicit list URL", p.Available(), nil)
			})
		}
	}
}
