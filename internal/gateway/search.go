package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A client may offer its model the web search of the vendor it was made
// for: Claude Code's WebSearch, Codex's web_search, a Chat client's
// web_search_options, Gemini's googleSearch. A provider that searches by
// itself is asked to; any other model is given a web_search tool of
// magpie's, which a model that can search — the Claude subscription, a
// Codex account, OpenAI, Anthropic's API, OpenRouter — answers for it, or,
// with none of them, a search API the user gave a key to (search_api.go).
// A Kimi Code plan searches for its own models first, with its search
// service (search_kimi.go).

// searchRounds is how many times a reply may go back to its model with
// what a search found.
const searchRounds = 6

// searchTimeout is how long a search's vendor has to answer; a model's
// search (modelSearch) has it from when its request went out, each try's
// own, and searchWaitMost in all, its waits for a slot or a resting
// provider with it (sideDeadline, #2318).
const searchTimeout, searchWaitMost = 2 * time.Minute, 5 * time.Minute

// searchingKey marks a search magpie asks for itself, which must not
// search by way of another model again.
type searchingKey struct{}

func searching(ctx context.Context) bool {
	v, _ := ctx.Value(searchingKey{}).(bool)
	return v
}

// searchForKey holds the request magpie's searches are run for, which the
// Routing view names by a search's row: magpie's own call, on the model it
// searches with, not the conversation's (#314).
type searchForKey struct{}

// CallFor is the request a call magpie made on its own was made for: its
// agent, and the model that agent asked for.
type CallFor struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
	// Unknown, on an image's description, is that the model was counted
	// text-only because nothing magpie knows says whether it sees images
	// (blindTo), not because its list or the user says it takes text only.
	// The Routing view says which, so a user whose model does see knows
	// to say so (#1287).
	Unknown bool `json:"unknown,omitempty"`
	// Missing, on an image's description, is the Image recognition model
	// the user picked in Settings that magpie can't find any more: the
	// model that described is the one magpie picks in its place
	// (VisionMissing), and the Routing view names both.
	Missing string `json:"missing,omitempty"`
	// MCP, on a web search or an image's description, is that the agent
	// called magpie's MCP server (SearchMCPPath, VisionMCPPath) itself, for
	// no model of magpie's.
	MCP bool `json:"mcp,omitempty"`
	// Instead, on a web search, is the searcher asked first that failed
	// or found nothing (Why, NothingFound): the searcher that did search
	// was the next one magpie tried in its place, and the Routing view
	// says so. Picked is that the first was the one picked in Settings.
	// A fallback that said nothing read as magpie ignoring the pick
	// (Gearclickist on Discord: Grok picked, Codex's model searched).
	// NotSearched is that the first answered without searching at all
	// (errNotSearched), apart from searching and naming no page.
	Instead      string `json:"instead,omitempty"`
	Why          string `json:"why,omitempty"`
	NothingFound bool   `json:"nothingFound,omitempty"`
	NotSearched  bool   `json:"notSearched,omitempty"`
	Picked       bool   `json:"picked,omitempty"`
}

func searchFor(ctx context.Context) *CallFor {
	f, _ := ctx.Value(searchForKey{}).(*CallFor)
	return f
}

// magpieSearches is whether magpie answers a client's web search for p
// though p searches the web by itself: Settings' SearchAll, while magpie
// can search for it. Never for magpie's own searcher request, which is
// the provider's search.
func magpieSearches(ctx context.Context, p provider.Provider) bool {
	return !searching(ctx) && settings.Load().SearchAll && canSearchFor(p)
}

// searchesItself says whether the provider searches the web by itself when
// asked on this API.
func searchesItself(p provider.Provider, proto provider.Protocol) bool {
	// a relay said to search (#359), and a plugin whose config hook says
	// its upstream does (#2027), on an API it has an address for: one with
	// only a Chat address would be sent no search tool at all. Another
	// sign-in's Searches is never read.
	saidTo := p.Searches && (proto == provider.Anthropic || proto == provider.Responses) && p.Base(proto) != ""
	if p.Account != nil {
		// Grok by its id: moved to its plugin, its account is the plugin's;
		// its plugin beside the built-in (grok-plugin) by the plugin's id.
		// Copilot's /responses runs web_search itself for every model it
		// serves there: gpt-5.3-codex, gpt-5.4-mini, gpt-6-luna, grok-4.7
		// and mai-code-1.1-flash each answered a request offering it with
		// web_search_call items and cited pages (2026-10-17, a real
		// account). Translated for magpie's search instead, Codex's every
		// turn on Copilot lost the relay (echo_ts on Discord).
		return (p.Account.Agent == "codex" || p.Account.Agent == "copilot" || p.ID == "grok" || p.PluginProvider() == "grok") && proto == provider.Responses ||
			p.IsPlugin() && saidTo
	}
	if saidTo {
		return true
	}
	return slices.Contains(searchHosts[proto], provider.HostOf(p.Base(proto)))
}

// googleAccount is a Google sign-in, Gemini CLI's or Antigravity's, which
// asks Code Assist; a plugin's is left to the plugin.
func googleAccount(p provider.Provider) bool {
	return p.Account != nil && (p.Account.Agent == "gemini" || p.Account.Agent == "antigravity") && !p.IsPlugin()
}

// codeAssistSearches is whether model, on a Google sign-in, searches with
// Google by itself: Gemini runs googleSearch on Code Assist, but not beside
// function tools — Antigravity turns the two down together, 400 "Please
// enable tool_config.include_server_side_tool_invocations to use Built-in
// tools with Function calling", with that flag set in toolConfig too — so
// only a request that declares none (searchesFor). One that does is given
// magpie's search, which the account answers itself (ownSearcher, #757).
func codeAssistSearches(p provider.Provider, proto provider.Protocol, model string) bool {
	return proto == provider.CodeAssist && googleAccount(p) && searchModel(model)
}

// geminiKeySearches is a provider asked with a key at Google AI Studio's
// Gemini API, the Google Gemini preset's beside its OpenAI-compatible one
// (provider.AIStudioGemini; cocoasoda on Discord): Gemini runs googleSearch
// there for a request that declares no function, as on Code Assist. One
// that does — Codex's every turn, Claude Code's — is given magpie's search,
// which the provider answers itself (searchesOwn), so the searches spend
// the user's own Gemini API credit. No other vendor's API is read as one.
func geminiKeySearches(p provider.Provider) bool {
	return p.Account == nil && !p.IsPlugin() && !p.IsVertex() && searchesItself(p, provider.Gemini)
}

// searchModel is a model Code Assist runs googleSearch for: Gemini's, not
// Claude's or GPT-OSS's on Antigravity, nor an image model.
func searchModel(model string) bool {
	l := strings.ToLower(model)
	return strings.Contains(l, "gemini") && !catalog.DrawsID(l)
}

// searchesFor is searchesItself for one request: a Google sign-in's Gemini
// searches by itself only for a request without function tools.
func searchesFor(p provider.Provider, proto provider.Protocol, model string, req *Request) bool {
	if proto == provider.Gemini && geminiKeySearches(p) {
		return searchModel(model) && (len(req.Tools) == 0 || req.ToolChoice == "none")
	}
	if searchesModel(p, proto, model) {
		return true
	}
	return codeAssistSearches(p, proto, model) && (len(req.Tools) == 0 || req.ToolChoice == "none")
}

