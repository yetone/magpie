package provider

import (
	"cmp"
	"net/url"
	"strings"
)

// openCodeRoot is the root of OpenCode's gateway a base URL is at — Zen's
// (https://opencode.ai/zen) or Go's (…/zen/go), given with /v1 or without —
// and the models.dev provider that lists its models: opencode or
// opencode-go. Both are "" for any other URL.
func openCodeRoot(base string) (root, catalogID string) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Scheme == "" {
		return "", ""
	}
	if h := strings.ToLower(u.Hostname()); h != "opencode.ai" && !strings.HasSuffix(h, ".opencode.ai") {
		return "", ""
	}
	switch path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1"); path {
	case "/zen/go":
		return u.Scheme + "://" + u.Host + path, "opencode-go"
	case "/zen":
		return u.Scheme + "://" + u.Host + path, "opencode"
	}
	return "", ""
}

// openCodeEndpoints fills in the APIs of OpenCode's gateway a provider at
// it was saved without. Zen and Go serve each model on one API of three,
// all under one root — chat completions and Responses at /v1, Anthropic's
// Messages at the root — and turn it away on the other two. OpenCode asks
// a model through the AI SDK package models.dev's per-model provider.npm
// names: @ai-sdk/openai (Responses) for GPT, Grok and Muse Spark,
// @ai-sdk/anthropic (Messages) for Claude, MiniMax and some Qwen, and
// @ai-sdk/openai-compatible (chat) for the rest. A provider at Go added by
// hand or brought from another app with its chat URL alone had nowhere
// but chat to send GPT, Grok, MiniMax or Muse Spark, which Go answers
// with "Model does not support this protocol" (#1215). A URL the user gave
// an API keeps it, and so does a catalog they named.
func (p *Provider) openCodeEndpoints() {
	root, catalogID := "", ""
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic} {
		if root, catalogID = openCodeRoot(u); root != "" {
			break
		}
	}
	if root == "" {
		return
	}
	p.Chat = cmp.Or(p.Chat, root+"/v1")
	p.Responses = cmp.Or(p.Responses, root+"/v1")
	p.Anthropic = cmp.Or(p.Anthropic, root)
	p.Catalog = cmp.Or(p.Catalog, catalogID)
}

// openCodeCatalogs are the models.dev providers that say which API a model
// of OpenCode's gateway is served on: the list of the gateway its URL is
// at (Go's for Go, Zen's for Zen) alone, whatever catalog the provider was
// saved with — the two serve one model on different APIs (MiniMax M2.7 on
// Messages at Go, on chat at Zen) — or, at another path, its own.
func (p Provider) openCodeCatalogs() []string {
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic} {
		if _, id := openCodeRoot(u); id != "" {
			return []string{id}
		}
	}
	return p.Catalogs()
}
