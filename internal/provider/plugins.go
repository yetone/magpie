package provider

import (
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

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
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

// PluginID is the id magpie gives the provider OpenCode calls id: the same,
// unless a preset or a built-in subscription has it (google, openai,
// anthropic), or a provider of the user's own made before the plugin was
// installed (zenmux, #867), when it is id-plugin — but for a built-in moved
// onto the plugin, whose id the plugin has now.
func PluginID(id string) string {
	if Moved(id) {
		return id
	}
	if slices.Contains(accountIDs, id) || Preset(id) != nil || id == "magpie" || customIDs()[id] {
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
		for _, p := range load().Providers {
			if p.Chat != "" || p.Responses != "" || p.Anthropic != "" || p.Decide != "" {
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

// PluginProvider is the id OpenCode knows the plugin's provider p is by
// (grok, kiro), whatever magpie names it; "" for one no plugin gives.
func (p Provider) PluginProvider() string {
	if !p.IsPlugin() {
		return ""
	}
	return p.Account.plugin.ID
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
			out = append(out, pluginProvider(pp, ls[0]))
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

func pluginModel(pp plugin.Provider, id string) (plugin.Model, bool) {
	for _, m := range pp.Models {
		if m.ID == id {
			return m, true
		}
	}
	return plugin.Model{}, false
}

func pluginCatalog(pp plugin.Provider) []catalog.Model {
	out := make([]catalog.Model, 0, len(pp.Models))
	for _, m := range pp.Models {
		c := catalog.Model{
			ID: m.ID, Name: m.Name, Provider: pp.ID, Released: m.Released,
			APIs: []string{string(pluginProtocol(pp.ID, m))}, Images: m.Image,
			Context: m.Input, Output: m.Output, Free: m.Free,
			Rate: m.Rate, RateWas: m.RateWas,
		}
		if c.Context == 0 {
			c.Context = m.Context
		} else if m.Context > 0 && m.Context < c.Context {
			// the prompt cap a plugin names wins, but never above the
			// window it also names: a row with input over context
			// (opencode/hy3-free's 192K over 190K) was served past its
			// window (#1286)
			c.Context = m.Context
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
		if m.Reasoning {
			c.Efforts, c.Reasoning = m.Variants, true
		}
		// a built-in moved onto its plugin keeps the levels it had for a
		// model its vendor gives none: its maker's, as effortsOf borrows.
		// So does Cline's plugin, which resells others' models as the
		// ClinePass built-in does and gives no levels of its own: signed in
		// through it, ClinePass showed none where its API key showed them
		if len(c.Efforts) == 0 && (Moved(pp.ID) || pp.ID == "cline") {
			c.Efforts = borrowedEfforts(m.ID)
		}
		// OpenCode's price of a model it has none for is 0, as the
		// plugins give a plan's models: a price is only one above it
		if m.Cost != nil && (m.Cost.Input > 0 || m.Cost.Output > 0) {
			c.Price = &catalog.Price{Input: m.Cost.Input, Output: m.Cost.Output}
		}
		if m.ImageSaid {
			c.ImageInput = &m.Image
		}
		out = append(out, c)
	}
	return out
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
	acct, user := l.acct, l.User
	id := PluginID(pp.ID)
	name := pp.Name
	if name == "" {
		name = pp.ID
	}
	a := &Account{Agent: "plugin", User: user, Plan: l.Plan, Stream: true, plugin: &pp, pluginKey: acct.Key}
	if pp.ID == "grok" {
		// Codex's namespaced tools go to Grok flat, as the built-in sends
		// them (#404): the plugin's own rewrite would leave them out
		a.body = grokBody
	}
	if pp.ID == "zed" {
		// an OpenAI model's request as Zed's cloud reads it: Codex's
		// developer messages as system ones, its namespaced tools flat
		a.body = ZedBody
	}
	a.models = func() []catalog.Model {
		if cur, ok := PluginOf(id); ok {
			return pluginAccountCatalog(cur, acct.Key)
		}
		return pluginAccountCatalog(pp, acct.Key)
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
				return catalog.Chat(pluginAccountCatalog(cur, acct.Key)), nil
			}
		}
		return nil, fmt.Errorf("%s's plugin no longer lists it", name)
	}
	a.sign = func(ctx context.Context, req *http.Request, body []byte) error { return nil }
	a.transport = func(req *http.Request) (*http.Response, error) { return pluginFetch(pp, acct.Key, req) }
	p := Provider{ID: id, Name: name, Icon: PluginIcon(pp), Account: a}
	if c, ok := movedCards[pp.ID]; ok && Moved(pp.ID) {
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
		if o, err := plugin.LoaderOptions(ctx, pp.ID, p.Account.pluginKey); err == nil {
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
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
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
	base := firstOf(o.BaseURL, m.URL, pp.API, npmBase[m.NPM])
	if base == "" {
		return nil, errors.New(pp.Name + "'s plugin says nowhere to send its requests")
	}
	base = strings.TrimRight(base, "/")
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

// notePluginSignIn marks or clears an account's lapse as the plugin's
// answer says, and takes SignInHeader off it.
func notePluginSignIn(pp plugin.Provider, account string, resp *http.Response) {
	said := strings.ToLower(strings.TrimSpace(resp.Header.Get(SignInHeader)))
	resp.Header.Del(SignInHeader)
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

func withModel(body []byte, model string) []byte {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	v["model"], _ = json.Marshal(model)
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	return body
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
func (p Provider) Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if p.Account != nil && p.Account.transport != nil {
		return p.Account.transport(req)
	}
	if p.Account != nil && p.Account.clientFor != nil {
		if c := p.Account.clientFor(req); c != nil {
			client = c
		}
	}
	req.Header.Del(ConversationHeader)
	return client.Do(req)
}
