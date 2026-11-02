package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/settings"
)

// A plugin's provider (see internal/plugin) is a subscription like the
// built-in ones: signed in to through the plugin, its models the ones the
// plugin lists, its requests made by the plugin's fetch. It speaks what
// OpenCode would speak to each model, by the AI SDK package the model is
// spoken to with.

// pluginBase is where a plugin provider's requests seem to go, for the
// gateway: its transport takes them to where the plugin says instead.
const pluginBase = "plugin://"

// ConversationHeader carries the conversation a request belongs to, to a
// plugin's chat.headers hook as its session; it goes no further.
const ConversationHeader = "X-Magpie-Conversation"

// WorkDirHeader and WorkGitHeader tell a plugin's fetch where the agent
// that asked works, as the agent says it in its request: the folder,
// percent-encoded (decodeURIComponent reads it), and "yes" or "no" for
// whether it is in a git repository. Each is left off when it isn't
// known. The plugin host takes them off whatever the plugin sends on
// (host.js), so they reach no vendor as headers.
const (
	WorkDirHeader = "X-Magpie-Work-Dir"
	WorkGitHeader = "X-Magpie-Work-Git"
)

// FallbackHeader, "1", tells a plugin's fetch that magpie needn't have the
// answer waited for: a request its vendor queues (Qoder's free models)
// comes back at once with the queue's 503 and Retry-After instead of being
// waited out for minutes. Without it the plugin waits. A model's test
// sends it, so a queued model's test says it is queued instead of running
// out of time. The plugin host takes it off whatever the plugin sends on
// (host.js), as it does the two above.
const FallbackHeader = "X-Magpie-Fallback"

// SessionToPluginHeader and SubagentToPluginHeader tell a plugin's fetch
// the session the agent named (sessionOf: Claude Code's
// x-claude-code-session-id and the rest) and the Claude Code subagent the
// request is of (x-claude-code-agent-id). The agent's own headers never
// reach a plugin, so a plugin that keeps state per conversation (Devin's
// cache keepalive) had only the request's text to go by, and Claude Code
// Auto's permission check, whose history grows in its one merged user
// message, looked like a new conversation each time
// (magpie-community/plugins#620). Each is left off when the agent named
// none. The plugin host takes them off whatever the plugin sends on, as it
// does the ones above.
const (
	SessionToPluginHeader  = "X-Magpie-Session"
	SubagentToPluginHeader = "X-Magpie-Subagent"
)

// warmPlugin starts p's plugin host and runs its account's loader, which
// may renew the sign-in, under ctx and through the account's proxy: what
// a plugin's first fetch waits for besides its vendor. A failure is left
// for the fetch to meet and say.
func (p Provider) warmPlugin(ctx context.Context) {
	if !p.IsPlugin() {
		return
	}
	id := p.Account.plugin.ID
	if cur, ok := PluginOf(p.ID); ok {
		id = cur.ID
	}
	_, _ = plugin.LoaderOptions(p.Via(ctx), id, p.Account.pluginKey)
}

// PluginID is the id magpie gives the provider OpenCode calls id: the same,
// unless a preset or a built-in subscription has it (google, openai,
// anthropic), or a provider of the user's own made before the plugin was
// installed (zenmux, #867), or "auto" or "group", which start the ids of
// routing groups, when it is id-plugin — but for a built-in moved
// onto the plugin, whose id the plugin has now.
func PluginID(id string) string {
	if PluginServes(id) {
		return id
	}
	if slices.Contains(accountIDs, id) || Preset(id) != nil || id == "magpie" || id == autoID || id == strings.TrimSuffix(GroupPrefix, "/") || customIDs()[id] {
		return id + "-plugin"
	}
	return id
}

// customIDs are the ids of the user's own providers, those with an endpoint
// in providers.json: a signed-in account with one of them would be hidden
// behind it, not listed (allProviders).
func customIDs() map[string]bool {
	return heldOf("customIDs", func() map[string]bool {
		ids := map[string]bool{}
		for _, p := range storedFile().Providers {
			if hasEndpoint(p) {
				ids[p.ID] = true
			}
		}
		return ids
	})
}

// subscriptionID is whether id is a subscription's, a built-in's or an
// installed plugin's, signed in or not: a provider of the user's own never
// takes it, or it would hide that subscription once signed in.
func subscriptionID(id string) bool {
	if slices.Contains(accountIDs, id) {
		return true
	}
	_, ok := PluginOf(id)
	return ok
}

// IsPlugin is whether the provider is a plugin's.
func (p Provider) IsPlugin() bool { return p.Account != nil && p.Account.plugin != nil }

