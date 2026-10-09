package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Sorghum on Discord: a magpie with another computer's magpie as its
// provider counts the calls it makes there at what that magpie counts
// them at — the price its user set for the model by hand — so the two
// Usage pages give one cost. A price this magpie's user set for the model
// still wins, and a magpie from before the list told prices changes
// nothing here.
func TestRemoteMagpiePrices(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000000,"completion_tokens":500000}}`)
	}))
	t.Cleanup(up.Close)
	// a relay's model no catalog prices: only the price set for it by hand
	if err := provider.Save(provider.Provider{ID: "relay-a", Name: "Relay A", Key: "k", Models: []string{"house-model-1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay-a", up.URL+"/v1", []catalog.Model{{ID: "house-model-1", Name: "House Model"}}); err != nil {
		t.Fatal(err)
	}
	const model = "relay-a/house-model-1"
	if _, ok := provider.EffectivePrice("relay-a", "house-model-1"); ok {
		t.Fatal("the fixture's model has a list price; pick one no catalog prices")
	}
	set := catalog.Price{Input: 3, Output: 12, CacheRead: 0.3, CacheWrite: 3.75}
	if err := provider.SetModelPrice(model, &set); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	t.Cleanup(gw.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: strings.TrimPrefix(gw.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// the remote's hand-set price is its price here
	if pr, ok := provider.EffectivePrice("office", model); !ok || !pr.Same(set) {
		t.Fatalf("office/%s is priced %+v (%v), want the remote's %+v", model, pr, ok, set)
	}
	if !office.RemotePriced(model) {
		t.Error("the editor isn't told the list price is the remote's")
	}
	// a call through it costs the same on both Usage pages
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"office/`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body.String())
	}
	cost := usage.NewCoster()
	costs := map[string]float64{}
	for _, rec := range usage.Load(time.Time{}) {
		c, ok := cost(rec)
		if !ok {
			t.Errorf("%s/%s is unpriced", rec.Provider, rec.Model)
		}
		costs[rec.Provider] = c
	}
	if want := 3 + 12*0.5; costs["office"] != want || costs["relay-a"] != want {
		t.Errorf("costs here and there: %v, want %v each", costs, want)
	}

	// a price set here for it wins, and fetching the list again keeps it
	mine := catalog.Price{Input: 1, Output: 2}
	if err := provider.SetModelPrice("office/"+model, &mine); err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pr, _ := provider.EffectivePrice("office", model); !pr.Same(mine) {
		t.Errorf("the remote's price %+v won over the one set here %+v", pr, mine)
	}

	// a magpie from before the list told prices: the model stays unpriced
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"relay-a/house-model-1","object":"model","owned_by":"relay-a","display_name":"House Model","native_endpoints":["/v1/chat/completions"]}]}`)
	}))
	t.Cleanup(old.Close)
	oid, err := provider.Add(provider.Provider{ID: "attic", Name: "Attic", Preset: provider.RemoteMagpiePreset, Key: "k", Chat: old.URL})
	if err != nil {
		t.Fatal(err)
	}
	attic, _ := provider.Find(oid)
	if _, err := attic.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pr, ok := provider.EffectivePrice(oid, model); ok || attic.RemotePriced(model) {
		t.Errorf("an old remote's list priced it: %+v", pr)
	}
}

// What the list tells of prices, and to whom: only to a magpie that asks
// for them, and only from this computer or with a gateway key — never to
// another machine let in without one (MAGPIE_ADDR open to the network).
func TestRemoteMagpiePricesTold(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay-a", Name: "Relay A", Key: "k", Models: []string{"house-model-1"}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay-a", "http://127.0.0.1:9/v1", []catalog.Model{{ID: "house-model-1"}}); err != nil {
		t.Fatal(err)
	}
	set := catalog.Price{Input: 3, Output: 12}
	if err := provider.SetModelPrice("relay-a/house-model-1", &set); err != nil {
		t.Fatal(err)
	}
	priced := func(from, key string, ask bool) (int, bool) {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = from
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if ask {
			r.Header.Set(provider.PricesHeader, "1")
		}
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		var v struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		for _, m := range v.Data {
			if _, ok := m["magpie_price"]; ok {
				return w.Code, true
			}
		}
		return w.Code, false
	}
	t.Setenv("MAGPIE_ADDR", "")
	if code, got := priced("127.0.0.1:5000", "", true); code != 200 || !got {
		t.Errorf("this computer, asking: %d, priced %v", code, got)
	}
	if _, got := priced("127.0.0.1:5000", "", false); got {
		t.Error("prices told to a list that didn't ask (an older magpie, an agent)")
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:0") // open to anyone, magpie not shared
	if code, got := priced("192.168.1.9:5000", "", true); code != 200 || got {
		t.Errorf("another machine without a key: %d, priced %v", code, got)
	}
	t.Setenv("MAGPIE_ADDR", "")
	_, secrets := newCaller(t, "Laptop")
	if code, got := priced("192.168.1.9:5000", secrets[0], true); code != 200 || !got {
		t.Errorf("another machine with a gateway key: %d, priced %v", code, got)
	}
}
