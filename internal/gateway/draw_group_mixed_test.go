package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// changqiu4890 on Discord: a routing group of chat models (three on a
// relay, one on DeepSeek) and one image model, the relay's gpt-image-2.5,
// and another relay that lists gpt-image-2.5 too. Asked to draw, the group
// draws on its own image member, on the relay it is pinned to: never on a
// chat member's images API, never on the other relay's model of that name.
func TestMixedGroupDrawsOnItsImageMemberOnly(t *testing.T) {
	s, _ := easeled(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	var mu sync.Mutex
	asked := map[string][]string{}
	vendor := func(id string) *httptest.Server {
		e := &easel{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			asked[id] = append(asked[id], r.URL.Path+" "+string(body))
			mu.Unlock()
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			e.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	cpa, other, ds := vendor("cpa"), vendor("other"), vendor("ds")
	for _, p := range []struct {
		p    provider.Provider
		live []catalog.Model
	}{
		// the other relay first, so a bare gpt-image-2.5 finds it first
		{provider.Provider{ID: "other", Name: "Other", Chat: other.URL + "/v1", Key: "key"}, []catalog.Model{{ID: "gpt-image-2.5", Draws: true}}},
		{provider.Provider{ID: "cpa", Name: "CPA", Chat: cpa.URL + "/v1", Key: "key", Models: []string{"gpt-6-astra", "gpt-6.1-sol"}}, []catalog.Model{{ID: "gpt-6-astra"}, {ID: "gpt-6.1-sol"}, {ID: "gpt-image-2.5", Draws: true}}},
		{provider.Provider{ID: "ds", Name: "DeepSeek", Chat: ds.URL + "/v1", Key: "key"}, []catalog.Model{{ID: "deepseek-v4.1-flash"}}},
	} {
		if err := provider.Save(p.p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.p.ID, p.p.Chat, p.live); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "smart-split", Name: "Smart split", Routing: "order", Members: []string{
		"cpa/gpt-6-astra", "cpa/gpt-6.1-sol", "ds/deepseek-v4.1-flash", "cpa/gpt-image-2.5",
	}}); err != nil {
		t.Fatal(err)
	}
	if !GroupDraws("group/smart-split") {
		t.Fatal("the group doesn't draw")
	}

	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"group/smart-split","prompt":"a magpie"}`)
	if code != 200 || a.Model != "cpa/gpt-image-2.5" {
		t.Fatalf("group/smart-split: %d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked["other"]) != 0 || len(asked["ds"]) != 0 {
		t.Fatalf("another provider was asked: other %v, ds %v", asked["other"], asked["ds"])
	}
	if len(asked["cpa"]) != 1 || !strings.Contains(asked["cpa"][0], `"model":"gpt-image-2.5"`) {
		t.Fatalf("the relay was asked %v, not its image model once", asked["cpa"])
	}
}
