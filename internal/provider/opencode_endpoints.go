package provider

import (
	"cmp"
	"net/url"
	"strings"
)

// openCodeRoot is the root of OpenCode's gateway a base URL is at — Zen's
// (https://opencode.ai/zen) or Go's (…/zen/go), or the inference API's
// that serves the same paths (…/inference, …/inference/go: models.dev's
// URL for both since October 2026), given with /v1 or without — and the
// models.dev provider that lists its models: opencode or opencode-go.
// Both are "" for any other URL.
func openCodeRoot(base string) (root, catalogID string) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Scheme == "" {
		return "", ""
	}
	if !openCodeHost(u.Hostname()) {
		return "", ""
	}
	switch path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1"); path {
	case "/zen/go", "/inference/go":
		return u.Scheme + "://" + u.Host + path, "opencode-go"
	case "/zen", "/inference":
		return u.Scheme + "://" + u.Host + path, "opencode"
	}
	return "", ""
}

// openCodeGateway says which of OpenCode's gateways base is at: "go" for
// Go's (…/zen/go, or the inference API's …/inference/go, whatever follows:
// /v1, or /openai/v1 and /anthropic, the URLs models.dev and Go's docs
// give since October 2026), "zen" for Zen's (…/zen, …/inference: the
// workspace's pay-as-you-go credits), "" for any other, a provider of the
// user's own at …/inference/custom among them, which its vendor bills.
func openCodeGateway(base string) string {
	// every host openCodeHost takes holds it: another base isn't parsed
	if !strings.Contains(strings.ToLower(base), "opencode.ai") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || !openCodeHost(u.Hostname()) {
		return ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if seg[0] != "zen" && seg[0] != "inference" {
		return ""
	}
	if len(seg) > 1 {
		switch seg[1] {
		case "go":
			return "go"
		case "custom":
			return ""
		}
	}
	return "zen"
}

// openCodeGemini is the base of Gemini's generateContent at the gateway
// root is: models/{model}:streamGenerateContent is asked under it. Zen's
// /zen/v1 relays it to the inference API's Google route, reading the key
// from x-goog-api-key; Go's /zen/go/v1 has no such route (404), so Go's
// Gemini models are asked at the inference API's /inference/go/v1, where
// Go's docs give their endpoint (opencode.ai/v2/docs/console/go, #2168).
func openCodeGemini(root string) string {
	if r, ok := strings.CutSuffix(root, "/zen/go"); ok {
		return r + "/inference/go/v1"
	}
	return root + "/v1"
}

// openCodeEndpoints fills in the APIs of OpenCode's gateway a provider at
// it was saved without. Zen and Go serve each model on one API of three,
// all under one root — chat completions and Responses at /v1, Anthropic's
// Messages at the root — and turn it away on the other two. OpenCode asks
// a model through the AI SDK package models.dev's per-model provider.npm
// names: @ai-sdk/openai (Responses) for GPT, Grok and Muse Spark,
// @ai-sdk/anthropic (Messages) for Claude, MiniMax and some Qwen,
// @ai-sdk/google (Gemini's generateContent) for Gemini, and
// @ai-sdk/openai-compatible (chat) for the rest. A provider at Go added by
// hand or brought from another app with its chat URL alone had nowhere
// but chat to send GPT, Grok, MiniMax or Muse Spark, which Go answers
// with "Model does not support this protocol" (#1215), and none had
// anywhere but those three to send a Gemini model (#2168). A URL the user
// gave an API keeps it, and so does a catalog they named.
func (p *Provider) openCodeEndpoints() {
	root, catalogID := "", ""
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
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
	p.Gemini = cmp.Or(p.Gemini, openCodeGemini(root))
	p.Catalog = cmp.Or(p.Catalog, catalogID)
}

// openCodeCatalogs are the models.dev providers that say which API a model
// of OpenCode's gateway is served on: the list of the gateway its URL is
// at (Go's for Go, Zen's for Zen) alone, whatever catalog the provider was
// saved with — the two serve one model on different APIs (MiniMax M2.7 on
// Messages at Go, on chat at Zen) — or, at another path, its own.
func (p Provider) openCodeCatalogs() []string {
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
		if _, id := openCodeRoot(u); id != "" {
			return []string{id}
		}
	}
	return p.Catalogs()
}

// NamedOnlyOn is whether p serves on proto only the models its list names
// that API for, so that one it names none for isn't asked there:
// OpenCode's Gemini API, which serves the Gemini models alone (each of
// OpenCode's models is served on one API family, its inference docs say).
// The rest are asked on chat, Responses or Messages, as before it had a
// Gemini URL (#2168).
func (p Provider) NamedOnlyOn(proto Protocol) bool { return proto == Gemini && p.IsOpenCode() }
