package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A model whose vendor takes more than the window agents are told, at a
// higher price above it (Copilot's gpt-6.2-sol: its default tier's 272K,
// and the 1,050,000 its limits' max_prompt_tokens say it takes), is
// listed in /v1/models with that most as max_context_window, and another
// magpie that has this one as its provider keeps it as its own model's
// MaxContext. Before, the list told 272K alone, so the other magpie could
// neither offer the raise ("Each model's most") nor let its provider's
// window for all its models reach past 272K (moooyo, #7296). A model with
// no larger window says none, and the window told stays the default
// tier's on both sides until the user raises it.
func TestRemoteMagpieKeepsTheMostAWindowMayBeRaisedTo(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// the windows Copilot's list gives (provider.copilotTokenPrices.apply)
	if err := catalog.SaveLive("relay", up.URL+"/v1", []catalog.Model{
		{ID: "gpt-6.2-sol", Context: 272000, MaxContext: 1050000},
		{ID: "gpt-4.1", Context: 128000},
	}); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	list := func() map[string]map[string]any {
		t.Helper()
		res, err := http.Get(remote.URL + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var v struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("/v1/models: %d %s", res.StatusCode, b)
		}
		out := map[string]map[string]any{}
		for _, m := range v.Data {
			id, _ := m["id"].(string)
			out[id] = m
		}
		return out
	}
	rows := list()
	sol, small := rows["relay/gpt-6.2-sol"], rows["relay/gpt-4.1"]
	if sol == nil || small == nil {
		t.Fatalf("/v1/models lists %v", rows)
	}
	if sol["context_window"] != float64(272000) || sol["max_input_tokens"] != float64(272000) || sol["max_context_window"] != float64(1050000) {
		t.Errorf("gpt-6.2-sol is listed at context_window %v, max_input_tokens %v, max_context_window %v; want 272000, 272000, 1050000",
			sol["context_window"], sol["max_input_tokens"], sol["max_context_window"])
	}
	if _, ok := small["max_context_window"]; ok {
		t.Errorf("gpt-4.1, with no larger window, is listed with max_context_window %v", small["max_context_window"])
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
	live, _, _ := catalog.Live(id)
	got := map[string]catalog.Model{}
	for _, m := range live {
		got[m.ID] = m
	}
	if m := got["relay/gpt-6.2-sol"]; m.Context != 272000 || m.MaxContext != 1050000 {
		t.Errorf("the remote's gpt-6.2-sol is kept at %d, most %d; want 272000, most 1050000", m.Context, m.MaxContext)
	}
	if m := got["relay/gpt-4.1"]; m.Context != 128000 || m.MaxContext != 0 {
		t.Errorf("the remote's gpt-4.1 is kept at %d, most %d; want 128000, most 0", m.Context, m.MaxContext)
	}

	// untouched, agents here are told the default tier's window
	office, _ = provider.Find(id)
	if w := office.WindowOf(got["relay/gpt-6.2-sol"]); w != 272000 {
		t.Errorf("the remote's gpt-6.2-sol is told at %d before any raise, want 272000", w)
	}
	// a window for all of the provider's models reaches each model's own
	// most and no further
	office.Contexts = map[string]int{"*": 2000000}
	if err := provider.Save(*office); err != nil {
		t.Fatal(err)
	}
	office, _ = provider.Find(id)
	if w := office.WindowOf(got["relay/gpt-6.2-sol"]); w != 1050000 {
		t.Errorf("raised for all models, the remote's gpt-6.2-sol is told at %d, want its most, 1050000", w)
	}
	if w := office.WindowOf(got["relay/gpt-4.1"]); w != 128000 {
		t.Errorf("raised for all models, the remote's gpt-4.1 is told at %d, want its own 128000", w)
	}
}