// PluginForTest is the provider id served by the plugin's provider
// pluginID, for a test to build a candidate of.
func PluginForTest(id, pluginID string) Provider {
	pp := plugin.Provider{ID: pluginID}
	return Provider{ID: id, Account: &Account{Agent: "plugin", User: id, plugin: &pp}}
}

// PluginProvider is the id OpenCode knows the plugin's provider p is by
// (grok, kiro), whatever magpie names it; "" for one no plugin gives.
func (p Provider) PluginProvider() string {
	if !p.IsPlugin() {
		return ""
	}
	return p.Account.plugin.ID
}

// OpenAIBase is where p takes the OpenAI-shaped requests that are no chat
// (embeddings, rerank, speech, transcriptions): its Chat base, and a
// plugin's fetch under plugin://<id>/v1 whatever API its chat models are
// spoken on, for the plugin to answer or put in its vendor's shape
// (XYenon's Google AI Studio plugin: Gemini's embedContent, its TTS).
func (p Provider) OpenAIBase() string {
	if p.IsPlugin() {
		return pluginBase + p.Account.plugin.ID + "/v1"
	}
	return p.Base(Chat)
}

// PluginOpenAICompatible is whether p is a plugin's provider that speaks
// model on an OpenAI-compatible API (@ai-sdk/openai-compatible), whose
// other routes, the images API among them, its fetch sends under the same
// base: SenseNova's plugin at token.sensenova.cn/v1. A plugin of a vendor
// with an API of its own (Cursor's, Kiro's) speaks another package.
func (p Provider) PluginOpenAICompatible(model string) bool {
	if !p.IsPlugin() {
		return false
	}
	pp := *p.Account.plugin
	if cur, ok := PluginOf(p.ID); ok {
		pp = cur
	}
	m, ok := pluginModel(pp, model)
	return ok && firstOf(m.NPM, pp.NPM) == "@ai-sdk/openai-compatible"
}

// PluginOf is the plugin provider magpie's provider id is, when it is one.
func PluginOf(id string) (plugin.Provider, bool) {
	for _, pp := range heldPlugins() {
		if PluginID(pp.ID) == id {
			return pp, true
		}
	}
	return plugin.Provider{}, false
}

// pluginAccounts are the plugins' providers signed in to, each as its
// first account.
func pluginAccounts() []Provider {
	var out []Provider
	for _, pp := range heldPlugins() {
		if movingNow(pp.ID) {
			continue // shown once the move is through (migrate.go)
		}
		if ls := pluginLogins(pp); len(ls) > 0 {
			// the provider offers what any account switched on serves, as
			// the requests reach each through its own list (#2554)
			keys := []string{ls[0].acct.Key}
			for _, l := range ls[1:] {
				if l.On {
					keys = append(keys, l.acct.Key)
				}
			}
			out = append(out, pluginProviderServing(pp, ls[0], keys))
		}
	}
	return out
}

// pluginProtocol is the API model is spoken to on, as OpenCode's AI SDK
// package for it speaks: Anthropic's, OpenAI's Responses (and Copilot's
// for GPT-5 and after), Gemini's (as Code Assist's inner request), else
// chat completions.
func pluginProtocol(provider string, m plugin.Model) Protocol {
	switch m.NPM {
	case "@ai-sdk/anthropic", "@ai-sdk/google-vertex/anthropic":
		return Anthropic
	case "@ai-sdk/openai", "@ai-sdk/azure":
		return Responses
	case "@ai-sdk/google":
		return CodeAssist
	case "@ai-sdk/github-copilot":
		return copilotAPI(modelAPIID(m))
	}
	if strings.HasPrefix(provider, "github-copilot") {
		return copilotAPI(modelAPIID(m))
	}
	return Chat
}

var gptN = regexp.MustCompile(`^gpt-(\d+)`)

// copilotAPI is OpenCode's shouldUseCopilotResponsesApi: GPT-5 and after,
// but for gpt-5-mini, on Responses.
func copilotAPI(model string) Protocol {
	if m := gptN.FindStringSubmatch(model); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 5 && !strings.HasPrefix(model, "gpt-5-mini") {
			return Responses
		}
	}
	return Chat
}

// npmBase is where the AI SDK package sends a model by default.
var npmBase = map[string]string{
	"@ai-sdk/anthropic":      "https://api.anthropic.com/v1",
	"@ai-sdk/openai":         "https://api.openai.com/v1",
	"@ai-sdk/google":         "https://generativelanguage.googleapis.com/v1beta",
	"@ai-sdk/github-copilot": "https://api.githubcopilot.com",
	"@ai-sdk/xai":            "https://api.x.ai/v1",
	"@ai-sdk/mistral":        "https://api.mistral.ai/v1",
	"@ai-sdk/groq":           "https://api.groq.com/openai/v1",
	"@ai-sdk/deepseek":       "https://api.deepseek.com/v1",
}

