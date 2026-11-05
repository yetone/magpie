package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Two magpies each the other's remote (cdredfox on Discord, 100-200% CPU
// until the remote was off) listed each other's models back, one level
// more on every 10-minute fetch, without end: rem/back/own/m1, then
// rem/back/rem/back/own/m1, each another member of the found auto/ group
// and another trip round the two, masked again on each. The list magpie
// gives another magpie now says which magpies each model passes, this one
// last, a routing group the magpies its members pass, and a model of a
// remote that went out of here comes back no more.
func TestRemoteMagpieListLoopsNoMore(t *testing.T) {
	self := catalog.Self()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(up.Close)
	// the other magpie: what it serves of its own, and this magpie's own
	// m1, which it has through this one as its remote
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[
		  {"id":"up/m1","object":"model","magpie_via":["peer"]},
		  {"id":"back/own/m1","object":"model","magpie_via":["`+self+`","peer"]}]}`)
	}))
	t.Cleanup(peer.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "own", Name: "Own", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("own", up.URL+"/v1", []catalog.Model{{ID: "m1"}}); err != nil {
		t.Fatal(err)
	}
	id, err := provider.Add(provider.Provider{ID: "rem", Name: "Rem", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(peer.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := provider.Find(id)
	ms, err := rem.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if slices.Contains(ids, "back/own/m1") || !slices.Contains(ids, "up/m1") {
		t.Fatalf("the remote's list taken = %v; want up/m1 and not this magpie's own model back", ids)
	}

	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set(provider.DrawersHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID  string   `json:"id"`
			Via []string `json:"magpie_via"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	via := map[string][]string{}
	for _, m := range list.Data {
		via[m.ID] = m.Via
	}
	if v, ok := via["own/m1"]; !ok || !slices.Equal(v, []string{self}) {
		t.Errorf("own/m1 is listed through %v, %v; want this magpie (%s) alone; list %v", v, ok, self, via)
	}
	if v, ok := via["rem/up/m1"]; !ok || !slices.Equal(v, []string{"peer", self}) {
		t.Errorf("rem/up/m1 is listed through %v, %v; want [peer %s]; list %v", v, ok, self, via)
	}
	// the auto/ group of m1 here has the remote's m1 in it, so it reaches
	// the other magpie too: listed back to it, it is left out there
	if v, ok := via["auto/m1"]; !ok || !slices.Equal(v, []string{"peer", self}) {
		t.Errorf("auto/m1 is listed through %v, %v; want [peer %s], its members' magpies and this one; list %v", v, ok, self, via)
	}
	if _, ok := via["rem/back/own/m1"]; ok {
		t.Errorf("this magpie's own model came back through the remote: %v", via)
	}
}

// A request that went round magpies each the other's remote (or one that
// is its own) is stopped once it has passed catalog.MaxVia of them, even
// where their lists came from magpies too old to say: each magpie tells
// the next how many it passed through (HopsHeader), and no vendor is
// asked.
func TestRemoteMagpieRequestLoopStops(t *testing.T) {
	var vendor int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendor++
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(up.Close)
	var hops []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"object":"list","data":[{"id":"up/m1","object":"model"}]}`)
			return
		}
		hops = append(hops, r.Header.Get(HopsHeader))
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(peer.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "own", Name: "Own", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("own", up.URL+"/v1", []catalog.Model{{ID: "m1"}}); err != nil {
		t.Fatal(err)
	}
	id, err := provider.Add(provider.Provider{ID: "rem", Name: "Rem", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(peer.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := provider.Find(id)
	if _, err := rem.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	ask := func(model, ua, hop string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		if hop != "" {
			req.Header.Set(HopsHeader, hop)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// an agent's request goes on as the first pass, another magpie's as
	// one more than it came with
	if code := ask("rem/up/m1", "curl/8", ""); code != http.StatusOK {
		t.Fatalf("an agent's request to the remote: %d", code)
	}
	if code := ask("rem/up/m1", "magpie/test", "3"); code != http.StatusOK {
		t.Fatalf("another magpie's request to the remote: %d", code)
	}
	if !slices.Equal(hops, []string{"1", "4"}) {
		t.Errorf("the remote was told %v passes; want [1 4]", hops)
	}
	// one that has gone round as many as a list may name is stopped here
	before := vendor
	if code := ask("own/m1", "magpie/test", strconv.Itoa(catalog.MaxVia)); code != http.StatusLoopDetected {
		t.Errorf("a request passed on through %d magpies: %d, want 508", catalog.MaxVia, code)
	}
	if vendor != before {
		t.Errorf("the vendor was asked a request that went round in a loop")
	}
	// only a magpie's count is read: an agent can't stop its own request
	if code := ask("own/m1", "curl/8", "99"); code != http.StatusOK {
		t.Errorf("an agent's request with a hops header of its own: %d, want 200", code)
	}
}