// searchesModel is searchesItself for one model: a Remote magpie searches
// for the models its list says it does, on any API (search_remote.go).
func searchesModel(p provider.Provider, proto provider.Protocol, model string) bool {
	return searchesItself(p, proto) || proto != provider.Gemini && remoteSearch(p, model) != "" || modelSearches(p, proto, model)
}

// searchModels are the models a provider runs web_search for only for some
// of its models, by API: Command Code's Provider API passes the tool on to
// the model's vendor, and 12 of its 87 models search (M3chD09's run of all
// of them, #1590); the rest take the tool and don't search, or have it
// stripped, so they keep magpie's searcher. Its Claude models search on
// Messages only, the others on Responses only.
var searchModels = map[string]map[provider.Protocol][]string{
	"commandcode": {
		provider.Anthropic: {"claude-sonnet-5-5", "claude-haiku-5-5"},
		provider.Responses: {"gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-luna",
			"meta/muse-spark-1.2", "meta/muse-spark-1.2-contributor", "meta/muse-spark-1.3", "meta/muse-spark-1.3-contributor",
			"xai/grok-4.5", "xai/grok-4.6", "xai/grok-4.7"},
	},
}

// searchModelsHosts are searchModels' providers by host, for one added by
// hand rather than from the preset.
var searchModelsHosts = map[string]string{"api.commandcode.ai": "commandcode"}

// modelSearches is whether a provider of searchModels searches by itself
// for model on proto. Command Code refuses every built-in tool under its
// zero-data-retention header (x-cmd-zdr: cmd_zdr_unsupported_tool), so a
// provider sent that header searches for none.
func modelSearches(p provider.Provider, proto provider.Protocol, model string) bool {
	if p.Account != nil || p.Base(proto) == "" {
		return false
	}
	id := p.Preset
	if _, ok := searchModels[id]; !ok {
		id = searchModelsHosts[provider.HostOf(p.Base(proto))]
	}
	for k, v := range p.Headers {
		if strings.EqualFold(k, "x-cmd-zdr") && strings.TrimSpace(v) != "" && v != "0" {
			return false
		}
	}
	return slices.Contains(searchModels[id][proto], model)
}

// modelSearchesAny is whether model of p searches by itself on one of
// p's APIs (modelSearches), where the gateway sends a search for it.
func modelSearchesAny(p provider.Provider, model string) bool {
	return slices.ContainsFunc(p.Speaks(), func(proto provider.Protocol) bool { return modelSearches(p, proto, model) })
}

// pluginSearches is whether p's plugin says model searches the web by
// itself when asked in plain words (m.searches, as catalog.Model's
// WebSearch "native"): the gateway asks it with no search tool, and takes
// what it says with the links in it (searchWith).
func pluginSearches(p provider.Provider, model string) bool {
	if !p.IsPlugin() {
		return false
	}
	i := slices.IndexFunc(p.Available(), func(m catalog.Model) bool { return m.ID == model })
	return i >= 0 && p.Available()[i].WebSearch == searchNative
}

// searchesAlone is whether model of p searches by itself where the rest
// of p's models don't: one of searchModels, or a plugin's it marks.
func searchesAlone(p provider.Provider, model string) bool {
	return modelSearchesAny(p, model) || pluginSearches(p, model)
}

// partSearcher is a provider of searchModels that searches by itself for
// those of its models alone, on no API for every model, or a plugin's
// that marks some of its models as ones that search (pluginSearches): one
// magpie may search with, with those models only (searchableModel).
// Command Code's searched for themselves but were left out of the
// searcher picker and magpie's own pick (01huadalang on Discord), and so
// was every plugin's, Trae's search model among them (01huadalang, 10-20).
func partSearcher(p provider.Provider) bool {
	if p.IsPlugin() {
		return slices.ContainsFunc(p.Available(), func(m catalog.Model) bool { return m.WebSearch == searchNative })
	}
	if p.Account != nil || slices.ContainsFunc([]provider.Protocol{provider.Anthropic, provider.Responses, provider.Chat}, func(proto provider.Protocol) bool { return searchesItself(p, proto) }) {
		return false
	}
	if _, ok := searchModels[p.Preset]; ok {
		return true
	}
	return slices.ContainsFunc(p.Speaks(), func(proto provider.Protocol) bool { return searchModelsHosts[provider.HostOf(p.Base(proto))] != "" })
}

// searchHosts are the APIs that search by themselves: OpenAI's, xAI's,
// DeepSeek's, Zhipu's, Z.ai's and Volcengine Ark's web_search tool
// (Zhipu's and Ark's on their Responses APIs only, DeepSeek's on its
// Responses and Anthropic APIs: their Chat APIs have no such tool; Ark's
// in the shape forArkSearch gives it), Anthropic's web_search_20250305 (DeepSeek's Anthropic
// API runs it too, answering web_search_tool_result blocks, #669),
// OpenRouter's web plugin, Gemini's googleSearch.
var searchHosts = map[provider.Protocol][]string{
	provider.Responses: {"api.openai.com", "api.x.ai", "api.deepseek.com", "open.bigmodel.cn", "api.z.ai", arkHost},
	provider.Anthropic: {"api.anthropic.com", "api.deepseek.com"},
	provider.Chat:      {"openrouter.ai"},
	provider.Gemini:    {"generativelanguage.googleapis.com"},
}

// searchAsked is whether a body offers the model web search, looked for
// cheaply before it is parsed.
func searchAsked(proto provider.Protocol, body []byte) bool {
	// bytes, not string(body): each conversion copied the whole body, three
	// times a request (a 200KB body: 640KB and 3ms)
	if !bytes.Contains(body, []byte("web_search")) && !bytes.Contains(body, []byte("oogle_search")) && !bytes.Contains(body, []byte("googleSearch")) {
		return false
	}
	req, err := parse(proto, body)
	return err == nil && req.WebSearch
}

// searchedForIt is whether a relayed request's web search is done by magpie
// for the provider, which can't be relayed: a body offering it to a model
// that doesn't search by itself. What is known of the provider is asked
// first: Codex offers web_search on every turn, and each relayed turn was
// parsed whole to learn it, for a provider that searches by itself.
func searchedForIt(ctx context.Context, p provider.Provider, from provider.Protocol, model string, body []byte) bool {
	return (from == provider.Chat && !p.IsRemoteMagpie() || !searchesModel(p, from, model) || magpieSearches(ctx, p)) && searchAsked(from, body)
}

// searcher is the model magpie searches with: the one Settings names, while
// it can (chosenSearcher), else the first of the providers that search by
// themselves, with a small model of theirs, as searching needs no more.
// An explicit "off" leaves only the search APIs.
func searcher() (provider.Provider, string, bool) {
	if settings.Load().Searcher == "off" {
		return provider.Provider{}, "", false
	}
	if p, m, why := chosenSearcher(); why == "" && p != nil {
		return *p, m, true
	}
	return autoSearcher()
}