// pluginModel is the model of pp's list by the id id, which is the
// plugin's own or one the user lists it by at magpie's provider of pp
// (SetModelID): what the plugin says of its model (its API, tiers, price)
// holds under either.
func pluginModel(pp plugin.Provider, id string) (plugin.Model, bool) {
	for _, m := range pp.Models {
		if m.ID == id {
			return m, true
		}
	}
	if len(settings.SharedModelIDs()) == 0 {
		return plugin.Model{}, false
	}
	if own := modelIDsOf(PluginID(pp.ID)).ownOf(id); own != id {
		for _, m := range pp.Models {
			if m.ID == own {
				return m, true
			}
		}
	}
	return plugin.Model{}, false
}

// PluginTiers are the service tiers p's plugin says its vendor serves
// model at (m.serviceTiers, #2514): none for a provider not a plugin's, or
// a model it doesn't list.
func PluginTiers(p Provider, model string) []string {
	if !p.IsPlugin() {
		return nil
	}
	pp, ok := PluginOf(p.ID)
	if !ok {
		return nil
	}
	m, _ := pluginModel(pp, model)
	return m.ServiceTiers
}

// pluginTierOn reports whether the plugin says its vendor serves m at tier,
// on a Chat or Responses API, where the gateway sends it as service_tier
// (forwardTranslated); a model on another API is sent no tier.
func pluginTierOn(provider string, m plugin.Model, tier string) bool {
	to := pluginProtocol(provider, m)
	return (to == Chat || to == Responses) && slices.Contains(m.ServiceTiers, tier)
}

func pluginCatalog(pp plugin.Provider) []catalog.Model {
	out := make([]catalog.Model, 0, len(pp.Models))
	for _, m := range pp.Models {
		if !pluginTalks(m) {
			continue
		}
		c := catalog.Model{
			ID: m.ID, Name: m.Name, Provider: pp.ID, Released: m.Released,
			APIs: []string{string(pluginProtocol(pp.ID, m))}, Images: m.Image,
			Context: catalog.Window(m.Input), Output: m.Output, Free: m.Free,
			Rate: m.Rate, RateWas: m.RateWas,
			// the model's own sampling, as its plugin said it (#2492)
			Temperature: m.Temperature,
			// a decision model the plugin marked counts only where it
			// serves a decision API (pp.Decide)
			Decides: pp.Decide && m.Decides,
			// the tiers it says its vendor serves the model at (#2514)
			Tiers: m.ServiceTiers,
		}
		// a limit under catalog.LeastWindow (512, H20 on Discord) is none:
		// the other one, or models.dev, answers (ListedWindow)
		if window := catalog.Window(m.Context); c.Context == 0 {
			c.Context = window
		} else if window > 0 && window < c.Context {
			// the prompt cap a plugin names wins, but never above the
			// window it also names: a row with input over context
			// (opencode/hy3-free's 192K over 190K) was served past its
			// window (#1286)
			c.Context = window
		}
		// Cursor's own ids no catalog knows: one not named a 1M model
		// holds what the catalog knows its base to, as the built-in's did
		if pp.ID == "cursor" && c.Context <= cursorDefaultContext {
			if n := cursorContext(m.ID, m.Name); c.Context == 0 || n < c.Context {
				c.Context = n
			}
		}
		if c.Name == "" {
			c.Name = m.ID
		}
		// a model that searches the web by itself, asked in plain words
		if m.Searches {
			c.WebSearch = "native"
		}
		if m.Reasoning {
			c.Efforts, c.Reasoning = m.Variants, true
		}
		// a built-in moved onto its plugin keeps the levels it had for a
		// model its vendor gives none: its maker's, as effortsOf borrows.
		// So does Cline's plugin, which resells others' models as the
		// ClinePass built-in does and gives no levels of its own: signed in
		// through it, ClinePass showed none where its API key showed them
		// And any plugin's model it says thinks but gives no variants of:
		// OpenCode reads no variants as nothing said, and makes its own
		// for the model, so the plugin hasn't said it has none. A
		// reseller's id models.dev knows under its maker's spelling
		// (Raccoon's sn-kimi-k3 for kimi-k3) counted as known
		// (levelsUnknown) with nothing borrowed, so Codex had no effort to
		// pick (laomao04_65777 on Discord); a custom provider's same id
		// borrows them (effortsOf)
		if len(c.Efforts) == 0 && (m.Reasoning || PluginServes(pp.ID) || pp.ID == "cline") {
			c.Efforts = borrowedEfforts(m.ID)
		}
		// OpenCode's price of a model it has none for is 0, as the
		// plugins give a plan's models: a price is only one above it
		if pr, ok := pluginPrice(m); ok {
			c.Price = &pr
		}
		if m.ImageSaid {
			c.ImageInput = &m.Image
		}
		// what else it takes (audio, video, pdf), where someone said it:
		// the plugin's word stands whether or not it spoke of images
		// (Command Code's plans, #4436); the host's default for a model
		// said nothing of is text alone and is nobody's word (#1595)
		if m.InputsSaid {
			c.InputModalities = m.Inputs
		}
		c.OutputModalities = m.Outputs
		out = append(out, c)
	}
	return out
}

