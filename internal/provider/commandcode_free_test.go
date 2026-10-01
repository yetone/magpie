package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Command Code's CLI marks Space Bunny Alpha FREE, as it does Ling 3.1
// Flash (01huadalang on Discord): magpie's chips showed 免费 on Ling only,
// whose id says :free, as Command Code's list says nothing of it. The
// models the CLI calls free are marked so, on Go's list and on a keyed
// plan's, before a fetch and after.
func TestCommandCodeFreeModels(t *testing.T) {
	list, err := os.ReadFile(filepath.Join("testdata", "commandcode_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"individual-go-monthly", "individual-max-monthly"} {
		t.Run(plan, func(t *testing.T) {
			home := signIn(t)
			writeFile(t, filepath.Join(home, ".commandcode", "auth.json"), map[string]any{"apiKey": "cc-key", "userName": "ccuser"})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/provider/v1/models":
					_, _ = w.Write(list)
				case "/alpha/billing/subscriptions":
					_, _ = w.Write([]byte(`{"success":true,"data":{"planId":"` + plan + `","status":"active"}}`))
				default:
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			oldAPI := cmdAPI
			cmdAPI = srv.URL
			defer func() { cmdAPI = oldAPI }()
			cmdPlansSeen.Lock()
			cmdPlansSeen.m = map[string]cmdSeen{}
			cmdPlansSeen.Unlock()

			p, found := find(All(), CommandCodePlanID)
			if !found {
				t.Fatal("no Command Code account")
			}
			CommandCodeGenerate(context.Background(), p) // the plan read
			if plan == "individual-go-monthly" {
				p, _ = find(All(), CommandCodePlanID)
				free := map[string]bool{}
				for _, m := range p.Available() {
					free[m.ID] = m.Free
				}
				if !free["stealth/space-bunny-alpha"] || !free["inclusionai/ling-3.1-flash:free"] {
					t.Errorf("before a fetch: Space Bunny Alpha free %v, Ling 3.1 Flash free %v", free["stealth/space-bunny-alpha"], free["inclusionai/ling-3.1-flash:free"])
				}
			}
			if _, err := p.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			p, _ = find(All(), CommandCodePlanID)
			free, seen := map[string]bool{}, map[string]bool{}
			for _, m := range p.Available() {
				free[m.ID], seen[m.ID] = m.Free, true
			}
			for _, id := range []string{"stealth/space-bunny-alpha", "inclusionai/ling-3.1-flash:free", "poolside/laguna-s-2.1-free"} {
				if !seen[id] {
					t.Errorf("%s not listed", id)
				} else if !free[id] {
					t.Errorf("%s not marked free", id)
				}
			}
			for _, id := range []string{"deepseek/deepseek-v4-flash", "thinkingmachines/inkling"} {
				if free[id] {
					t.Errorf("%s marked free", id)
				}
			}
		})
	}
}