// searchRank is where a provider comes among those that can search for a
// model that can't, -1 when it can't. A relay said to search is left out:
// it would spend the relay's quota on other models' searches, and one that
// serves only Claude Code refuses magpie's own request, which has no
// metadata.user_id (#359).
func searchRank(p provider.Provider) int {
	p.Searches = false
	switch {
	case p.Account != nil && p.Account.Agent == "claude":
		return 0
	case p.Account != nil && p.Account.Agent == "codex":
		return 1
	case p.Account != nil && p.Account.Agent == "copilot":
		// it searches for its own models only: another's searches would
		// spend its premium requests, which no one asked for
		return -1
	case searchesItself(p, provider.Anthropic):
		return 2
	case searchesItself(p, provider.Responses):
		return 3
	case searchesItself(p, provider.Chat):
		return 4
	case partSearcher(p) && slices.ContainsFunc(p.Available(), func(m catalog.Model) bool { return searchesAlone(p, m.ID) }):
		return 4
	case googleAccount(p), geminiKeySearches(p):
		// Gemini on a Google sign-in (#757) or an AI Studio key
		// (cocoasoda on Discord), by googleSearch
		return 5
	case remoteSearchesAny(p):
		// another magpie, with the models it searches natively for
		return 5
	}
	return -1
}

// manualRelayRank is after every provider magpie picks or falls back to by
// itself: a relay said to search is offered only to be named (#359).
const manualRelayRank = 6

// namedSearcherRank is searchRank as Settings may name it: a Kimi Code
// plan, by its search service, and a relay said to search on Anthropic's or
// Responses' API come last, and only by hand.
func namedSearcherRank(p provider.Provider) (int, bool) {
	if r := searchRank(p); r >= 0 {
		return r, false
	}
	if provider.KimiCodeSearch(p) != "" {
		return manualRelayRank, true
	}
	if p.Searches && (searchesItself(p, provider.Anthropic) || searchesItself(p, provider.Responses)) {
		return manualRelayRank, true
	}
	return -1, false
}

// autoSearcher is the searcher magpie picks by itself: the first by
// searchRank that is on, with its small model. One with allowance left
// goes before one whose every account or key is out of it (smallReady),
// which is picked only when every searcher is, so that magpie still
// offers its search (canSearch) (#1801).
func autoSearcher() (provider.Provider, string, bool) {
	p, m, _, ok := autoSearcherReady()
	return p, m, ok
}

// autoSearcherReady is autoSearcher, and whether its pick has allowance
// left.
func autoSearcherReady() (provider.Provider, string, bool, bool) {
	var best provider.Provider
	var model string
	top, bestReady := -1, false
	for _, p := range provider.All() {
		r := searchRank(p)
		if r < 0 || !p.On() || (bestReady && r >= top) {
			continue
		}
		m, ready := searcherReady(p)
		if m == "" || top >= 0 && (bestReady && !ready || ready == bestReady && r >= top) {
			continue
		}
		if codexSearches(p) {
			// by the backend's search, which spends no model's tokens
			m = codexSearchModel
		}
		best, model, top, bestReady = p, m, r, ready
	}
	return best, model, bestReady, top >= 0
}

// Why the searcher Settings names isn't used, and magpie picks one itself.
const (
	SearcherGone   = "gone"    // no such provider any more
	SearcherOff    = "off"     // turned off, or signed out
	SearcherCant   = "cant"    // it can't search the web by itself
	SearcherNoneOf = "nomodel" // it lists no model to search with
)

// chosenSearcher is the provider and model Settings' Searcher names
// ("<provider>" for its small model, or "<provider>/<model>"), and why it
// can't be used, "" when it can. A model it no longer lists gives way to
// its small model, as when none is named. Nil when none is named or
// provider search is explicitly off.
func chosenSearcher() (*provider.Provider, string, string) {
	return namedSearcher(settings.Load().Searcher)
}

// namedSearcher is chosenSearcher of v, a searcher as Settings writes it.
func namedSearcher(v string) (*provider.Provider, string, string) {
	v = strings.TrimSpace(v)
	if v == "" || v == "off" {
		return nil, "", ""
	}
	id, model, _ := strings.Cut(v, "/")
	i := slices.IndexFunc(provider.All(), func(p provider.Provider) bool { return p.ID == id })
	if i < 0 {
		return nil, "", SearcherGone
	}
	p := provider.All()[i]
	r, _ := namedSearcherRank(p)
	switch {
	case !p.On():
		return &p, "", SearcherOff
	case r < 0:
		return &p, "", SearcherCant
	case provider.KimiCodeSearch(p) != "":
		// it searches with no model
		return &p, "", ""
	case model == codexSearchModel && codexSearches(p):
		// by the ChatGPT backend's search
		return &p, codexSearchModel, ""
	}
	if model != "" && slices.ContainsFunc(searcherModels(p), func(m catalog.Model) bool { return m.ID == model }) && searchableModel(p, model) {
		return &p, model, ""
	}
	if m := searcherModel(p); m != "" {
		return &p, m, ""
	}
	if ms := p.Exposed(); len(ms) > 0 {
		return &p, ms[0].ID, ""
	}
	return &p, "", SearcherNoneOf
}

// Searchers are the providers Settings' Searcher may name: those magpie
// would pick, then relays said to search, each with the model it would
// search with.
func Searchers() []SearcherChoice {
	var out []SearcherChoice
	for _, p := range provider.All() {
		r, manualOnly := namedSearcherRank(p)
		if r < 0 || !p.On() {
			continue
		}
		if provider.KimiCodeSearch(p) != "" {
			out = append(out, SearcherChoice{Provider: p, rank: r, ManualOnly: manualOnly, Service: true})
			continue
		}
		var ms []catalog.Model
		for _, m := range searcherModels(p) {
			if !provider.IsGroupRef(m.ID) && searchableModel(p, m.ID) {
				ms = append(ms, m)
			}
		}
		if len(ms) == 0 {
			continue
		}
		out = append(out, SearcherChoice{Provider: p, Small: searcherModel(p), Models: ms, rank: r, ManualOnly: manualOnly, Own: googleAccount(p) || geminiKeySearches(p), Search: codexSearches(p)})
	}
	slices.SortStableFunc(out, func(a, b SearcherChoice) int { return cmp.Compare(a.rank, b.rank) })
	return out
}

// searcherModels are the models a searcher may be named with: those its
// list gives, then those typed in by hand that it doesn't list. A relay
// with no model list (its models typed in by hand) has none listed, and
// was left out of the picker while the row named it a relay to pick
// (axelburks on Discord).
func searcherModels(p provider.Provider) []catalog.Model {
	avail := p.Available()
	out := avail
	for _, m := range p.ExposedOf(avail) {
		if !slices.ContainsFunc(avail, func(a catalog.Model) bool { return a.ID == m.ID }) {
			if len(out) == len(avail) {
				out = slices.Clone(avail)
			}
			out = append(out, m)
		}
	}
	return out
}

