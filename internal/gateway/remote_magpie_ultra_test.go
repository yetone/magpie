package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Another magpie's model it serves Ultrafast is offered Ultrafast here,
// and a turn asked Ultrafast goes on to it so and from it to its vendor so
// (OoopsHh on X: a magpie in Docker set a group's member Ultrafast, and the
// magpie that has it as its provider was offered Fast alone). The remote
// has no Codex beside it, so only its own providers can say a model has
// Ultrafast: here a relay's gpt-6.2-sol, and a group it is sent Ultrafast
// in. Here the gateway is both computers', as in
// TestRemoteMagpieOffersItsTiers.
func TestRemoteMagpieOffersUltrafast(t *testing.T) {
	var mu sync.Mutex
	var vendor, hops []string // service_tier as the vendor, and the remote magpie, were sent it
	tierOf := func(b []byte) string {
		var v struct {
			Tier *string `json:"service_tier"`
		}
		json.Unmarshal(b, &v)
		if v.Tier == nil {
			return "(none)"
		}
		return *v.Tier
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, tierOf(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.2-sol"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.2-sol","usage":{"input_tokens":5,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir()) // no Codex beside it, as in Docker
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.2-sol", "glm-5"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "sol", Name: "Sol", Members: []string{"relay/gpt-6.2-sol"},
		Fast: []string{"relay/gpt-6.2-sol"}, Ultra: []string{"relay/gpt-6.2-sol"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			mu.Lock()
			hops = append(hops, tierOf(b))
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)

	// the remote's list, as another magpie reads it
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	listed := map[string]string{}
	for _, m := range list.Data {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + " "
		}
		listed[m.ID] = s
	}
	for id, want := range map[string]string{"relay/gpt-6.2-sol": "priority ultrafast ", "group/sol": "priority ultrafast ", "relay/glm-5": ""} {
		if s, ok := listed[id]; !ok || s != want {
			t.Errorf("%s listed with tiers %q (listed %v), want %q", id, s, ok, want)
		}
	}

	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// what the GUI's Ultrafast button, and an agent's pick, go by here
	for _, m := range []string{"relay/gpt-6.2-sol", "group/sol"} {
		if !provider.CanUltra(*office, m) {
			t.Errorf("%s: no Ultrafast on the remote's model (tiers %v)", m, provider.RemoteTiers(*office, m))
		}
	}
	if provider.CanUltra(*office, "relay/glm-5") {
		t.Error("relay/glm-5: Ultrafast where the remote offers no tier")
	}

	// a turn asked Ultrafast, as Codex sends it, to each
	for _, m := range []string{"office/relay/gpt-6.2-sol", "office/group/sol"} {
		ask := strings.Replace(codexFastAsk("ultrafast"), `"model":"m1"`, `"model":"`+m+`"`, 1)
		if code, body := post(t, "/v1/responses", ask); code != 200 {
			t.Fatalf("%s: %d: %s", m, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"ultrafast", "ultrafast"}; !slices.Equal(hops, want) || !slices.Equal(vendor, want) {
		t.Errorf("tier sent to the remote magpie %v, by it to its vendor %v; want %v for both", hops, vendor, want)
	}
}
