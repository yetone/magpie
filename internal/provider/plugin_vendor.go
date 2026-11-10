package provider

import (
	"net"
	"strings"
	"sync"
)

// A plugin's provider in front of a vendor magpie has a preset for is that
// vendor to the gateway (#5464): what magpie does for the vendor's API —
// Mistral's thinking parts, DeepSeek's reasoning replay and token field,
// xAI's schema rules, OpenRouter's shared free pool — is done for the
// plugin's requests too, before they reach its fetch. The plugin says
// where its requests go with its provider's `api` (its config hook's
// cfg.provider[id].api, plugin.Provider.API), the base URL it already
// gives OpenCode; nothing else is asked of it. A base on no preset's host
// (the vendor's own app API, a local helper) leaves the plugin as it was.

// vendorPresets is each preset's API host to its id, the first preset in
// the list taking a host two share (Qwen's DashScope ones): every base a
// preset names, its regions' too. A local server's or another magpie's
// preset is no vendor's (Ollama's localhost would make every plugin
// whose fetch talks to a helper on this computer Ollama).
var vendorPresets = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	add := func(id string, bases ...string) {
		for _, b := range bases {
			if h := HostOf(b); h != "" && out[h] == "" {
				out[h] = id
			}
		}
	}
	for _, d := range presets {
		if d.Kind == KindLocal || d.Kind == KindRemote {
			continue
		}
		add(d.ID, d.Chat, d.Responses, d.Anthropic)
		for _, r := range d.Regions {
			add(d.ID, r.Chat, r.Responses, r.Anthropic)
		}
	}
	return out
})

// PluginAPI is the base URL the plugin's provider says p's requests go to
// (its `api`): https://api.mistral.ai/v1 behind a Mistral Vibe plugin. ""
// for a provider that isn't a plugin's.
func (p Provider) PluginAPI() string {
	if !p.IsPlugin() {
		return ""
	}
	return p.Account.plugin.API
}

// pluginVendor is the preset, and its host, of the vendor a plugin's
// provider says its requests go to; "" for any other provider, a moved
// subscription (whose Host is the built-in's already), and a plugin whose
// base is on no preset's host or on this computer or network.
func (p Provider) pluginVendor() (preset, host string) {
	if !p.IsPlugin() || p.Account.moved {
		return "", ""
	}
	h := HostOf(p.PluginAPI())
	if h == "" || onThisNetwork(h) {
		return "", ""
	}
	if id := vendorPresets()[h]; id != "" {
		return id, h
	}
	return "", ""
}

// onThisNetwork is a host on this computer or its network.
func onThisNetwork(h string) bool {
	if s, _, err := net.SplitHostPort(h); err == nil {
		h = s
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

// VendorHost is the host of the vendor's API p's requests are shaped for:
// Host(), and for a plugin's provider in front of a vendor magpie has a
// preset for, that vendor's API host (api.mistral.ai), where Host() is
// the plugin's id. The gateway's checks of whose API a request goes to
// read this; Host() stays where calls are kept and shown.
func (p Provider) VendorHost() string {
	if _, h := p.pluginVendor(); h != "" {
		return h
	}
	return p.Host()
}

// VendorPreset is the preset of the vendor p's requests are shaped for:
// p.Preset, and for a plugin's provider in front of a vendor magpie has a
// preset for, that preset (mistral).
func (p Provider) VendorPreset() string {
	if p.Preset != "" {
		return p.Preset
	}
	id, _ := p.pluginVendor()
	return id
}
