package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A Remote magpie that is this magpie (hanslin1666 on Discord: B uses A
// through one, and sync carried it to A, where it points at A) is left
// out: its models are not listed beside this magpie's own, a found group
// doesn't take them, and a request naming one (an agent's pick synced
// from B) is served here at once, not passed through the gateway again.
// "Use it anyway" (KeepSelf) has it back.
func TestRemoteMagpieThatIsThisOneIsLeftOut(t *testing.T) {
	provider.LeaveOutSelf = true
	t.Cleanup(func() { provider.LeaveOutSelf = false })
	var vendor atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}
		vendor.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "own", Name: "Own", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("own", up.URL+"/v1", []catalog.Model{{ID: "m1"}}); err != nil {
		t.Fatal(err)
	}
	var passes atomic.Int32 // chat requests this gateway was asked
	h := New().Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			passes.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	id, err := provider.Add(provider.Provider{ID: "rem", Name: "Rem", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := provider.Find(id)
	if _, err := rem.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	rem, _ = provider.Find(id)
	if !rem.SelfAnswered() || !rem.SelfLeftOut() || rem.On() {
		t.Fatalf("a Remote magpie at this magpie's own address: answered as self %v, left out %v, on %v; want true, true, false", rem.SelfAnswered(), rem.SelfLeftOut(), rem.On())
	}

	listed := func() map[string]bool {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range l.Data {
			out[m.ID] = true
		}
		return out
	}
	if l := listed(); l["rem/own/m1"] || !l["own/m1"] {
		t.Errorf("listed %v; want own/m1 and not rem/own/m1", l)
	}
	for _, e := range provider.Catalog() {
		if e.Provider.ID == "rem" {
			t.Errorf("the catalog has %s through this magpie's own Remote magpie", e.ID)
		}
	}

	ask := func(model string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "curl/8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := ask("rem/own/m1"); code != http.StatusOK {
		t.Fatalf("a request for rem/own/m1: %d, want 200", code)
	}
	if p, v := passes.Load(), vendor.Load(); p != 1 || v != 1 {
		t.Errorf("rem/own/m1 took %d passes through the gateway and %d vendor requests; want 1 and 1", p, v)
	}

	// kept by the user: through the remote as before
	if err := provider.SetKeepSelf(id, true); err != nil {
		t.Fatal(err)
	}
	rem, _ = provider.Find(id)
	if !rem.SelfAnswered() || rem.SelfLeftOut() || !rem.On() {
		t.Fatalf("kept: answered as self %v, left out %v, on %v; want true, false, true", rem.SelfAnswered(), rem.SelfLeftOut(), rem.On())
	}
	if l := listed(); !l["rem/own/m1"] {
		t.Errorf("kept, listed %v; want rem/own/m1 again", l)
	}
	passes.Store(0)
	if code := ask("rem/own/m1"); code != http.StatusOK || passes.Load() != 2 {
		t.Errorf("kept, rem/own/m1: %d in %d passes; want 200 in 2", code, passes.Load())
	}
}