// SearcherChoice is a provider Settings' Searcher may name.
type SearcherChoice struct {
	Provider   provider.Provider
	Small      string // the model it searches with when none is named
	Models     []catalog.Model
	rank       int
	ManualOnly bool // a relay said to search, which only Settings may name
	// Service is a Kimi Code plan, which searches by its search service,
	// with no model (and no Small or Models)
	Service bool
	// Own is a Google sign-in or an AI Studio key, whose Gemini searches
	// for the provider's own models first, whoever is named (ownSearcher)
	Own bool
	// Search is a ChatGPT account, which searches by the ChatGPT backend's
	// search besides its models ("<provider>/@search", codexSearchModel):
	// magpie's own pick, and what it asks when another fails
	Search bool
}

// RelaysSaidToSearch are the providers on that are said to search by
// themselves but are not asked for another model unless Settings names them
// (#359).
func RelaysSaidToSearch() []provider.Provider {
	var out []provider.Provider
	for _, p := range provider.All() {
		if p.Searches && p.On() && searchRank(p) < 0 {
			out = append(out, p)
		}
	}
	return out
}

// smallModel is the provider's cheapest small model of a vendor that
// searches well, or its cheapest, or its first; only among those keep
// holds, when it is given. Only a model the user enabled is picked (what
// agents are offered, Exposed), never one of the rest of the vendor's
// list: OpenRouter turned away an unpicked anthropic/claude-haiku-5.5 for
// the user's region (JonahFu on X).
func smallModel(p provider.Provider, keep func(catalog.Model) bool) string {
	id, _ := smallReady(p, keep, provider.Chat)
	return id
}

// smallReady is smallModel of the models routing would still find an
// account or key with allowance for, asked on from (spentModel), and
// ready true; else smallModel of them all, and ready false. A model out
// of its allowance is only picked when every other is (#1801: Image
// recognition kept describing with an account out of its allowance, each
// image refused 429).
func smallReady(p provider.Provider, keep func(catalog.Model) bool, from provider.Protocol) (string, bool) {
	c, ready := smallReadyOf(p, smallCands(p, keep), from)
	return c.id, ready
}

// smallReadyOf is smallReady of cs, ranked as rankSmall ranks them: the
// first with allowance left, each asked in turn, else the first.
// spentModel weighs every account and key of the provider, and was asked
// of each of its models before one was picked: a quarter of
// AutoVision's allocations.
func smallReadyOf(p provider.Provider, cs []smallCand, from provider.Protocol) (smallCand, bool) {
	if len(cs) == 0 {
		return smallCand{}, false
	}
	now := time.Now()
	for _, c := range cs {
		if _, spent := spentModel(p, c.id, from, now); !spent {
			return c, true
		}
	}
	return cs[0], false
}

// smallCand is a model smallModel may pick, and what it costs: input and
// output a million tokens, 1e9 when it has no price.
type smallCand struct {
	id   string
	cost float64
}

// smallCands is the provider's models smallModel picks among, of those
// keep holds, in the order it picks them (rankSmall). What the user said
// of a model named in full (ImageOverride) is their word that the ID is
// the vendor's own, not one of the shapes guessed at below: their model
// stays in the pool though its ID carries a namespace colon (a relay's
// cn:deepseek-v4.1-flash, #4359) or one of the words, and keep has said
// whether it sees. Only their "it sees" lifts the guess: a "takes no
// images" says nothing of whether the ID is a vendor's model, and keeps
// the guess as it was. An ID that starts a group, a found group or, on a
// subscription, what magpie wrote into its agent, is magpie's own shape
// whatever the user said of it, and stays out.
func smallCands(p provider.Provider, keep func(catalog.Model) bool) []smallCand {
	var ms []catalog.Model
	s := settings.Shared()
	for _, m := range p.Exposed() {
		if keep != nil && !keep(m) {
			continue
		}
		// a subscription's list can have what magpie wrote into its agent
		if strings.HasPrefix(m.ID, provider.GroupPrefix) || strings.HasPrefix(m.ID, provider.AutoPrefix) || (p.Account != nil && strings.Contains(m.ID, "/")) {
			continue
		}
		if sees, said := provider.ImageOverrideIn(s, p.ID, m.ID); !said || !sees {
			l := strings.ToLower(m.ID)
			if strings.ContainsAny(l, ":~") || slices.ContainsFunc([]string{"nano", "lite", "image", "audio", "tts", "embed", "free"}, func(w string) bool { return strings.Contains(l, w) }) {
				continue
			}
		}
		if m.ID == provider.CodexAutoReview {
			// Codex's reviewer, priced free for the reviews it runs, not
			// a model to ask anything else of
			continue
		}
		if m.Price == nil {
			// a subscription's list carries no prices: Codex's starts
			// with its flagship, gpt-6.1-sol, which every search and
			// every image was asked of while gpt-6-luna sat below it
			// (lucy_85908 on Discord). Its list price, as the Usage
			// page counts it, says which is the cheap one
			if pr, ok := p.ListPrice(m.ID); ok {
				m.Price = &pr
			}
		}
		ms = append(ms, m)
	}
	return rankSmall(ms)
}

// smallKept is the providers' smallCands, kept by provider.StampNow:
// AutoVision and the searcher's pick built every provider's list again
// each time they were asked, for every agent's list of models.
var smallKept stamped[smallKey, []smallCand]

// smallKey is what smallCands are of: a provider (an account's, by who is
// signed in) and which keep.
type smallKey struct {
	kind, id, agent, user, plan, home string
}

// keptSmallCands is smallCands, kept (smallKept); kind names keep, which
// must hold of a model by the model and the provider alone.
func keptSmallCands(p provider.Provider, kind string, keep func(catalog.Model) bool) []smallCand {
	k := smallKey{kind: kind, id: p.ID}
	if a := p.Account; a != nil {
		k.agent, k.user, k.plan, k.home = a.Agent, a.User, a.Plan, a.Home
	}
	return smallKept.get(k, func() []smallCand { return smallCands(p, keep) })
}

// smallWords name a vendor's small tier: GPT-6's is Luna, the list has
// no mini (#604).
var smallWords = []string{"haiku", "mini", "flash", "luna"}

// rankSmall is ms in the order smallModel picks them, each one's pick of
// those after it: on a router, a model of the big vendors first; then a
// small one; then the cheaper; then the one listed first.
func rankSmall(ms []catalog.Model) []smallCand {
	type ranked struct {
		smallCand
		router, large bool // of a vendor not big; not small
	}
	rs := make([]ranked, len(ms))
	for i, m := range ms {
		v, _, ok := strings.Cut(m.ID, "/")
		// "gemini" has a "mini" in it, which made every Gemini small and
		// the first of them, a Pro, the one picked (#757)
		l := strings.ReplaceAll(strings.ToLower(m.ID), "gemini", "")
		cost := 1e9
		if m.Price != nil {
			cost = m.Price.Input + m.Price.Output
		}
		rs[i] = ranked{smallCand{m.ID, cost},
			ok && !slices.Contains([]string{"anthropic", "openai", "google", "x-ai"}, v),
			!slices.ContainsFunc(smallWords, func(w string) bool { return strings.Contains(l, w) })}
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		if c := falseFirst(a.router, b.router); c != 0 {
			return c
		}
		if c := falseFirst(a.large, b.large); c != 0 {
			return c
		}
		return cmp.Compare(a.cost, b.cost)
	})
	out := make([]smallCand, len(rs))
	for i, r := range rs {
		out[i] = r.smallCand
	}
	return out
}