// pluginTalks is whether m is a model to talk with, as its plugin says
// what it gives: one that gives text, or images or videos (whose place is
// #2792's to decide, a drawing model kept where it is until then), or one
// said nothing of. An embedding ({"text": false}) and a TTS (audio alone)
// are not, and stay out of every chat list, as the catalog's own lists
// leave them out (textModel), #3279. Nothing is guessed from its id: a
// plugin that says nothing of its outputs keeps every model it lists.
func pluginTalks(m plugin.Model) bool {
	if !m.OutputsSaid {
		return true
	}
	return slices.ContainsFunc(m.Outputs, func(k string) bool { return k == "text" || k == "image" || k == "video" })
}

// pluginPrice is m's price as its plugin gives it (m.Cost), if it gives
// one: OpenCode's price of a model it has none for is 0, so only an input
// or output price above it is one. A cache price of 0 is none given.
func pluginPrice(m plugin.Model) (catalog.Price, bool) {
	if m.Cost == nil || (m.Cost.Input <= 0 && m.Cost.Output <= 0) {
		return catalog.Price{}, false
	}
	return catalog.Price{Input: m.Cost.Input, Output: m.Cost.Output, CacheRead: m.Cost.Cache.Read, CacheWrite: m.Cost.Cache.Write}, true
}

// pluginAccountCatalog is what the account at key serves: its own list
// when the plugin told one, as the built-ins read each account's.
func pluginAccountCatalog(pp plugin.Provider, key string) []catalog.Model {
	all := pluginCatalog(pp)
	for _, a := range pp.Accounts {
		if a.Key == key && a.Models != nil {
			return slices.DeleteFunc(all, func(m catalog.Model) bool { return !slices.Contains(a.Models, m.ID) })
		}
	}
	return all
}

// PluginListError is why the plugin couldn't list the account at key's
// models, "" when it did or kept the list it had: what it shows instead
// is its short defaults (Cursor's Auto alone).
func PluginListError(pp plugin.Provider, key string) string {
	for _, a := range pp.Accounts {
		if a.Key == key {
			if a.FellBack {
				return a.ListError
			}
			return ""
		}
	}
	return ""
}

// ListError is why a plugin's account lists only the plugin's defaults,
// "" for any other provider.
func (p Provider) ListError() string {
	if !p.IsPlugin() {
		return ""
	}
	pp := *p.Account.plugin
	if cur, ok := PluginOf(p.ID); ok {
		pp = cur
	}
	return PluginListError(pp, p.Account.pluginKey)
}

// pluginServedCatalog is what the accounts at keys serve between them,
// in the catalog's order: an account the plugin told no list of, or
// doesn't know, serves all of it.
func pluginServedCatalog(pp plugin.Provider, keys []string) []catalog.Model {
	all := pluginCatalog(pp)
	listed := map[string]bool{}
	for _, k := range keys {
		i := slices.IndexFunc(pp.Accounts, func(a plugin.Account) bool { return a.Key == k })
		if i < 0 || pp.Accounts[i].Models == nil {
			return all
		}
		for _, m := range pp.Accounts[i].Models {
			listed[m] = true
		}
	}
	return slices.DeleteFunc(all, func(m catalog.Model) bool { return !listed[m.ID] })
}

// pluginLists is whether a plugin's account serves model, as the plugin
// last told its list; one it told none of serves all of the provider's.
func (a *Account) pluginLists(model string) bool {
	pp := *a.plugin
	if cur, ok := PluginOf(PluginID(pp.ID)); ok {
		pp = cur
	}
	for _, ac := range pp.Accounts {
		if ac.Key == a.pluginKey && ac.Models != nil {
			return slices.Contains(ac.Models, model)
		}
	}
	return true
}

