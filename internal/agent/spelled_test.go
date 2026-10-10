package agent

import (
	"path/filepath"
	"testing"
)

// An agent that spells a model provider/model, magpie's as magpie/<ref>:
// one of its own providers named as one of magpie's (its own relay, and
// magpie's relay) is its own model, not magpie's. The row isn't connected
// on it, and Disconnect from magpie puts it back and is done, rather than
// leaving the row connected on the model it went back to (#835, OpenHanako's
// own deepseek/deepseek-v4-pro).
func TestOwnProviderNamedAsMagpies(t *testing.T) {
	ids := []string{"agy", "opencode", "openchamber", "mimocode", "pi", "aside", "omo", "prime-agent", "goose", "zed", "vscode",
		"crush", "dsh", "commandcode", "fx", "omp", "hermes", "morph", "kimi", "empryo", "minimax-code", "droid",
		"qoder", "qoder-cn", "grok", "atomcode", "cline", "snow"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if id == "aside" {
				asideHome(t)
			} else {
				syncHome(t)
			}
			a, err := Find(id)
			if err != nil {
				t.Fatal(err)
			}
			f := &a.Fields[0]
			if id == "cline" {
				// a model of OpenRouter's, the provider Cline is on
				hanakoWrite(t, a.Path, `{"version":1,"modes":{},"providers":{},"lastUsedProvider":"openrouter"}`, 0o600)
			}
			if err := f.Set("relay/glm-4.6"); err != nil {
				t.Fatal(err)
			}
			if v := f.Get(); v != "relay/glm-4.6" {
				t.Fatalf("own model reads %q", v)
			}
			if a.Wired() {
				t.Fatal("its own relay/glm-4.6 reads as magpie's")
			}
			if err := a.Connect(); err != nil {
				t.Fatal(err)
			}
			if !a.Wired() {
				t.Fatalf("not connected after Connect: %v", a.Values())
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if a.Wired() {
				t.Fatalf("still connected after Disconnect: %v", a.Values())
			}
			if v := f.Get(); v != "relay/glm-4.6" {
				t.Errorf("Disconnect left %q, not its own relay/glm-4.6", v)
			}
		})
	}
}

// OpenCode's own provider that sends to magpie's gateway is magpie's all
// the same (openCodeRef names a model there by it).
func TestOpenCodeGatewayProviderIsMagpies(t *testing.T) {
	syncHome(t)
	a, err := Find("opencode")
	if err != nil {
		t.Fatal(err)
	}
	hanakoWrite(t, a.Path, `{"provider":{"relay":{"options":{"baseURL":"`+gatewayV1()+`"},"models":{"relay/glm-4.6":{}}}},"model":"relay/relay/glm-4.6"}`, 0o644)
	if !a.Wired() {
		t.Fatalf("a model through its own provider at the gateway isn't magpie's: %v (%s)", a.Values(), filepath.Base(a.Path))
	}
	hanakoWrite(t, a.Path, `{"provider":{"relay":{"options":{"baseURL":"https://x/v1"},"models":{"glm-4.6":{}}}},"model":"relay/glm-4.6"}`, 0o644)
	if a.Wired() {
		t.Fatalf("its own relay elsewhere reads as magpie's: %v", a.Values())
	}
}
