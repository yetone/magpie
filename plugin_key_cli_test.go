package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// magpie provider login asks a plugin's key by its method's label, with
// the placeholder the plugin gives as its hint (Lemon on Discord).
func TestPluginKeyPrompt(t *testing.T) {
	p := keyPrompt("Lemon", plugin.Method{Type: "api", Label: "Lemon API key (from lemon.example/keys)", Placeholder: "sk-lemon-…"})
	if !strings.HasPrefix(p, "Lemon API key (from lemon.example/keys) ") || !strings.Contains(p, "(sk-lemon-…)") || !strings.HasSuffix(p, ": ") {
		t.Errorf("prompt = %q", p)
	}
	if p := keyPrompt("Lime", plugin.Method{Type: "api", Label: "API key"}); p != "Lime API key: " {
		t.Errorf("generic label's prompt = %q", p)
	}
}