// pluginProvider is the provider as one of its accounts, l as the
// accounts list has it.
func pluginProvider(pp plugin.Provider, l pluginLogin) Provider {
	return pluginProviderServing(pp, l, nil)
}

// pluginProviderServing is pluginProvider offering the models the
// accounts at keys serve between them; nil keys, l's own.
func pluginProviderServing(pp plugin.Provider, l pluginLogin, keys []string) Provider {
	acct, user := l.acct, l.User
	serves := func(cur plugin.Provider) []catalog.Model {
		if keys == nil {
			return pluginAccountCatalog(cur, acct.Key)
		}
		return pluginServedCatalog(cur, keys)
	}
	id := PluginID(pp.ID)
	name := pp.Name
	if name == "" {
		name = pp.ID
	}
	a := &Account{Agent: "plugin", User: user, Plan: l.Plan, Stream: true, plugin: &pp, pluginKey: acct.Key}
	if pp.ID == "grok" {
		// Codex's namespaced tools go to Grok flat, as the built-in sends
		// them (#404): the plugin's own rewrite would leave them out; and
		// a plain agent_message goes as the user's message, as the plugin
		// itself sends it from 0.1.11. A lone tool_choice "none" stays,
		// and so does a tool_choice object Grok's backend doesn't take (an
		// allowed_tools names the client's tools), for the plugin to read
		// (grokPluginBody, #3110, #3153)
		a.body = grokPluginBody
	}
	if pp.ID == "zed" {
		// an OpenAI model's request as Zed's cloud reads it: Codex's
		// developer messages as system ones, its namespaced tools flat
		a.body = ZedBody
	}
	a.models = func() []catalog.Model {
		if cur, ok := PluginOf(id); ok {
			return serves(cur)
		}
		return serves(pp)
	}
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ps, err := plugin.Providers(ctx)
		if err != nil {
			return nil, err
		}
		for _, cur := range ps {
			if cur.ID == pp.ID {
				// a Refresh that got the plugin's short defaults back says
				// why, rather than "1 models" (gnayiab on X: Cursor in WSL
				// listing Auto alone)
				if e := PluginListError(cur, acct.Key); e != "" {
					return nil, fmt.Errorf("%s couldn't list its models: %s", name, e)
				}
				return catalog.Chat(serves(cur)), nil
			}
		}
		return nil, fmt.Errorf("%s's plugin no longer lists it", name)
	}
	a.sign = func(ctx context.Context, req *http.Request, body []byte) error { return nil }
	a.transport = func(req *http.Request) (*http.Response, error) { return pluginFetch(pp, acct.Key, req) }
	p := Provider{ID: id, Name: name, Icon: PluginIcon(pp), Account: a}
	if c, ok := movedCards[pp.ID]; ok && PluginServes(pp.ID) {
		p.Name, p.Icon, p.Website = c.name, c.icon, c.site // as the built-in was
		m, _ := MigrationOf(pp.ID)
		a.moved, a.wasHost = true, m.Host
		if a.wasHost == "" { // moved before the move kept it
			a.wasHost = builtinHost(pp.ID)
		}
		if n, ok := movedNames[pp.ID]; ok {
			p.Name = n
		}
	}
	for _, m := range pp.Models {
		switch pluginProtocol(pp.ID, m) {
		case Chat:
			p.Chat = pluginBase + pp.ID + "/v1"
		case Responses:
			p.Responses = pluginBase + pp.ID + "/v1"
		case Anthropic:
			p.Anthropic = pluginBase + pp.ID
		case CodeAssist:
			a.codeAssist = pluginBase + pp.ID
		}
	}
	if pp.Decide {
		// its decision API is the provider's own base, asked at
		// /systemone through the plugin's fetch: pluginFetch sends nowhere
		// but the loader's host, so an address of the plugin's own
		// couldn't be reached (#1514)
		p.Decide = pluginBase + pp.ID + "/v1"
	}
	// a plugin in front of Anthropic's or OpenAI's API says its upstream
	// searches, as a relay is said to (#2027): its client's web_search goes
	// to it as sent, on the APIs it has an address for (searchesItself)
	p.Searches = pp.Searches
	return p
}

// pluginAPIs is the one API a plugin provider's model is spoken to on.
func (p Provider) pluginAPIs(model string) []Protocol {
	pp := p.Account.plugin
	if cur, ok := PluginOf(p.ID); ok {
		pp = &cur
	}
	if m, ok := pluginModel(*pp, model); ok {
		return []Protocol{pluginProtocol(pp.ID, m)}
	}
	return nil
}

