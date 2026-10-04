package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A remote magpie is added from the TUI with its address, as the app's
// editor and magpie provider add remote-magpie … url=… add it, and its
// address is changed there later (akic404 on Discord: the TUI had no place
// for it, only the key).
func TestTUIAddsARemoteMagpieWithItsAddress(t *testing.T) {
	home(t)
	m := press(t, model{w: 120, h: 40}, "2", "a")
	if m.mode != modePick {
		t.Fatalf("a opened mode %v, not the vendors", m.mode)
	}
	m = typeIn(m, "remote-magpie")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "http://192.168.1.20:3425" {
		t.Fatalf("picking Remote magpie asked %q (mode %v), not its address", m.ask.input.Placeholder, m.mode)
	}
	// nothing typed: said to be needed, nothing added
	m = press(t, m, "enter")
	wantFlash(t, m, false, "The other magpie's address is needed")
	if _, err := provider.Find("remote-magpie"); err == nil {
		t.Fatal("added without an address")
	}

	m = press(t, m, "a")
	m = typeIn(m, "remote-magpie")
	m = press(t, m, "enter")
	m = typeIn(m, "127.0.0.1:1")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "the API key" {
		t.Fatalf("after the address it asked %q (mode %v), not the key", m.ask.input.Placeholder, m.mode)
	}
	m = typeIn(m, "sk-magpie-test")
	m = press(t, m, "enter")
	// nothing answers there: added, and said why it has no models
	wantFlash(t, m, false, "added Remote magpie · no model list")
	p, err := provider.Find("remote-magpie")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "http://127.0.0.1:1/v1" || p.Responses != "http://127.0.0.1:1/v1" || p.Anthropic != "http://127.0.0.1:1" || p.Key != "sk-magpie-test" {
		t.Fatalf("saved %q %q %q key %q", p.Chat, p.Responses, p.Anthropic, p.Key)
	}

	// w changes its address, the one there now in the line
	m.reloadProviders()
	for i, q := range m.provs {
		if q.ID == "remote-magpie" {
			m.prow = i
		}
	}
	m = press(t, m, "w")
	if m.mode != modeAsk || m.ask.input.Value() != "http://127.0.0.1:1" {
		t.Fatalf("w opened %q (mode %v), not the address now", m.ask.input.Value(), m.mode)
	}
	m = press(t, m, "ctrl+u")
	m = typeIn(m, "http://127.0.0.1:2/v1")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "address")
	p, _ = provider.Find("remote-magpie")
	if p.Chat != "http://127.0.0.1:2/v1" || p.Anthropic != "http://127.0.0.1:2" || p.Key != "sk-magpie-test" {
		t.Fatalf("after w: %q %q key %q", p.Chat, p.Anthropic, p.Key)
	}

	// a vendor of its own address has none to change
	m.reloadProviders()
	for i, q := range m.provs {
		if q.ID == "a" {
			m.prow = i
		}
	}
	m = press(t, m, "w")
	if m.mode == modeAsk {
		t.Fatal("w asked a custom provider's address")
	}
}

// Adding a provider fetches its list and says what came of it — how many
// models, or why none — and m on the Providers page fetches it again, as
// the app editor's Refresh does (akic404 on Discord: added from the TUI,
// a remote magpie had 0 models, with nothing said and no way to fetch
// them).
func TestTUIFetchesAProvidersModels(t *testing.T) {
	home(t)
	var mu sync.Mutex
	ids := []string{"custom/qwen3.6-max", "custom/glm-5.3"}
	down := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			http.Error(w, `{"error":"invalid gateway key"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		var data []map[string]any
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "object": "model", "display_name": id, "magpie_label": id + " · 中转站"})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	defer srv.Close()
	set := func(d bool, list ...string) {
		mu.Lock()
		defer mu.Unlock()
		down = d
		if list != nil {
			ids = list
		}
	}
	at := func(m model, id string) model {
		m.reloadProviders()
		for i, q := range m.provs {
			if q.ID == id {
				m.prow = i
			}
		}
		return m
	}

	m := press(t, model{w: 160, h: 40}, "2", "a")
	m = typeIn(m, "remote-magpie")
	m = press(t, m, "enter")
	m = typeIn(m, strings.TrimPrefix(srv.URL, "http://"))
	m = press(t, m, "enter")
	m = typeIn(m, "sk-magpie-test")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "added Remote magpie · 2 models from its list · 2 for agents")
	p, err := provider.Find("remote-magpie")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range p.Exposed() {
		got = append(got, x.ID)
	}
	if strings.Join(got, ",") != "custom/qwen3.6-max,custom/glm-5.3" {
		t.Fatalf("exposed %v", got)
	}

	// the other magpie's list grows: m fetches it again
	set(false, "custom/qwen3.6-max", "custom/glm-5.3", "custom/kimi-k3")
	m = at(m, "remote-magpie")
	m = press(t, m, "m")
	wantFlash(t, m, true, "Remote magpie: 3 models from its list · 3 for agents")
	if !strings.Contains(m.View(), "3 models") {
		t.Fatalf("the row doesn't say 3 models after m:\n%s", m.View())
	}

	// refused: said so, and the list had stays
	set(true)
	m = press(t, m, "m")
	wantFlash(t, m, false, "no model list")
	wantFlash(t, m, false, "m asks again")
	if p, _ := provider.Find("remote-magpie"); len(p.Exposed()) != 3 {
		t.Fatalf("a failed fetch left %d models", len(p.Exposed()))
	}
}
