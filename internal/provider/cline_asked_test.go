package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// ClinePassAsked says what a ClinePass request asked the Cline API for
// (zRain on Discord, 10-24): what magpie wrote into it, and nothing more.
// Since 2026-11-06 Cline refuses providerOptions (#5442), so the Upstream
// tick asks for nothing on a DeepSeek or GLM model and only glm-5.3-flash
// keeps OpenRouter's provider.only; the Cline plugin's provider asks for
// nothing by default (0.1.13 pins nothing unless its own option is on,
// and a pin it writes is refused, so no reply is served on it).
func TestClinePassAsked(t *testing.T) {
	const ds = "cline-pass/deepseek-v4.1-flash-1017"
	builtin := Provider{ID: "clinepass", Preset: "clinepass"}
	ticked := builtin
	ticked.PinUpstream = true
	plug := Provider{ID: "cline", Account: &Account{plugin: &plugin.Provider{ID: "cline"}}}
	plugTicked := plug
	plugTicked.PinUpstream = true
	other := Provider{ID: "grok", Account: &Account{plugin: &plugin.Provider{ID: "grok"}}}
	other.PinUpstream = true
	for _, c := range []struct {
		name  string
		p     Provider
		model string
		want  string
	}{
		{"built-in, tick off", builtin, ds, ""},
		{"built-in, tick on", ticked, ds, ""},
		{"built-in, tick on, GLM", ticked, "cline-pass/glm-5.3", ""},
		{"built-in, tick on, usage-billed DeepSeek", ticked, "deepseek/deepseek-v4-pro", ""},
		{"built-in, tick on, glm-5.3-flash", ticked, "cline-pass/glm-5.3-flash", "z-ai"},
		{"built-in, tick off, glm-5.3-flash", builtin, "cline-pass/glm-5.3-flash", ""},
		{"plugin by default", plug, ds, ""},
		{"plugin, tick on", plugTicked, ds, ""},
		{"plugin, tick on, glm-5.3-flash", plugTicked, "cline-pass/glm-5.3-flash", "z-ai"},
		{"plugin, a model it doesn't pin", plug, "cline-pass/kimi-k3", ""},
		{"another plugin", other, "cline-pass/glm-5.3-flash", ""},
	} {
		if got := c.p.ClinePassAsked(c.model); got != c.want {
			t.Errorf("%s: ClinePassAsked(%s) = %q; want %q", c.name, c.model, got, c.want)
		}
	}
}