// Origin is the scheme and host p's requests go to, for a URL of the
// vendor's own outside its API (WorkBuddy's /v3/config beside its /v2): a
// plugin's where its loader sends them, which p.Do then takes there through
// the plugin; any other's its API's. "" when it has none.
func (p Provider) Origin(ctx context.Context) string {
	base := ""
	if p.IsPlugin() {
		pp := *p.Account.plugin
		if cur, ok := PluginOf(p.ID); ok {
			pp = cur
		}
		base = pp.API
		// the loader may renew the sign-in: through the account's proxy,
		// as its requests go (#2777)
		if o, err := plugin.LoaderOptions(p.Via(ctx), pp.ID, p.Account.pluginKey); err == nil {
			base = firstOf(o.BaseURL, pp.API)
		}
	} else {
		for _, pr := range p.Speaks() {
			if base = p.Base(pr); base != "" {
				break
			}
		}
	}
	scheme, rest, ok := strings.Cut(strings.TrimSpace(base), "://")
	if !ok || HostOf(rest) == "" {
		return ""
	}
	return scheme + "://" + HostOf(rest)
}

// pluginFetch sends a request the gateway made for a plugin's provider
// through the plugin: to the base URL its loader gave (else the model's,
// the provider's, the AI SDK package's), with what the loader adds.
func pluginFetch(pp plugin.Provider, account string, req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var body []byte
	if req.Body != nil {
		// read into the length said: io.ReadAll grows from 512 bytes, so a
		// 1 MB request was some 3 MB read (#3472)
		buf := bytes.NewBuffer(make([]byte, 0, max(req.ContentLength, 0)+bytes.MinRead))
		_, err := buf.ReadFrom(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = buf.Bytes()
	}
	rest := strings.TrimPrefix(req.URL.String(), pluginBase+pp.ID)
	model := bodyModel(body)
	codeAssist := strings.HasPrefix(rest, "/v1internal:")
	if codeAssist {
		// Code Assist's envelope off: Gemini's own request, the model in
		// the path, as @ai-sdk/google sends it
		var env struct {
			Model   string          `json:"model"`
			Request json.RawMessage `json:"request"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		model, body = env.Model, env.Request
	}
	m, ok := pluginModel(pp, model)
	if !ok {
		if cur, found := PluginOf(PluginID(pp.ID)); found {
			pp = cur
			m, ok = pluginModel(pp, model)
		}
	}
	if !ok {
		m = plugin.Model{ID: model, NPM: pp.NPM}
	}
	api := modelAPIID(m)
	if api != model && !codeAssist {
		body = withModel(body, api)
	}
	o, err := plugin.LoaderOptions(ctx, pp.ID, account)
	if err != nil {
		return nil, err
	}
	base := pluginRequestBase(pp, m, o.BaseURL)
	if base == "" {
		return nil, errors.New(pp.Name + "'s plugin says nowhere to send its requests")
	}
	var url string
	switch {
	case codeAssist:
		url = base + "/models/" + api + ":streamGenerateContent?alt=sse"
	case strings.HasPrefix(rest, "/v1/"):
		// chat, responses and Anthropic's messages: the AI SDK's base
		// ends where magpie's /v1 does
		url = base + strings.TrimPrefix(rest, "/v1")
	case strings.HasPrefix(rest, "https://") || strings.HasPrefix(rest, "http://"):
		// a URL of the vendor's own outside the base (Provider.Origin's),
		// signed by the plugin as its other requests: only at the host the
		// plugin sends to, never anywhere else with the account's sign-in
		if HostOf(rest) != HostOf(base) {
			return nil, fmt.Errorf("%s's plugin sends to %s, not %s", pp.Name, HostOf(base), HostOf(rest))
		}
		url = rest
	default:
		url = base + rest
	}
	h := map[string]string{}
	for k, vs := range req.Header {
		if len(vs) > 0 && !strings.EqualFold(k, ConversationHeader) {
			h[strings.ToLower(k)] = vs[0]
		}
	}
	resp, err := plugin.Fetch(ctx, plugin.FetchRequest{
		Provider: pp.ID, Account: account, Model: api, NPM: m.NPM, URL: url, Method: req.Method,
		Headers: h, Body: body, Session: req.Header.Get(ConversationHeader),
	})
	if err == nil {
		notePluginSignIn(pp, account, resp)
	}
	return resp, err
}

// pluginRequestBase is the base URL a plugin provider's request for m goes
// under: the loader's (else the model's, the provider's, the AI SDK
// package's), with no slash at its end.
//
// A loader's base is the provider's, said once for all its models, and an
// Anthropic model may be served under /v1 of it: Copilot's loader says
// https://api.githubcopilot.com, while its Claude models are on
// /v1/messages (models.dev's api.url .../v1; OpenCode's own Copilot
// provider sends them to the loader's base + "/v1"). Sent to the bare base
// they went to /messages and Copilot answered 404 (cyoking1 on X). So an
// Anthropic model whose own URL ends in /v1 is sent under the loader's
// base + /v1, when that base doesn't end in it already.
func pluginRequestBase(pp plugin.Provider, m plugin.Model, loader string) string {
	base := strings.TrimRight(firstOf(loader, m.URL, pp.API, npmBase[m.NPM]), "/")
	if base == "" || loader == "" || pluginProtocol(pp.ID, m) != Anthropic {
		return base
	}
	if strings.HasSuffix(strings.TrimRight(m.URL, "/"), "/v1") && !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// SignInHeader is how a plugin says what its answer means for the
// account's sign-in, whatever its status, so it can answer with the
// status its built-in did: "expired" marks the account lapsed, as a
// built-in whose vendor refused the sign-in marked it, though the status
// be a 502; "kept" leaves the account as it is, as a built-in answering a
// 401 of the vendor's without its sign-in refused did; "renewed" clears
// the mark, as a built-in whose sign-in renewed took it off whatever the
// request then met. Without it a 401 marks the account and a success
// clears the mark.
const SignInHeader = "X-Magpie-Sign-In"

// ModelUnserved are how a vendor says the model isn't one it serves this
// key, or this way — words another provider, or key, may not answer with
// (the gateway's unservedWords, read decoded: catalog.Words): ModelScope's
// 400 for a model id none of its instances serves, "Model id : … , has no
// provider supported" (#2887), and Azure OpenAI's DeploymentNotFound, whose
// message "The API deployment for this resource does not exist" a relay
// passes on as a 400 (its 404 fails over by its status already).
var ModelUnserved = regexp.MustCompile(`(?i)model.{0,80}(not (supported|accessible|available|found|enabled|allowed)|unsupported|does ?n[o']t exist|unknown|invalid)|(no such|unknown|invalid|unsupported) model|model_not_found|has no provider supported|deployment[ _]?not[ _]?found|deployment.{0,60}does ?n[o']t exist|模型.{0,12}(不存在|不支持|无权|未开通)`)

// signInWords are words of a refusal about the sign-in itself, which a
// model named beside them doesn't make the model's: "model gpt-x: invalid
// token" is the token.
var signInWords = regexp.MustCompile(`(?i)token|api.?key|credential|sign.?in|log.?in|expired|session|令牌|密钥|登录|过期`)

// modelOnly401 says a 401 a plugin answered without SignInHeader is about
// the model, not the account's sign-in: its body says the model isn't
// served (ModelUnserved) and nothing of the sign-in. A plugin answering
// "this model isn't available to you" with 401 marked every account the
// request failed over to as needing a new sign-in (plugins#468), for a
// credential that was fine. Only the start of the body is read, and given
// back whole for the gateway to read as before.
func modelOnly401(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized || resp.Body == nil {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	if err != nil || len(head) == 0 {
		return false
	}
	words := catalog.Words(head)
	return ModelUnserved.Match(words) && !signInWords.Match(words)
}

// notePluginSignIn marks or clears an account's lapse as the plugin's
// answer says, and takes SignInHeader off it. A 401 it says nothing of
// that is about the model alone leaves the account as it is (kept).
func notePluginSignIn(pp plugin.Provider, account string, resp *http.Response) {
	said := strings.ToLower(strings.TrimSpace(resp.Header.Get(SignInHeader)))
	resp.Header.Del(SignInHeader)
	if said == "" && modelOnly401(resp) {
		said = "kept"
	}
	notePluginSaid(pp, account, said, resp.StatusCode)
}

// notePluginSaid marks or clears an account's lapse as the plugin said
// ("expired", "kept", "renewed"), or, when it said nothing, as status
// reads: a 401 marks it and a success clears it.
func notePluginSaid(pp plugin.Provider, account, said string, status int) {
	switch said {
	case "expired":
		notePluginLapse(pp, account, http.StatusUnauthorized)
	case "renewed":
		notePluginLapse(pp, account, http.StatusOK)
	case "kept":
	default:
		notePluginLapse(pp, account, status)
	}
}

func init() {
	// a plugin's model list is asked through the proxy of the account it
	// is the list of, as a built-in's is fetched through the account's
	plugin.ProxyFor = func(id, key string) string {
		mid := PluginID(id)
		if key == "" {
			return ProxyOf(mid)
		}
		for _, l := range readLogins() {
			if l.Agent == "plugin:"+id && l.Home == key {
				return ProxyOfLogin(mid, l.User)
			}
		}
		return ProxyOf(mid)
	}
	// a plugin provider switched off in magpie isn't asked its models:
	// Off means no request goes to it (ChangQiu on Discord)
	plugin.ListingOff = pluginsOff
	// a models hook saying its account's sign-in expired marks it, as a
	// built-in whose model list the vendor refused marked the account
	plugin.OnSignIn(func(id, account, said string) {
		pp, ok := pluginOfAgent("plugin:" + id)
		if !ok {
			// told while the providers were first read: wait for them
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, _ = plugin.Providers(ctx)
			if pp, ok = pluginOfAgent("plugin:" + id); !ok {
				pp = plugin.Provider{ID: id}
			}
		}
		notePluginSaid(pp, account, said, 0)
	})
}

func modelAPIID(m plugin.Model) string {
	if m.APIID != "" {
		return m.APIID
	}
	return m.ID
}

// withModel is body asking for model.
func withModel(body []byte, model string) []byte {
	if b, ok := replaceModel(body, model); ok {
		return b
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil || v == nil {
		return body
	}
	v["model"], _ = json.Marshal(model)
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	return body
}

// replaceModel is body with its one top-level "model", a string, replaced
// by model and every other byte as it came; false when body isn't JSON or
// has no such "model". Decoding a body and marshalling it again to ask
// a plugin for its model id allocated ten times a 45 MB request (#4004).
func replaceModel(body []byte, model string) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return nil, false
	}
	n := 0
	for _, k := range gjson.GetBytes(body, "@keys").Array() {
		if k.Str == "model" {
			n++
		}
	}
	was := gjson.GetBytes(body, "model")
	at := was.Index
	if n != 1 || was.Type != gjson.String || at <= 0 {
		return nil, false
	}
	name, _ := json.Marshal(model)
	out := make([]byte, 0, len(body)-len(was.Raw)+len(name))
	out = append(out, body[:at]...)
	out = append(out, name...)
	return append(out, body[at+len(was.Raw):]...), true
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Do sends req, through the account's own transport when it has one (a
// plugin's), the client the account asks for this request when it asks
// for one (ZCode's Start Plan: zcodeStartClient), and client otherwise.
//
// A request sent for magpie's own test (probelog.With) is written into the
// request log here, whichever way it goes: a plugin's fetch and a client
// of the account's own pass no transport that would.
func (p Provider) Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if ctx := req.Context(); probelog.Pending(ctx) {
		start := time.Now()
		req = req.WithContext(probelog.Taken(ctx))
		res, err := p.do(client, req)
		status := 0
		if err == nil {
			status = res.StatusCode
		}
		probelog.Note(ctx, req.URL.String(), req.Method, start, status, err)
		return res, err
	}
	return p.do(client, req)
}

// SendsThroughPlugin, set by the gateway, is told of each request handed
// to a plugin's account, which goes past the gateway's own transport, as
// it goes, and what came back for it: the call's headers both ways, for
// Recent calls (gateway's wire_headers.go).
var SendsThroughPlugin func(id string, req *http.Request) func(*http.Response, error)

func (p Provider) do(client *http.Client, req *http.Request) (*http.Response, error) {
	if p.Account != nil && p.Account.transport != nil {
		if SendsThroughPlugin == nil {
			return p.Account.transport(req)
		}
		done := SendsThroughPlugin(p.ID, req)
		res, err := p.Account.transport(req)
		done(res, err)
		return res, err
	}
	if p.Account != nil && p.Account.clientFor != nil {
		if c := p.Account.clientFor(req); c != nil {
			client = c
		}
	}
	req.Header.Del(ConversationHeader)
	return client.Do(req)
}

// pluginsOff is the plugins' own ids of the plugin providers switched off
// in magpie, which a listing of the plugins asks nothing: a plugin's id
// is magpie's (PluginID) as it is, or with "-plugin" taken off. It reads
// providers.json alone, never the plugins' list, which a listing is
// asking for.
func pluginsOff() []string {
	var out []string
	for _, p := range storedFile().Providers {
		if !p.Off {
			continue
		}
		for _, id := range []string{p.ID, strings.TrimSuffix(p.ID, "-plugin")} {
			if id != "" && PluginID(id) == p.ID && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}