// falseFirst orders false before true.
func falseFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// searcherModel is the model p searches with when none is named: its
// small model, of those it can search with.
func searcherModel(p provider.Provider) string {
	m, _ := searcherReady(p)
	return m
}

// searcherReady is searcherModel, and whether it has allowance left
// (smallReady).
func searcherReady(p provider.Provider) (string, bool) {
	// what searchableModel says of a model is the model's and the
	// provider's list's, which keptSmallCands is kept by
	var keep func(catalog.Model) bool
	if googleAccount(p) || geminiKeySearches(p) || p.IsRemoteMagpie() || partSearcher(p) {
		keep = func(m catalog.Model) bool { return searchableModel(p, m.ID) }
	}
	c, ready := smallReadyOf(p, keptSmallCands(p, "search", keep), provider.Anthropic)
	return c.id, ready
}

// searchTool is the tool a model that can't search is given, under a name
// the client's own tools leave free.
func searchTool(tools []Tool) Tool {
	name := "web_search"
	for slices.ContainsFunc(tools, func(t Tool) bool { return t.Name == name }) {
		name = "magpie_" + name
	}
	return Tool{Name: name,
		Description: "Search the web for current information: news, releases, documentation, prices, anything after your training or that you are unsure of. Returns what the pages found say, with their URLs. Cite the URLs you use.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"}},"required":["query"]}`)}
}

const searchSystem = "You are a web search tool. Search the web for what is asked and report what the results say: the facts that answer it, as specifically as they are given (numbers, dates, versions, names), each with the title and URL of its page. Report only what the pages say; don't answer from memory, don't add advice. Be concise."

// webSearch searches the web and says what it found, and on which pages:
// for a model of a Kimi Code plan, with the plan's search service first;
// then with the searcher's model, or, without one or when it fails, with
// the search APIs the user set up (#419). Settings can put the search APIs
// first (#928): the providers then search only when none of them answers.
// With provider search off, only the search APIs are asked.
func (s *Server) webSearch(ctx context.Context, query string) (string, []Hit, error) {
	// a search is magpie's own request, not the turn's provider's: the
	// search APIs go through the global proxy and each searcher through
	// its own (Provider.Via), never through the proxy the turn's provider
	// is set to (#1522: Codex asked while a "direct" provider served the
	// turn dialed chatgpt.com directly)
	ctx = netproxy.With(ctx, "")
	ctx, query = searchScoped(ctx, query)
	if !searchServed() {
		return "", nil, errSearchOff
	}
	if settings.Load().Searcher == "off" {
		if len(provider.SearchAPIs()) == 0 {
			return "", nil, errNoSearcher
		}
		return s.apiSearch(ctx, query)
	}
	var errs []error
	apiFirst := settings.Load().SearchFirst == settings.SearchFirstAPI && len(provider.SearchAPIs()) > 0
	if apiFirst {
		said, hits, err := s.apiSearch(ctx, query)
		if err == nil {
			return said, hits, nil
		}
		errs = append(errs, err)
	}
	if p, ok := ownSearcher(ctx); ok {
		said, hits, err := s.ownSearch(ctx, p, query)
		if err == nil {
			return said, hits, nil
		}
		errs = append(errs, err)
	}
	said, hits, err := s.modelSearch(ctx, query)
	if err == nil {
		return said, hits, nil
	}
	apis := len(provider.SearchAPIs()) > 0
	// that no provider searches is news only when nothing else was tried
	if !errors.Is(err, errNoSearcher) || len(errs) == 0 && !apis {
		errs = append(errs, err)
	}
	if !apis || apiFirst {
		return "", nil, errors.Join(errs...)
	}
	said, hits, err = s.apiSearch(ctx, query)
	if err != nil {
		return "", nil, errors.Join(append(errs, err)...)
	}
	return said, hits, nil
}

var errNoSearcher = errors.New("no provider that can search the web, nor a search API, is set up in magpie")

// errSearchOff is a search asked of magpie while plugins/search is off.
var errSearchOff = errors.New("magpie's web search is off: turn the Web search plugin on in magpie's Settings → Plugins")

// searchServed is whether plugins/search serves magpie's web search here
// (the gateway's tool point, gwtool): off, magpie neither offers its
// web_search nor answers one, and a vendor that searches by itself still
// does.
func searchServed() bool { return gwtool.Served(gwtool.WebSearch) }

// searchersInTurn is how many searchers a search is asked of, in turn,
// before it goes to the search APIs.
const searchersInTurn = 3

// modelSearch searches the web with the searcher's model; one magpie
// picked that fails or finds nothing gives way to the next searcher magpie
// would pick, all of them within searchWaitMost. One Settings names gives
// way to those named after it (SearcherThen), and, when it can't serve at
// all — out of its allowance, refused for its quota, credit or rate
// (errCantServe) — to the searchers magpie would pick in turn: a Codex
// account named to search for Cursor's models, used up, broke every
// search of the turns Cursor took over from it (pedestrianz, plugins#635).
// One that answered without searching or found nothing gives way to no
// other: the search APIs, when set up, search in its place (webSearch),
// and Settings says it didn't search (SearcherFailed). Handed on, a search
// for Grok, picked, went to Codex's gpt-6-astra, a pricier model the user
// hadn't picked, on every search (Gearclickist on Discord).
func (s *Server) modelSearch(ctx context.Context, query string) (string, []Hit, error) {
	p, model, ok := searcher()
	if !ok {
		return "", nil, errNoSearcher
	}
	ctx, cancel := sideDeadline(context.WithValue(ctx, searchingKey{}, true), "search", searchTimeout, searchWaitMost)
	defer cancel()
	tried := []searchPick{{p.ID, model}}
	var said string
	var hits []Hit
	// one magpie picked that is out of its allowance isn't asked: it
	// would only be refused again (#1801); one the Settings name is, and
	// its refusal, as one out of its allowance, hands it on (errCantServe)
	err := searcherSpent(p, model)
	cp, _, why := chosenSearcher()
	named := why == "" && cp != nil && cp.ID == p.ID
	if named {
		err = nil
	}
	if err == nil {
		said, hits, err = s.searchBy(ctx, p, model, query, searchEffort(p, model))
	}
	if named {
		// those Settings names after it, in their order (SearcherThen),
		// then, when it can't serve, those magpie would pick
		instead, first := "", err
		if err != nil && ctx.Err() == nil {
			for _, v := range settings.Load().SearcherThen {
				q, m, why := namedSearcher(v)
				if why != "" || q == nil || q.ID == p.ID && m == model || ctx.Err() != nil {
					continue
				}
				if instead == "" {
					ctx = searchingInstead(ctx, p, model, first)
				}
				instead = searcherOf(*q, m)
				tried = append(tried, searchPick{q.ID, m})
				var next error
				if said, hits, next = s.searchBy(ctx, *q, m, query, searchEffort(*q, m)); next == nil {
					err = nil
					break
				}
				err = errors.Join(err, next)
			}
			if err != nil && errors.Is(first, errCantServe) && ctx.Err() == nil {
				if instead == "" {
					ctx = searchingInstead(ctx, p, model, first)
				}
				var by string
				said, hits, by, err = s.searchInTurn(ctx, query, tried, err, len(tried)+searchersInTurn-1)
				if err == nil {
					instead = by
				}
			}
			if err == nil {
				// what Settings says is the first's failure, and who
				// searched in its place
				if !errors.Is(ctx.Err(), context.Canceled) {
					namedFailed(p, model, first, instead)
				}
				return said, hits, nil
			}
		}
		// a search the agent stopped waiting for is no failure of its.
		// Settings says the first's own failure, those after it having
		// failed too
		if !errors.Is(ctx.Err(), context.Canceled) {
			namedFailed(p, model, first, "")
		}
		if err != nil {
			switch {
			case errors.Is(first, errCantServe):
				return "", nil, fmt.Errorf("%w (no other provider could search in its place)", err)
			case len(settings.Load().SearcherThen) > 0:
				return "", nil, fmt.Errorf("%w (the searchers picked in Settings › Web search, in their order: no other provider searches in their place)", err)
			}
			return "", nil, fmt.Errorf("%w (the searcher picked in Settings › Web search: no other provider searches in its place)", err)
		}
		return said, hits, nil
	}
	if err != nil && ctx.Err() == nil {
		ctx = searchingInstead(ctx, p, model, err)
		said, hits, _, err = s.searchInTurn(ctx, query, tried, err, searchersInTurn)
	}
	return said, hits, err
}

// turnModel is what c searches with when magpie picks it: a ChatGPT
// account by the backend's search, another by its small model.
func turnModel(c SearcherChoice) string {
	if c.Search {
		return codexSearchModel
	}
	return c.Small
}

// searchPick is a searcher a search was asked of.
type searchPick struct{ id, model string }

// searchInTurn asks the searchers magpie would pick (Searchers), those
// with allowance left first, of a provider not tried yet, until one
// searches or most have been tried in all: what it found, and which
// searched (searcherOf). err is the failures so far, which the new ones
// join.
func (s *Server) searchInTurn(ctx context.Context, query string, tried []searchPick, err error, most int) (string, []Hit, string, error) {
	var turn, spent []SearcherChoice
	for _, c := range Searchers() {
		if c.ManualOnly || c.Small == "" {
			continue
		}
		if searcherSpent(c.Provider, turnModel(c)) != nil {
			spent = append(spent, c)
		} else {
			turn = append(turn, c)
		}
	}
	for _, c := range append(turn, spent...) {
		if ctx.Err() != nil || len(tried) >= most {
			break
		}
		if slices.ContainsFunc(tried, func(t searchPick) bool { return t.id == c.Provider.ID }) {
			continue
		}
		m := turnModel(c)
		tried = append(tried, searchPick{c.Provider.ID, m})
		next := searcherSpent(c.Provider, m)
		if next == nil {
			var said string
			var hits []Hit
			if said, hits, next = s.searchBy(ctx, c.Provider, m, query, searchEffort(c.Provider, m)); next == nil {
				return said, hits, searcherOf(c.Provider, m), nil
			}
		}
		err = errors.Join(err, next)
	}
	return "", nil, "", err
}

// errCantServe is a searcher that can't serve a search now: every account
// or key of it out of its allowance (searcherSpent), or the search refused
// for its quota, credit or rate (searchWith). It is the provider's state,
// not what it does with a search, so another searches in its place.
var errCantServe = errors.New("can't search now")

// cantServe is err, a searcher's own error, said to be errCantServe
// without changing what it says.
type cantServe struct{ error }

func (c cantServe) Unwrap() []error { return []error{c.error, errCantServe} }

// lastNamedFail is the last search the searcher Settings names didn't
// make, for Settings to say (SearcherFailed); cleared when it searches.
var lastNamedFail struct {
	sync.Mutex
	f SearchFailure
}

// SearchFailure is a search the searcher Settings names didn't make: why,
// and when.
type SearchFailure struct {
	Searcher     string // "<provider>/<model>", or "<provider>"
	Why          string // the error, when it neither found nothing nor didn't search
	NothingFound bool   // it searched and named no page
	NotSearched  bool   // it answered without searching (errNotSearched)
	// Instead is the searcher Settings names after it (SearcherThen) that
	// searched in its place, "" when none did
	Instead string
	At      time.Time
}

// searcherOf is a searcher as Settings writes it: "<provider>/<model>",
// or "<provider>" for one that searches with no model.
func searcherOf(p provider.Provider, model string) string {
	if model == "" {
		return p.ID
	}
	return p.ID + "/" + model
}

func namedFailed(p provider.Provider, model string, err error, instead string) {
	name := searcherOf(p, model)
	lastNamedFail.Lock()
	defer lastNamedFail.Unlock()
	if err == nil {
		if lastNamedFail.f.Searcher == name {
			lastNamedFail.f = SearchFailure{}
		}
		return
	}
	f := SearchFailure{Searcher: name, At: time.Now(), NotSearched: errors.Is(err, errNotSearched), Instead: instead}
	f.NothingFound = !f.NotSearched && errors.Is(err, errFoundNothing)
	if !f.NothingFound && !f.NotSearched {
		f.Why = strings.TrimPrefix(err.Error(), name+": ")
	}
	lastNamedFail.f = f
}

// SearcherFailed is the last search the searcher Settings names now didn't
// make, since it last searched; ok false when there is none.
func SearcherFailed() (SearchFailure, bool) {
	p, m, why := chosenSearcher()
	if p == nil || why != "" {
		return SearchFailure{}, false
	}
	name := p.ID
	if m != "" {
		name += "/" + m
	}
	lastNamedFail.Lock()
	defer lastNamedFail.Unlock()
	f := lastNamedFail.f
	return f, f.Searcher != "" && f.Searcher == name
}

// searcherSpent is why a searcher's model isn't asked: every account or
// key of it is out of its allowance (spentModel). nil when it has
// allowance, or searches with no model (a Kimi Code plan's service, a
// ChatGPT account by the backend's search).
func searcherSpent(p provider.Provider, model string) error {
	if model == "" || model == codexSearchModel || provider.KimiCodeSearch(p) != "" {
		return nil
	}
	back, spent := spentModel(p, model, provider.Anthropic, time.Now())
	if !spent {
		return nil
	}
	when := ""
	if !back.IsZero() {
		when = " until " + heldClock(back, time.Now())
	}
	return cantServe{fmt.Errorf("%s/%s: out of its allowance%s, not asked", p.ID, model, when)}
}

// searchingInstead is ctx with its web search's CallFor saying that the
// searcher asked first, p's model, failed with err, so the searches made
// with ctx are in its place.
func searchingInstead(ctx context.Context, p provider.Provider, model string, err error) context.Context {
	f := searchFor(ctx)
	if f == nil {
		f = &CallFor{}
	}
	g := *f
	g.Instead = p.ID
	if model != "" {
		g.Instead += "/" + model
	}
	g.Picked = false
	if c, _, why := chosenSearcher(); why == "" && c != nil && c.ID == p.ID {
		g.Picked = true
	}
	g.NotSearched = errors.Is(err, errNotSearched)
	g.NothingFound = !g.NotSearched && errors.Is(err, errFoundNothing)
	g.Why = ""
	if !g.NothingFound && !g.NotSearched {
		g.Why = strings.TrimPrefix(err.Error(), g.Instead+": ")
	}
	return context.WithValue(ctx, searchForKey{}, &g)
}

// errFoundNothing is a searcher's reply that names no page it found.
var errFoundNothing = errors.New("found nothing")

// errNotSearched is a searcher's reply with no search in it at all: the
// vendor ran no search, the tool dropped or left unused, and the model
// answered from what it knew (Gearclickist on Discord: grok-4.7 answered
// in 1.4k tokens, the size of a request with no search tool, where one
// that searched took 6k-66k). It is found nothing too.
var errNotSearched = fmt.Errorf("answered without searching the web: %w", errFoundNothing)

// searchWith asks one searcher's model to search: what it said, with the
// pages its searches found. A reply with no page found and no address in
// what it says found nothing: the model answered without searching (#669:
// DeepSeek on its Chat API, which has no search tool, said it couldn't, or
// wrote its own tool-call markup as text).
//
// effort is the level its model is asked to think at, "" for none sent:
// the searcher's is picked in Settings (searchEffort), and a provider
// searching for its own models is sent none.
func (s *Server) searchWith(ctx context.Context, p provider.Provider, model, query, effort string) (string, []Hit, error) {
	body, _ := json.Marshal(searchBody(p, model, query, effort))
	r, err := http.NewRequestWithContext(ownAsk(magpieChose(ctx)), http.MethodPost, "http://magpie/v1/messages", nil)
	if err != nil {
		return "", nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Anthropic, body)
	var out struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Content   json.RawMessage `json:"content"`
			Citations []struct {
				URL       string `json:"url"`
				CitedText string `json:"cited_text"`
			} `json:"citations"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		if t, ok := sideTimeout(ctx); ok {
			msg = t.Error()
		}
		err := fmt.Errorf("%s/%s: %s", p.ID, model, msg)
		switch failure(w.Code, w.Body.Bytes()) {
		case failQuota, failCredit, failRate:
			err = cantServe{err}
		}
		return "", nil, err
	}
	var text strings.Builder
	var sources []string
	var found []Hit
	quoted := map[string]string{} // what the searcher's citations quote of each page
	searched := false
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
			for _, c := range b.Citations {
				if c.URL != "" && c.CitedText != "" && quoted[c.URL] == "" {
					quoted[c.URL] = c.CitedText
				}
			}
		case "server_tool_use":
			searched = true
		case "web_search_tool_result":
			searched = true
			// each result read on its own, its title, address and age
			// only: a vendor's encrypted_content is for that vendor, and
			// an error ({"type":"web_search_tool_result_error"}) has none
			var results []json.RawMessage
			json.Unmarshal(b.Content, &results)
			for _, raw := range results {
				var h struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					PageAge any    `json:"page_age"`
				}
				if json.Unmarshal(raw, &h) != nil || h.URL == "" {
					continue
				}
				age, _ := h.PageAge.(string)
				sources = append(sources, "- "+h.Title+" — "+h.URL)
				found = append(found, Hit{Title: h.Title, URL: h.URL, PageAge: age})
			}
		}
	}
	for i := range found {
		found[i].Cited = quoted[found[i].URL]
	}
	said := strings.TrimSpace(text.String())
	if said == "" || (len(found) == 0 && !strings.Contains(said, "://")) {
		if !searched {
			return "", nil, fmt.Errorf("%s/%s %w", p.ID, model, errNotSearched)
		}
		return "", nil, fmt.Errorf("%s/%s %w", p.ID, model, errFoundNothing)
	}
	if len(sources) > 0 && len(sources) <= 20 {
		said += "\n\nSources:\n" + strings.Join(sources, "\n")
	}
	return said, found, nil
}

