package provider

import (
	"cmp"
	"net/url"
	"slices"
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
	path := strings.TrimRight(u.Path, "/")
	if r, ok := strings.CutSuffix(path, "/google/v1beta"); ok {
		path = r
	} else {
		path = strings.TrimSuffix(path, "/v1")
		for _, api := range []string{"/openai", "/anthropic", "/google"} {
			path = strings.TrimSuffix(path, api)
		}
	}
	switch path {
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
// Gemini models are asked at the inference API, where Go's docs give
// their endpoint: /inference/go/google/v1beta/models/{model} (since
// November 2026; before it /inference/go/v1, #2168).
func openCodeGemini(root string) string {
	if r, ok := strings.CutSuffix(root, "/zen/go"); ok {
		return r + "/inference/go/google/v1beta"
	}
	if strings.HasSuffix(root, "/inference/go") {
		return root + "/google/v1beta"
	}
	return root + "/v1"
}

// openCodeGoSplit is whether a Go URL names its API in the path, the way
// Go's docs give them since November 2026: …/inference/go/openai/v1 (chat
// and Responses), …/inference/go/anthropic (Messages) and
// …/inference/go/google/v1beta (Gemini).
func openCodeGoSplit(u string) bool {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || openCodeGateway(u) != "go" {
		return false
	}
	for _, seg := range strings.Split(pu.Path, "/") {
		if seg == "openai" || seg == "anthropic" || seg == "google" {
			return true
		}
	}
	return false
}

// openCodeGoOld are the URLs the OpenCode Go preset gave before Go's docs
// named an endpoint per API; a provider of the preset saved with them is
// moved to where the docs have them (openCodeEndpoints).
var openCodeGoOld = map[string]bool{
	"https://opencode.ai/zen/go/v1":       true,
	"https://opencode.ai/zen/go":          true,
	"https://opencode.ai/inference/go/v1": true,
	"https://opencode.ai/inference/go":    true,
}

// OpenCodeGo are the URLs Go's docs give each API (opencode.ai/docs/console/go,
// Endpoints).
const (
	OpenCodeGoChat      = "https://opencode.ai/inference/go/openai/v1"
	OpenCodeGoAnthropic = "https://opencode.ai/inference/go/anthropic"
	OpenCodeGoGemini    = "https://opencode.ai/inference/go/google/v1beta"
)

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
	chat, anthropic := root+"/v1", root
	if strings.HasSuffix(root, "/inference/go") && slices.ContainsFunc([]string{p.Chat, p.Responses, p.Anthropic, p.Gemini}, openCodeGoSplit) {
		chat, anthropic = root+"/openai/v1", root+"/anthropic"
	}
	if p.Preset == "opencode-go" {
		// the preset's own URLs, saved before Go's docs named one per API
		for _, f := range []struct {
			have *string
			to   string
		}{{&p.Chat, OpenCodeGoChat}, {&p.Responses, OpenCodeGoChat}, {&p.Anthropic, OpenCodeGoAnthropic}} {
			if openCodeGoOld[strings.ToLower(*f.have)] {
				*f.have = f.to
			}
		}
		if strings.EqualFold(p.Gemini, "https://opencode.ai/inference/go/v1") {
			p.Gemini = OpenCodeGoGemini
		}
		root, catalogID = openCodeRoot(cmp.Or(p.Chat, p.Responses, p.Anthropic, p.Gemini))
		if strings.HasSuffix(root, "/inference/go") {
			chat, anthropic = root+"/openai/v1", root+"/anthropic"
		}
	}
	p.Chat = cmp.Or(p.Chat, chat)
	p.Responses = cmp.Or(p.Responses, chat)
	p.Anthropic = cmp.Or(p.Anthropic, anthropic)
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
