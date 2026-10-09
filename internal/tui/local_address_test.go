package tui

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A local server's preset (Ollama, LM Studio, oMLX, MLX-Serve) is asked
// its address before its key, as the app's editor asks it: Enter alone keeps the
// default, another port or computer moves each API there, and w changes
// it later.
func TestTUIAddsALocalServerAtItsAddress(t *testing.T) {
	home(t)
	at := func(m model, id string) model {
		m.reloadProviders()
		for i, q := range m.provs {
			if q.ID == id {
				m.prow = i
			}
		}
		return m
	}

	// Enter alone: the preset's own address
	m := press(t, model{w: 120, h: 40}, "2", "a")
	m = typeIn(m, "lmstudio")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "http://localhost:1234" {
		t.Fatalf("picking LM Studio asked %q (mode %v), not its address", m.ask.input.Placeholder, m.mode)
	}
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "the API key" {
		t.Fatalf("after the address it asked %q (mode %v), not the key", m.ask.input.Placeholder, m.mode)
	}
	m = press(t, m, "enter")
	if p, err := provider.Find("lmstudio"); err != nil || p.Chat != "http://localhost:1234/v1" {
		t.Fatalf("lmstudio: %v %+v", err, p)
	}

	// a refused address: said, nothing added
	m = press(t, m, "a")
	m = typeIn(m, "omlx")
	m = press(t, m, "enter")
	m = typeIn(m, "ftp://box")
	m = press(t, m, "enter")
	wantFlash(t, m, false, "is not an address like http://localhost:11434")
	if _, err := provider.Find("omlx"); err == nil {
		t.Fatal("added at a refused address")
	}

	// another port: each API keeps its path
	m = press(t, m, "a")
	m = typeIn(m, "omlx")
	m = press(t, m, "enter")
	m = typeIn(m, "127.0.0.1:1")
	m = press(t, m, "enter")
	m = press(t, m, "enter")
	p, err := provider.Find("omlx")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "http://127.0.0.1:1/v1" || p.Responses != "http://127.0.0.1:1/v1" || p.Anthropic != "http://127.0.0.1:1" {
		t.Fatalf("omlx at %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}

	// w: the address now, and changed
	m = at(m, "omlx")
	m = press(t, m, "w")
	if m.mode != modeAsk || m.ask.input.Value() != "http://127.0.0.1:1" {
		t.Fatalf("w opened %q (mode %v), not the address now", m.ask.input.Value(), m.mode)
	}
	m = press(t, m, "ctrl+u")
	m = typeIn(m, "127.0.0.1:2")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "address http://127.0.0.1:2")
	p, _ = provider.Find("omlx")
	if p.Chat != "http://127.0.0.1:2/v1" || p.Responses != "http://127.0.0.1:2/v1" || p.Anthropic != "http://127.0.0.1:2" {
		t.Fatalf("after w: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	// a refused one leaves it
	m = press(t, m, "w", "ctrl+u")
	m = typeIn(m, "no such thing")
	m = press(t, m, "enter")
	wantFlash(t, m, false, "is not an address like")
	if p, _ := provider.Find("omlx"); p.Chat != "http://127.0.0.1:2/v1" {
		t.Fatalf("a refused w left %q", p.Chat)
	}

	// URLs set apart stay when the address is left as it opened, or typed
	// again in another form
	p, _ = provider.Find("omlx")
	p.Chat, p.Responses, p.Anthropic = "http://10.0.0.2:8000/v1", "http://10.0.0.2:8000/v1", "http://10.0.0.3:9000"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "10.0.0.2:8000/v1"} {
		m = at(m, "omlx")
		m = press(t, m, "w")
		if typed != "" {
			m = press(t, m, "ctrl+u")
			m = typeIn(m, typed)
		}
		m = press(t, m, "enter")
		wantFlash(t, m, true, "address unchanged")
		if p, _ := provider.Find("omlx"); p.Chat != "http://10.0.0.2:8000/v1" || p.Anthropic != "http://10.0.0.3:9000" {
			t.Fatalf("w, %q, enter moved them: %q %q", typed, p.Chat, p.Anthropic)
		}
	}
}