// searchBody is the Anthropic Messages request the searcher's model is
// asked to search with. A level of reasoning is asked as Claude Code asks
// it, a thinking budget with output_config.effort beside it, which the
// gateway fits to the vendor the model is on: adaptive thinking for a
// Claude that takes only that (adaptiveThinking), reasoning_effort, a
// Gemini thinking level. A Claude before adaptive thinking is sent the
// budget alone, as output_config.effort isn't its; "none" turns thinking
// off.
func searchBody(p provider.Provider, model, query, effort string) map[string]any {
	q := map[string]any{
		"model": p.ID + "/" + model, "max_tokens": replyRoom(4096, effort), "stream": false, "system": searchSystem,
		"messages": []map[string]any{{"role": "user", "content": "Search the web for: " + query}},
		"tools":    []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": 3}},
	}
	switch effort {
	case "":
	case "none":
		q["thinking"] = map[string]any{"type": "disabled"}
	default:
		budget := 1024 // Anthropic's least
		if effort != "minimal" {
			budget = budgetOf(effortOf(effort))
		}
		q["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		vendor := model
		if _, m, ok := provider.Resolve(p.ID + "/" + model); ok {
			vendor = m
		}
		if _, _, claude := claudeVersionOf(vendor); !claude || adaptiveOnly(vendor) {
			q["output_config"] = map[string]any{"effort": effort}
		}
	}
	return q
}

// SearchAgent is the User-Agent of the searches magpie makes for a model.
const SearchAgent = "magpie-search/1"

// round asks the model once and gives its reply's events, or the status
// and message of a failure.
type round func(ctx context.Context, req *Request) (<-chan Event, int, string)

// textCallRound is a round whose reply has the calls written into its
// text made calls (textCallSee), as the replies of the routes without a
// search have them (streamTranslated, forwardTranslated): every search
// route's every round passes here, whatever protocol the client asked
// on, and a model that writes its calls out had them reach the client
// as the text they were (#4562).
func textCallRound(tools []Tool, ask round) round {
	names := textCallNames(tools)
	if names == nil {
		return ask
	}
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		in, status, msg := ask(ctx, req)
		if in == nil {
			return in, status, msg
		}
		out := make(chan Event, 16)
		t := &textCallSee{names: names, see: func(ev Event) {
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}}
		go func() {
			defer close(out)
			for ev := range in {
				select {
				case <-ctx.Done():
				default:
					t.event(ev)
				}
			}
			// a round that ended without its stop still hands on the
			// text held from a mark begun, as the routes without a
			// search do at their reply's end
			t.release()
		}()
		return out, status, msg
	}
}

