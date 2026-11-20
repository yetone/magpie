package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// sub2apiNoDispatch is sub2api's 403 to /v1/messages for a key whose group
// isn't let dispatch Messages, as its handler writes it: every Claude Code
// request of a Sidrune AI China-region key (Axo, 11-20).
const sub2apiNoDispatch = `{"type":"error","error":{"type":"permission_error","message":"This group does not allow /v1/messages dispatch"}}`

// A key served nothing on Messages tests as that, not as a failure of the
// key: its models answer on Chat.
func TestTestGroupWithoutMessages(t *testing.T) {
	sandboxHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = rw.Write([]byte(`{"object":"list","data":[{"id":"claude-sonnet-4-5","object":"model"}]}`))
		case r.URL.Path == "/v1/messages":
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusForbidden)
			_, _ = rw.Write([]byte(sub2apiNoDispatch))
		default:
			_, _ = rw.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL, Key: "sk-1"}
	got := map[Protocol]Result{}
	for _, r := range p.Test(context.Background()) {
		got[r.Protocol] = r
	}
	if !got[Chat].OK || got[Chat].Unserved {
		t.Errorf("chat: %+v", got[Chat])
	}
	if a := got[Anthropic]; a.OK || !a.Unserved || a.Status != http.StatusForbidden {
		t.Errorf("anthropic: %+v, want unserved", a)
	}
}

// sub2api's 404 for a model its /v1/models lists that no account of the
// key's group serves (no_account_error.go), as a Sidrune AI China-region
// key's Test got it for gpt-5.6 (Axo, 11-20).
const sub2apiNoAccount = `{"error":{"type":"model_not_found","message":"Model \"gpt-5.6\" is not supported by any configured account in this group"}}`

// A key whose group serves none of the first model it lists tests on the
// next one, not as a failure of the key.
func TestTestNextModelWhenUnserved(t *testing.T) {
	sandboxHome(t)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_, _ = rw.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6"},{"id":"gpt-5.5"}]}`))
			return
		}
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		asked = append(asked, in.Model)
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if in.Model == "gpt-5.6" {
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(sub2apiNoAccount))
			return
		}
		_, _ = rw.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	if err := catalog.SaveLive("relay", srv.URL+"/v1", []catalog.Model{{ID: "gpt-5.6"}, {ID: "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-1"}
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK || rs[0].Model != "gpt-5.5" {
		t.Errorf("results %+v, want chat OK on gpt-5.5", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{"gpt-5.6", "gpt-5.5"}) {
		t.Errorf("asked %q", asked)
	}
}

// A model refused as one the key isn't served, every model the key has,
// asks a few at most and reports the last refusal.
func TestTestUnservedModelsAskedAFew(t *testing.T) {
	sandboxHome(t)
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_, _ = rw.Write([]byte(`{"object":"list","data":[{"id":"m1"},{"id":"m2"},{"id":"m3"},{"id":"m4"},{"id":"m5"},{"id":"m6"}]}`))
			return
		}
		mu.Lock()
		n++
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusNotFound)
		_, _ = rw.Write([]byte(sub2apiNoAccount))
	}))
	defer srv.Close()
	var ms []catalog.Model
	for _, id := range []string{"m1", "m2", "m3", "m4", "m5", "m6"} {
		ms = append(ms, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive("relay", srv.URL+"/v1", ms); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-1"}
	rs := p.Test(context.Background())
	if len(rs) != 1 || rs[0].OK || rs[0].Status != http.StatusNotFound {
		t.Errorf("results %+v, want one 404", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != testModels {
		t.Errorf("asked %d times, want %d", n, testModels)
	}
}

// sub2api's 401 to a key it doesn't know, as global.sidrune.ai answered a
// Sidrune AI China key on every endpoint (Axo, 11-20).
const sub2apiInvalidKey = `{"error":{"type":"authentication_error","message":"Invalid API key"}}`

// A key of a partner's other place, China's or Global's, tests at the
// place that takes it, and says which, for the editor to move there; a
// key neither takes stays refused where it was.
// twoPlaces serves a preset "sid" with a China region taking sk-cn and a
// Global one taking sk-global; each refuses any other key as sub2api does.
func twoPlaces(t *testing.T) (cn, global *httptest.Server) {
	t.Helper()
	serve := func(key string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			if r.Header.Get("Authorization") != "Bearer "+key {
				rw.WriteHeader(http.StatusUnauthorized)
				_, _ = rw.Write([]byte(sub2apiInvalidKey))
				return
			}
			if r.Method != http.MethodPost {
				_, _ = rw.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5"}]}`))
				return
			}
			_, _ = rw.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	cn, global = serve("sk-cn"), serve("sk-global")
	pr := &PresetDef{Regions: []Region{
		{ID: "cn", Name: "China", Chat: cn.URL + "/v1"},
		{ID: "global", Name: "Global", Chat: global.URL + "/v1"},
	}}
	was := testPreset
	t.Cleanup(func() { testPreset = was })
	testPreset = func(id string) *PresetDef {
		if id == "sid" {
			return pr
		}
		return nil
	}
	return cn, global
}

func TestTestKeyOfTheOtherPlace(t *testing.T) {
	sandboxHome(t)
	cn, global := twoPlaces(t)
	for _, c := range []struct {
		name, key, at, want string
		ok                  bool
	}{
		{"a China key at Global", "sk-cn", global.URL, "cn", true},
		{"a Global key at China", "sk-global", cn.URL, "global", true},
		{"a China key at China", "sk-cn", cn.URL, "", true},
		{"a key neither takes", "sk-none", global.URL, "", false},
	} {
		p := Provider{ID: "sid", Name: "Sid", Preset: "sid", Chat: c.at + "/v1", Key: c.key}
		rs := p.Test(context.Background())
		if len(rs) != 1 || rs[0].OK != c.ok || rs[0].Region != c.want {
			t.Errorf("%s: %+v, want ok %v at region %q", c.name, rs, c.ok, c.want)
		}
		if !c.ok && rs[0].Status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want the 401", c.name, rs[0].Status)
		}
	}
}

// A provider saved with a key its place refuses, which the other place
// takes, is saved at the other place (Axo's China key at Global, 11-20).
func TestSaveMovesToKeyPlace(t *testing.T) {
	sandboxHome(t)
	cn, global := twoPlaces(t)
	for _, c := range []struct {
		name, key, at, want, chat string
	}{
		{"a China key at Global", "sk-cn", global.URL, "China", cn.URL + "/v1"},
		{"a Global key at Global", "sk-global", global.URL, "", global.URL + "/v1"},
		{"a key neither takes", "sk-none", global.URL, "", global.URL + "/v1"},
	} {
		p := Provider{ID: "sid", Name: "Sid", Preset: "sid", Chat: c.at + "/v1", Key: c.key}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		_, err := p.Fetch(context.Background())
		q, at := MoveToKeyPlace(context.Background(), p, err)
		if at != c.want {
			t.Errorf("%s: moved to %q, want %q (fetch: %v)", c.name, at, c.want, err)
		}
		saved, ferr := Find("sid")
		if ferr != nil {
			t.Fatal(ferr)
		}
		if saved.Chat != c.chat || q.Chat != c.chat {
			t.Errorf("%s: saved at %s (returned %s), want %s", c.name, saved.Chat, q.Chat, c.chat)
		}
	}
}