// searchReply answers req through ask, running the searches its model
// asks for (when search) and the advisor calls (req.Advisor,
// advisor_serve.go), and asking it again with what they gave; the client
// sees one reply. The first round's failure is a status, as another
// provider may take over.
func (s *Server) searchReply(w http.ResponseWriter, r *http.Request, from provider.Protocol, name string, req *Request, usage *Usage, ask round, search bool) (int, string) {
	ctx, cancel := context.WithCancel(withWebAccess(context.WithValue(r.Context(), searchForKey{}, &CallFor{Agent: callerOf(r).agent, Model: unprefixed(req.Model)}), req.WebAccess))
	defer cancel()
	q := *req
	q.Tools = slices.Clone(q.Tools)
	var tool Tool
	if search {
		q.WebSearch = false
		tool = searchTool(q.Tools)
		q.Tools = append(q.Tools, tool)
	}
	if q.Advisor != "" {
		q.Tools = append(q.Tools, advisorToolDef())
	}
	q.Messages = slices.Clone(q.Messages)
	ask = textCallRound(q.Tools, ask)
	first, status, msg := ask(ctx, &q)
	if first == nil {
		return writeError(w, from, status, msg), msg
	}
	events := make(chan Event, 16)
	go func() {
		defer close(events)
		s.searchRounds(ctx, &q, tool.Name, first, ask, events)
	}()
	return relay(w, r, from, name, req, events, usage, cancel, nil, func(string, string, bool) {})
}

func (s *Server) searchRounds(ctx context.Context, q *Request, tool string, in <-chan Event, ask round, out chan<- Event) {
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var before, this Usage
	started, said := false, false
	ours := func(name string) bool {
		return name != "" && name == tool || q.Advisor != "" && name == advisorTool
	}
	advised := 0 // the advisor's calls so far
	for n := 1; ; n++ {
		var col collector
		mine, theirs, gap := false, false, said
		var stop string
		for ev := range in {
			if ev.Kind == KAlive {
				// nothing of the reply: no part of what it collects
				if !send(ev) {
					return
				}
				continue
			}
			col.add(ev)
			switch ev.Kind {
			case KStart:
				// a round's prompt is told, the rounds before billed as
				// the reply's Deferred (after)
				this = ev.Usage
				ev.Usage = ev.Usage.after(before, false)
				ev.Usage.restart = started
				if started {
					ev = Event{Kind: KUsage, Usage: ev.Usage}
				}
				started = true
			case KUsage:
				this.add(ev.Usage)
				ev.Usage = ev.Usage.carryUpdate(this, before)
			case KText:
				if gap && ev.Text != "" {
					// what it says after a search goes on from what it
					// said before it
					ev.Text = "\n\n" + strings.TrimLeft(ev.Text, "\n")
					gap = false
				}
				said = said || ev.Text != ""
			case KToolStart:
				mine = ours(ev.Name)
				theirs = theirs || !mine
				if mine {
					continue
				}
			case KToolArgs:
				if mine {
					continue
				}
			case KStop:
				stop = ev.Stop
				continue
			case KError:
				send(ev)
				return
			}
			if !send(ev) {
				return
			}
		}
		res := col.finish()
		var calls []Part
		var at []int // where in the reply each call is
		for i, p := range res.Parts {
			if p.Kind == ToolCall && ours(p.Name) {
				if p.ID == "" {
					kind := "search"
					if p.Name == advisorTool && p.Name != tool {
						kind = advisorTool
					}
					res.Parts[i].ID = fmt.Sprintf("call_%s_%d_%d", kind, n, i)
				}
				calls = append(calls, res.Parts[i])
				at = append(at, i)
			}
		}
		// a reply that calls the client's tools goes to the client: it
		// can't answer a search, and doesn't see the ones left out
		if len(calls) == 0 || theirs || n > searchRounds+1 {
			if stop == "" || (len(calls) > 0 && !theirs) {
				stop = "stop"
			}
			send(Event{Kind: KStop, Stop: stop})
			return
		}
		results := make([]Part, len(calls))
		searches := make([]Event, len(calls))
		var wg sync.WaitGroup
		for i, c := range calls {
			if c.Name == advisorTool && c.Name != tool {
				// the advisor, in order, each seeing what the model
				// said before it
				advised++
				results[i], searches[i] = s.adviceRound(ctx, q, res.Parts[:at[i]], c, advised)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = Part{Kind: ToolResult, CallID: c.ID}
				if n > searchRounds {
					results[i].Text, results[i].IsError = "No more searches: answer with what was found.", true
					return
				}
				var a struct {
					Query string `json:"query"`
				}
				json.Unmarshal(c.Args, &a)
				if strings.TrimSpace(a.Query) == "" {
					results[i].Text, results[i].IsError = "query is empty", true
					return
				}
				found, hits, err := s.webSearch(context.WithValue(ctx, searchCallKey{}, c.ID), a.Query)
				if err != nil {
					results[i].Text, results[i].IsError = "search failed: "+err.Error(), true
					return
				}
				results[i].Text = found
				searches[i] = Event{Kind: KSearch, Text: a.Query, Hits: hits}
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		// the client hears of the searches as its provider's own
		for _, ev := range searches {
			if (ev.Kind == KSearch || ev.Kind == KAdvice) && !send(ev) {
				return
			}
		}
		q.Messages = append(q.Messages, Message{Role: "assistant", Parts: res.Parts}, Message{Role: "user", Parts: results})
		before, this = before.plus(this, false), Usage{}
		var status int
		var msg string
		if in, status, msg = ask(ctx, q); in == nil {
			send(Event{Kind: KError, Text: fmt.Sprintf("%s (%d)", msg, status)})
			return
		}
	}
}

// askTranslated is a round for a provider asked on to.
func (s *Server) askTranslated(p provider.Provider, to provider.Protocol, model string, in http.Header, reply http.Header) round {
	first := true
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		q := *req
		q.Stream = true
		res, actual, err := s.forwardTranslated(ctx, p, to, &q, model, in)
		if err != nil {
			return nil, 502, p.Name + ": " + err.Error()
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if first {
				keepRetry(reply, res.Header, b)
			}
			return nil, res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status)
		}
		first = false
		to = actual
		rd, sse := eventStream(res)
		if !sse {
			b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
			res.Body.Close()
			return nil, 502, p.Name + " " + didNotStream + ": " + provider.APIError(b, "unexpected reply")
		}
		events := make(chan Event, 16)
		// read here, on the request's goroutine: the reader below can
		// outlive the request (#5973)
		every, gap := keepaliveEvery, keepaliveGap
		go func() {
			defer close(events)
			defer res.Body.Close()
			dec := decoderFor(p, actual, q.Tools)
			send := func(ev Event) error {
				select {
				case events <- ev:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var failed error
			told := time.Now() // when the reader was last sent anything
			// the reader hears that the reply goes on while the
			// provider says nothing at all, as streamTranslated's client
			// does (keepThroughSilence)
			var mu sync.Mutex
			heard, stopKeep := keepThroughSilence(every, &mu, func() {
				if failed == nil && time.Since(told) >= every/2 {
					failed = send(Event{Kind: KAlive})
					told = time.Now()
				}
			})
			err := readSSEAlive(rd, func(_, data string) error {
				mu.Lock()
				defer mu.Unlock()
				heard()
				if err := dec(data, func(ev Event) {
					if failed == nil {
						failed = send(ev)
						told = time.Now()
					}
				}); err != nil {
					return err
				}
				return failed
			}, func() {
				mu.Lock()
				defer mu.Unlock()
				heard()
				// a provider still queued or thinking — keepalives,
				// response.in_progress — is heard from with nothing to
				// translate: the reader hears that it goes on (#2316)
				if failed == nil && time.Since(told) >= gap {
					failed = send(Event{Kind: KAlive})
					told = time.Now()
				}
			})
			stopKeep()
			if err != nil && failed == nil && ctx.Err() == nil {
				send(Event{Kind: KError, Text: p.Name + ": " + err.Error()})
			}
		}()
		return events, 0, ""
	}
}
