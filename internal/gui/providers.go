package gui

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
	"github.com/yetone/magpie/internal/upstream"
)

// The providers page: the vendors the user added, the presets they can add
// with one key, the gateway that fronts them, and who is routed where.

type modelJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`              // the user's name for it, if they gave one
	Default  string   `json:"default,omitempty"` // its own name, when the user gave it another
	Kept     []string `json:"kept,omitempty"`    // the reasoning levels the user keeps of Efforts, when not all
	Efforts  []string `json:"efforts,omitempty"`
	Given    bool     `json:"given,omitempty"`     // its levels aren't known: Efforts are those it can be given, Kept those it was
	Images   bool     `json:"images"`              // agents are told it can see images
	ImageSet bool     `json:"imageSet,omitempty"`  // the user said so, rather than its vendor
	Own      bool     `json:"ownImages,omitempty"` // its vendor's answer, which a staged Restore default shows
	On       bool     `json:"on"`                  // exposed to agents
	Context  int      `json:"context,omitempty"`   // the window agents are told: the user's, else Listed
	Output   int      `json:"output,omitempty"`    // the reply limit agents are told (provider.ReplyLimit)
	Listed   int      `json:"listed,omitempty"`    // its window before the user's: its vendor's list's, else models.dev's
	Max      int      `json:"max,omitempty"`       // the most its context may be set to, above Listed
	Free     bool     `json:"free,omitempty"`      // costs the subscription nothing
	Rate     float64  `json:"rate,omitempty"`      // the credits a request costs the subscription, as a multiple
	RateWas  float64  `json:"rateWas,omitempty"`   // the rate before a discount running now
	API      string   `json:"api,omitempty"`       // the one API the user said it is asked on
	Auto     []string `json:"auto,omitempty"`      // the APIs its vendor's list says it is served on, what Auto asks it on
	Same     string   `json:"same,omitempty"`      // the model the user said it is the same as, for the groups magpie finds (#583)
	Merge    string   `json:"merge,omitempty"`     // what those groups merge it by when the user says nothing
	// what it costs, USD per million tokens (#819): the price the user set
	// for it, and its list price, its vendor's else its maker's, before the
	// provider's price rate
	Price *catalog.Price `json:"price,omitempty"`
	List  *catalog.Price `json:"list,omitempty"`
}

type providerJSON struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon"`
	Preset    string            `json:"preset"`
	Host      string            `json:"host"`
	Chat      string            `json:"chat"`
	Responses string            `json:"responses"`
	Anthropic string            `json:"anthropic"`
	Decide    string            `json:"decide,omitempty"` // a decision API: it only routes groups
	Catalog   string            `json:"catalog"`
	Website   string            `json:"website"`
	KeysURL   string            `json:"keysUrl"`
	Headers   map[string]string `json:"headers,omitempty"`
	// the API the user gave a custom provider's Base URL as (BaseAPI)
	BaseAPI string `json:"baseAPI,omitempty"`
	// the vendor searches the web by itself (provider.Searches)
	Searches bool `json:"searches"`
	// on the Cline API: its DeepSeek models are served by DeepSeek's own
	// API alone (provider.PinUpstream), which only it offers
	Cline       bool `json:"cline,omitempty"`
	PinUpstream bool `json:"pinUpstream"`
	// on this machine or the local network, set to go unmasked by
	// Settings' redaction (provider.Unredacted)
	Unredacted bool `json:"unredacted"`
	// the proxy its requests go through: "" the global one, "direct"
	// none, or an address (#237)
	Proxy string `json:"proxy"`
	// the proxy of each of a subscription's accounts that has one of its
	// own, by its name in lower case; the others follow Proxy
	AccountProxies map[string]string `json:"accountProxies,omitempty"`
	// the models each account or key the user narrowed serves alone, by
	// its name in lower case or its key's id (#474); the others serve all
	AccountModels map[string][]string `json:"accountModels,omitempty"`
	// the usage cap each account the user capped is held at, in percent
	// of its windows, by its name in lower case (provider.AccountCaps)
	AccountCaps map[string]int `json:"accountCaps,omitempty"`
	// where a custom provider's balance is asked (see provider.Balance)
	BalanceURL  string `json:"balanceURL,omitempty"`
	BalancePath string `json:"balancePath,omitempty"`
	ModelsURL   string `json:"modelsURL,omitempty"`
	// an account-wide balance token (provider.BalanceToken): whether the
	// vendor takes one, and whether one is saved; never the token itself
	BalanceToken struct {
		Takes bool `json:"takes"`
		Set   bool `json:"set"`
	} `json:"balanceToken"`
	// a StepFun provider's Step Plan windows, told only to a platform
	// sign-in: which site's, and whether one is kept
	StepPlan *stepPlanJSON `json:"stepPlan,omitempty"`
	// a Zhipu or Z.ai key's team, for a team's GLM Coding Plan (#236):
	// set, if empty, for those providers alone, which the editor asks it of
	ZhipuTeam *provider.ZhipuTeam `json:"zhipuTeam,omitempty"`
	// ModelTest is why its models can't each be sent a test request, ""
	// when they can (provider.ModelTest): the editor says so on a chip's
	// right-click rather than offer no menu
	ModelTest string `json:"modelTest,omitempty"`
	// DecideTest is set when its decision models can each be sent a
	// System One question (provider.AsksDecideModels): a mixed
	// provider's Jev too, beside its conversation models
	DecideTest bool `json:"decideTest,omitempty"`
	// Deciders are its decision models when it lists them apart from its
	// chat models (OpenRouter's): those, and no Jev-named chat model
	Deciders []string `json:"deciders,omitempty"`

	Key struct {
		Set      bool   `json:"set"`
		Masked   string `json:"masked"`
		Optional bool   `json:"optional"`
	} `json:"key"`
	Ready    bool     `json:"ready"`
	Chosen   []string `json:"chosen"`   // the user's explicit picks, if any
	Fallback []string `json:"fallback"` // where requests go when this one can't take them
	Routing  string   `json:"routing"`  // how requests spread over its keys or accounts
	Affinity string   `json:"affinity"` // how long a conversation stays with who answered it
	// Sink: a key or account rate limited with quota left goes to the
	// back of the order (provider.Provider.Sink)
	Sink bool `json:"sink,omitempty"`
	// KeepLogin: magpie keeps Codex or Claude Code signed in to the first
	// account rather than moving it on when that runs low (#524)
	KeepLogin bool `json:"keepLogin,omitempty"`
	// KeepLoginAs: the account it is kept signed in to instead of the
	// first, the gateway still trying them in their order
	KeepLoginAs string `json:"keepLoginAs,omitempty"`
	// how many requests each of its keys or accounts has out at once, the
	// rest queued: the user's (null: not set), and what its plugin says
	// when the user set none (provider.Concurrency)
	MaxConcurrency    *int `json:"maxConcurrency"`
	PluginConcurrency int  `json:"pluginConcurrency,omitempty"`
	// each key's or account's own limit over it (#892), by its name in
	// lower case or its key id; 0 there is none
	AccountConcurrency map[string]int `json:"accountConcurrency,omitempty"`
	// how many may wait for each key or account, and for how many seconds
	// (0: no bound)
	QueueLimit int `json:"queueLimit,omitempty"`
	QueueWait  int `json:"queueWait,omitempty"`
	// what it charges against the official price, 0 for that (#819)
	PriceRate float64     `json:"priceRate,omitempty"`
	Models    []modelJSON `json:"models"`            // everything the vendor lists, exposed ones flagged
	Exposed   int         `json:"exposed"`           // how many reach the agents
	Draws     int         `json:"draws,omitempty"`   // how many of its models draw images (gateway.Drawers)
	DrawIDs   []string    `json:"drawIds,omitempty"` // those models' ids, listed apart in its editor
	Unlisted  bool        `json:"unlisted"`          // its models serve only through routing groups
	// Groups are the routing groups ("group/<id>") each of its models is
	// in, by model id: what an unlisted one is still used through, and the
	// editor names those in none
	Groups   map[string][]string `json:"groups,omitempty"`
	Off      bool                `json:"off"`                // switched off: kept, but agents get none of its models
	Contexts map[string]int      `json:"contexts,omitempty"` // the windows the user set, "*" for all its models
	Outputs  map[string]int      `json:"outputs,omitempty"`  // the reply limits the user set (provider.OutputsOf)
	Compacts map[string]int      `json:"compacts,omitempty"` // where Codex and Claude Code compact on its models (provider.CompactsOf)
	Fetched  *time.Time          `json:"fetched,omitempty"`  // when the list came from the vendor; the page says how long ago in its language
	// ListError is why a plugin's account has only the plugin's defaults
	// (provider.ListError): the editor says so under its models
	ListError string             `json:"listError,omitempty"`
	Agents    []providerAgent    `json:"agents"` // detected agents, current ones flagged
	Sponsored bool               `json:"sponsored"`
	KeyList   []provider.KeyInfo `json:"keyList"`           // its keys, in the order requests try them
	Account   *accountJSON       `json:"account,omitempty"` // a signed-in agent, see provider.Account
	// Move is where a built-in subscription stands with the community
	// plugin that can run it (provider.Move): set for those that have one
	Move *moveJSON `json:"move,omitempty"`
}

type moveJSON struct {
	Package string `json:"package"`
	// State is "" (built-in, never moved), "plugin", "back" or "failed"
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Why is a failed move's reason, which the page says in its language
	Why *provider.MoveWhy `json:"why,omitempty"`
}

// stepPlanJSON: whether a StepFun provider's platform sign-in is kept, and
// where and how the user gets one
type stepPlanJSON struct {
	Site        string `json:"site"`
	SignedIn    bool   `json:"signedIn"`
	URL         string `json:"url"`
	Bookmarklet string `json:"bookmarklet"`
}

type accountJSON struct {
	provider.Account
	Agent string `json:"agent"`     // the agent's id
	Name  string `json:"agentName"` // the agent's name, for "from Codex CLI's sign-in"
	Icon  string `json:"agentIcon"`
	// Logins are the agent's accounts magpie remembers, to switch between
	Logins []provider.Login `json:"logins,omitempty"`
	// Builtin is a plugin's provider id, for a plugin beside a built-in
	// subscription (cursor-plugin's cursor): its plan named as the built-in's
	Builtin string `json:"builtin,omitempty"`
	// WSL is the distro Claude Code runs in, for a Windows with none of its own
	WSL string `json:"wsl,omitempty"`
}

// accountLabel is the name and logo an account is shown with: its agent's,
// the subscription's for one no agent magpie configures, and a plugin's
// provider's for a plugin's sign-in, whose agent is "plugin" (#694 listed a
// removed Qoder as "plugin", with no logo).
func accountLabel(p provider.Provider) (name, icon string) {
	a := p.Account
	if a == nil {
		return p.Name, p.Icon
	}
	name, icon = a.Agent, "generic"
	if a.Agent == "factory" {
		// a Factory subscription is magpie's own sign-in, not Droid's
		name, icon = "Factory", "factory"
	} else if a.Agent == provider.MiMoID {
		// a Xiaomi MiMo account, not MiMo Code (the agent "mimo" also names)
		name, icon = "Xiaomi MiMo", "mimocode"
	} else if a.Agent == provider.ChatGPTAPIID {
		// a ChatGPT plan through OpenAI's API, not Codex's backend
		name, icon = "ChatGPT API", "openai"
	} else if ag, err := agent.Find(a.Agent); err == nil {
		name, icon = ag.Name, ag.Icon
	} else if a.Agent == "cursor" {
		// a Cursor subscription is served by the gateway, not an agent magpie configures
		name, icon = "Cursor CLI", "cursor"
	} else if a.Agent == "kiro" {
		// Kiro's sign-in is magpie's own, kiro-cli's or the Kiro IDE's
		name, icon = "Kiro", "kiro-color"
	} else if a.Agent == "antigravity" {
		name, icon = "Antigravity", "antigravity-color"
	} else if a.Agent == provider.WorkBuddyAIID {
		// WorkBuddy AI, the international build, isn't an agent magpie configures
		name, icon = "WorkBuddy AI", "workbuddy-color"
	} else if a.Agent == provider.CommandCodePlanID {
		// Command Code's CLI keeps the key its sign-in made
		name, icon = "Command Code", "commandcode"
	}
	if !p.IsPlugin() {
		return name, icon
	}
	if pp, ok := provider.PluginOf(p.ID); ok {
		// a plugin's sign-in: named for the provider it signs in to
		name, icon = pp.Name, pluginIcon(pp)
		if name == "" {
			name = p.Name
		}
		if provider.Moved(pp.ID) {
			icon = p.Icon // the built-in's, as it was
		}
		return name, icon
	}
	// its plugin not listed now: the provider's own name and logo
	name, icon = p.Name, p.Icon
	if icon == "" {
		icon = "generic"
	}
	return name, icon
}

type providerAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Current bool   `json:"current"` // this agent is on one of this provider's models now
	Model   string `json:"model,omitempty"`
	Group   string `json:"group,omitempty"` // through this routing group, one of whose members it is
}

type presetJSON struct {
	provider.PresetDef
	Added bool `json:"added"`
	// ZhipuTeam: a key of it may be on a team's GLM Coding Plan, whose
	// organization and project the editor offers to take
	ZhipuTeam bool `json:"zhipuTeam,omitempty"`
}

type gatewayJSON struct {
	URL     string   `json:"url"`
	LAN     bool     `json:"lan"`
	LANURLs []string `json:"lanURLs,omitempty"`
	Open    bool     `json:"open,omitempty"` // listens beyond loopback with no key: anyone reaching it is let in
	Running bool     `json:"running"`
	Mine    bool     `json:"mine"`   // this process serves it
	Window  bool     `json:"window"` // the magpie serving it shows its routing
	// Version is another magpie's, serving it, and Older says it is older
	// than this one: agents' requests are then sent as that version sends
	// them, without this one's fixes (#506)
	Version string         `json:"version,omitempty"`
	Older   bool           `json:"older,omitempty"`
	Models  int            `json:"models"`
	Calls   []gateway.Call `json:"calls"`
	// Lanes are the keys and accounts with requests out or waiting under
	// a limit on requests at once (#892), by provider#keyid or
	// provider@account
	Lanes   map[string]gateway.Lane `json:"lanes,omitempty"`
	Groups  []gwGroupJSON           `json:"groups"`  // the catalog's routing groups, listed before the models
	Archive archiveJSON             `json:"archive"` // the request archive's switch, and where it goes
}

// gwGroupJSON is a routing group as the Gateway view lists it.
type gwGroupJSON struct {
	ID        string   `json:"id"` // group/<id>, what a request names
	Name      string   `json:"name"`
	Icons     []string `json:"icons"`     // its providers', one each
	Providers []string `json:"providers"` // their names, in the group's order
	// what agents are told of it, as of a model: its reasoning levels,
	// whether it takes images (every member does) and the context and
	// output its members all hold
	Efforts []string `json:"efforts,omitempty"`
	Images  bool     `json:"images,omitempty"`
	Context int      `json:"context,omitempty"`
	Output  int      `json:"output,omitempty"`
	Members []string `json:"members,omitempty"` // the models it sends to, provider/model, in its order
}

type excludedJSON struct {
	provider.Exclusion
	Name string `json:"agentName"`
	Icon string `json:"agentIcon"`
}

type providersJSON struct {
	Providers []providerJSON `json:"providers"`
	Presets   []presetJSON   `json:"presets"`
	Excluded  []excludedJSON `json:"excluded"` // sign-ins magpie found but will not share
	Gateway   gatewayJSON    `json:"gateway"`
	// CodexDaemon is the account Codex's background app-server is still
	// signed in to after Codex was switched to another; "" when none is
	// left behind (provider.CodexDaemonStale).
	CodexDaemon string `json:"codexDaemon,omitempty"`
	// Plugins are the providers the plugins sign in to, for the add sheet
	Plugins []pluginSubJSON `json:"plugins"`
	// OnPlugins are the built-in subscriptions moved onto their plugins,
	// which the add sheet offers as the plugin's alone
	OnPlugins []string `json:"onPlugins,omitempty"`
	// Movable are the built-in subscriptions a community plugin can run,
	// deprecated in magpie itself: the add sheet marks them so
	Movable []string `json:"movable,omitempty"`
	// MovesTo is the plugin each of them goes to, for the add sheet's
	// offer to install it before signing in
	MovesTo map[string]string `json:"movesTo,omitempty"`
	// Moved is the agents the change moved off models it stopped serving
	// (agent.Reseat), for the page to say so.
	Moved []agent.Move `json:"moved,omitempty"`
	// Added and Had: of the keys pasted at once (keys/import), how many
	// were new and how many the provider had already.
	Added int `json:"added,omitempty"`
	Had   int `json:"had,omitempty"`
	// Removed: of the keys removed at once (keys/remove-many), how many.
	Removed int `json:"removed,omitempty"`
	// FileError is why providers.json can't be read (provider.FileError):
	// the page says so over what is listed, which is then the signed-in
	// accounts alone, never "add your first provider".
	FileError string `json:"fileError,omitempty"`
	// Fetching: accounts' lists are still being asked for
	// (provider.FetchingNew); the page asks again until they are in
	Fetching bool `json:"fetching,omitempty"`
}

// agentModel is the model an agent is on, as magpie's catalog names it.
func agentModel(a *agent.Agent) string {
	if len(a.Fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(a.Fields[0].Get(), "magpie/")
}

// agentUse is what an agent is on now: read once for the page, not again
// for each provider (reading it may ask the agent itself, as Alma's does).
type agentUse struct {
	*agent.Agent
	pid, model string
	group      provider.Group
	members    []provider.Member
	inGroup    bool
}

func agentUses(agents []*agent.Agent, findGroup func(string) (provider.Group, []provider.Member, bool)) []agentUse {
	out := make([]agentUse, 0, len(agents))
	for _, a := range agents {
		u := agentUse{Agent: a}
		v := agentModel(a)
		if strings.HasPrefix(v, provider.GroupPrefix) {
			u.group, u.members, u.inGroup = findGroup(v)
		} else if pid, model, ok := strings.Cut(v, "/"); ok {
			if _, err := provider.Find(pid); err == nil {
				u.pid, u.model = pid, model
			}
		}
		out = append(out, u)
	}
	return out
}

func providerInfo(p provider.Provider, agents []agentUse) providerJSON {
	out := providerJSON{
		ID: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Host: p.Host(),
		Chat: p.Chat, Responses: p.Responses, Anthropic: p.Anthropic, Decide: p.Decide, BaseAPI: p.BaseAPI, ModelTest: p.ModelTest(), DecideTest: p.AsksDecideModels(),
		Catalog: p.Catalog, Website: p.Website, KeysURL: p.KeysURL,
		Proxy: p.Proxy, AccountProxies: p.AccountProxies, AccountModels: p.AccountModels, AccountCaps: p.AccountCaps, Headers: p.Headers, Searches: p.Searches, Cline: p.ClinePinnable(), PinUpstream: p.PinUpstream, Unredacted: p.Unredacted, BalanceURL: p.BalanceURL, BalancePath: p.BalancePath, ModelsURL: p.ModelsURL,
		Ready: p.Ready(), Chosen: p.Models, Models: []modelJSON{}, Agents: []providerAgent{},
		Fallback: p.Fallback, Routing: p.Routing, Sink: p.Sink, Affinity: p.Affinity, KeepLogin: p.KeepLogin, KeepLoginAs: p.KeepLoginAs, Unlisted: p.Unlisted, Off: p.Off, Contexts: p.Contexts,
		MaxConcurrency: p.MaxConcurrency, PluginConcurrency: p.PluginConcurrency(), PriceRate: p.PriceRate,
		AccountConcurrency: p.AccountConcurrency, QueueLimit: p.QueueLimit, QueueWait: p.QueueWait,
		Outputs: provider.OutputsOf(p.ID), Compacts: provider.CompactsOf(p.ID),
	}
	if out.Fallback == nil {
		out.Fallback = []string{}
	}
	if out.Chosen == nil {
		out.Chosen = []string{}
	}
	if pr := provider.Preset(p.Preset); pr != nil {
		out.Sponsored = pr.Sponsored
		out.Key.Optional = pr.NoKey
	}
	out.BalanceToken.Takes, out.BalanceToken.Set = provider.TakesBalanceToken(p), p.BalanceToken != ""
	if provider.TakesZhipuTeam(p) {
		out.ZhipuTeam = &provider.ZhipuTeam{}
		if p.ZhipuTeam != nil {
			*out.ZhipuTeam = *p.ZhipuTeam
		}
	}
	if site := provider.StepFunSite(p); site != "" {
		out.StepPlan = &stepPlanJSON{site, provider.StepFunSignedIn(site), provider.StepFunSignInURL(site), provider.StepFunBookmarklet()}
	}
	out.Key.Set = p.Key != ""
	out.Key.Masked = provider.Mask(p.Key)
	if provider.Movable(p.ID) {
		m, _ := provider.MigrationOf(p.ID)
		out.Move = &moveJSON{Package: provider.MovePackage(p.ID), State: m.State, Error: m.Err, Why: m.Why}
		if m.State == provider.MoveMoving {
			out.Move.State = ""
		}
	}
	out.KeyList = p.KeyList()
	if out.KeyList == nil {
		out.KeyList = []provider.KeyInfo{}
	}
	// a key the gateway passes over after a failure says so on its row
	for i, k := range out.KeyList {
		if !k.On {
			continue
		}
		if r, ok := keyRestOf(p.RestKey(k)); ok {
			out.KeyList[i].Rest = &provider.KeyRest{Why: r.Why, Status: r.Status, Until: r.Until, Key: p.RestKey(k)}
		}
	}
	if !out.Key.Set && p.Ready() {
		out.Key.Optional = true
	}
	for id, gs := range provider.MemberGroups() {
		if m, ok := strings.CutPrefix(id, p.ID+"/"); ok {
			if out.Groups == nil {
				out.Groups = map[string][]string{}
			}
			out.Groups[m] = gs
		}
	}
	if a := p.Account; a != nil {
		out.Account = &accountJSON{Account: *a, Agent: a.Agent}
		out.Account.Name, out.Account.Icon = accountLabel(p)
		out.Account.Logins = provider.Logins(a.Agent)
		if a.Agent == "claude" {
			out.Account.WSL = provider.ClaudeInWSL()
		}
		if pp, ok := provider.PluginOf(p.ID); ok && p.IsPlugin() {
			// a plugin's sign-in: the page follows it by the provider's id
			out.Account.Agent, out.Account.Builtin = p.ID, pp.ID
			out.Account.Logins = provider.Logins(p.ID)
			if out.Icon == "" || out.Icon == "generic" {
				out.Icon = out.Account.Icon
			}
		}
	}
	exposed := map[string]bool{}
	for _, m := range p.Exposed() {
		exposed[m.ID] = true
	}
	seen := map[string]bool{}
	names, kept := p.ModelNames(), p.ModelEfforts()
	held := settings.Load()
	sames := held.ModelSameAs
	// a list fetched before magpie kept each model's most: the one Codex
	// CLI keeps says it
	var most []catalog.Model
	if p.Account != nil && p.Account.Agent == "codex" {
		most = catalog.Codex()
	}
	// read once for the list: read for each model, three times over, it
	// was most of the Providers page's wait with many models (lml on
	// Discord, Windows)
	set := settings.Load()
	named := func(m catalog.Model, on bool) modelJSON {
		images := m.Images || catalog.SeesImages(m.ID)
		if m.ImageInput != nil {
			images = *m.ImageInput
		}
		own := images
		said, imageSet := provider.ImageOverrideIn(set, p.ID, m.ID)
		if imageSet {
			images = said
		}
		j := modelJSON{ID: m.ID, Name: m.Name, Efforts: provider.EffortsOf(m), On: on, Context: p.WindowOf(m), Output: p.ReplyLimitIn(m, set), Listed: provider.ListedWindow(m), Max: m.MaxContext, Free: m.Free, Rate: m.Rate, RateWas: m.RateWas, Images: images, ImageSet: imageSet, Own: own}
		if i := slices.IndexFunc(most, func(c catalog.Model) bool { return c.ID == m.ID }); j.Max == 0 && i >= 0 {
			j.Max = most[i].MaxContext
		}
		if n, ok := names[m.ID]; ok {
			j.Default = cmp.Or(m.Name, m.ID)
			j.Name = n
		}
		if api, ok := p.ModelAPI(m.ID); ok {
			j.API = string(api)
		}
		for _, a := range p.ListedAPIs(m.ID) {
			if slices.Contains(provider.Protocols, a) {
				j.Auto = append(j.Auto, string(a))
			}
		}
		j.Same = sames[p.ID+"/"+m.ID]
		j.Merge = provider.MergeName(m.ID)
		if mp, ok := held.ModelPrices[p.ID+"/"+m.ID]; ok {
			if pr, bad := mp.Price(); bad == "" {
				j.Price = &pr
			}
		}
		if pr, ok := p.ListPrice(m.ID); ok {
			j.List = &pr
		} else if pr, ok := provider.MakerPrice(m.ID); ok {
			j.List = &pr
		}
		if len(j.Efforts) == 0 {
			j.Efforts, j.Given = provider.Levels, true
		}
		if k, ok := kept[m.ID]; ok {
			j.Kept = slices.DeleteFunc(slices.Clone(j.Efforts), func(e string) bool { return !slices.Contains(k, e) })
		}
		return j
	}
	for _, m := range p.Available() {
		seen[m.ID] = true
		out.Models = append(out.Models, named(m, exposed[m.ID]))
	}
	// OpenRouter's decision models, listed apart from its chat models
	// (ARNO on Discord), after them
	for _, m := range p.DecisionModels() {
		out.Deciders = append(out.Deciders, m.ID)
		if !seen[m.ID] {
			seen[m.ID] = true
			out.Models = append(out.Models, named(m, exposed[m.ID]))
		}
	}
	// picks the vendor list does not know go first, so they are visible
	for _, m := range p.Exposed() {
		if !seen[m.ID] {
			out.Models = append([]modelJSON{named(m, true)}, out.Models...)
		}
	}
	out.Exposed = len(exposed)
	// its image models aren't among those agents chat with; the editor
	// lists them apart, as Settings → Images is where one is picked
	for _, m := range gateway.Drawers(p) {
		out.DrawIDs = append(out.DrawIDs, m.ID)
	}
	out.Draws = len(out.DrawIDs)
	if t, ok := p.Listed(); ok {
		out.Fetched = &t
	}
	out.ListError = p.ListError()
	for _, a := range agents {
		pa := providerAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, Current: a.pid == p.ID, Model: a.model}
		if a.inGroup {
			for _, m := range a.members {
				if m.Provider.ID == p.ID {
					pa.Current, pa.Model, pa.Group = true, m.Model, a.group.Name
					break
				}
			}
		}
		out.Agents = append(out.Agents, pa)
	}
	return out
}

// keyRestOf is gateway.RestOf, swapped in tests.
var keyRestOf = gateway.RestOf

func providersState() providersJSON {
	// an account signed in since start-up is listed with its vendor's
	// models, not magpie's own list of them (#204): asked behind the page,
	// which is told so and asks again, never waited for (#541)
	provider.FetchNewBehind(8 * time.Second)
	agents := agent.Detected()
	s := providersJSON{Providers: []providerJSON{}, Presets: []presetJSON{}, Excluded: []excludedJSON{}}
	s.OnPlugins = provider.OnPlugins()
	s.Movable = provider.MovableIDs()
	for _, id := range s.Movable {
		if s.MovesTo == nil {
			s.MovesTo = map[string]string{}
		}
		s.MovesTo[id] = provider.MovePackage(id)
	}
	if err := provider.FileError(); err != nil {
		s.FileError = err.Error()
	}
	hidden := map[string]provider.Provider{}
	for _, p := range provider.Hidden() {
		hidden[p.ID] = p
	}
	for _, x := range provider.Excluded() {
		e := excludedJSON{Exclusion: x, Name: x.Agent, Icon: "generic"}
		if p, ok := hidden[x.Provider]; ok && x.Provider != "" {
			// a removed account as its row was named: a plugin's by the
			// provider it signs in to, not its agent "plugin" (#694)
			e.Name, e.Icon = accountLabel(p)
		} else if a, err := agent.Find(x.Agent); err == nil {
			e.Name, e.Icon = a.Name, a.Icon
		}
		s.Excluded = append(s.Excluded, e)
	}
	findGroup := provider.GroupFinder()
	uses := agentUses(agents, findGroup)
	have := map[string]bool{}
	for _, p := range provider.All() {
		// a preset is added once any provider is its, whatever its id
		have[p.ID], have[p.Preset] = true, true
		s.Providers = append(s.Providers, providerInfo(p, uses))
	}
	for _, pr := range provider.Presets() {
		team := provider.TakesZhipuTeam(provider.Provider{Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic})
		s.Presets = append(s.Presets, presetJSON{PresetDef: pr, Added: have[pr.ID], ZhipuTeam: team})
	}
	cat := provider.Catalog()
	s.Gateway = gatewayJSON{URL: gateway.URL(), Open: gateway.OpenToAnyone(), Models: len(cat), Calls: []gateway.Call{}, Groups: []gwGroupJSON{}}
	s.Gateway.LAN = settings.Load().LAN
	if s.Gateway.LAN {
		s.Gateway.LANURLs = gateway.LANURLs()
	}
	for _, e := range cat {
		if e.Group == "" {
			continue
		}
		g := gwGroupJSON{ID: e.ID, Name: e.Name, Icons: e.Icons, Efforts: e.Efforts, Images: e.Images, Context: e.Context, Output: e.Output}
		if _, ms, ok := findGroup(e.ID); ok {
			for _, m := range ms {
				if id := m.Provider.ID + "/" + m.Model; !slices.Contains(g.Members, id) {
					g.Members = append(g.Members, id)
				}
				if !slices.Contains(g.Providers, m.Provider.Name) {
					g.Providers = append(g.Providers, m.Provider.Name)
				}
			}
		}
		s.Gateway.Groups = append(s.Gateway.Groups, g)
	}
	if gw := served.Load(); gw != nil {
		s.Gateway.Running, s.Gateway.Mine, s.Gateway.Window = true, true, true
		s.Gateway.Calls = gw.Recent()
		s.Gateway.Lanes = gw.Lanes()
	} else {
		o := gateway.ServedBy()
		s.Gateway.Running, s.Gateway.Window, s.Gateway.Version = o.Running, o.Window, o.Version
		s.Gateway.Older = o.Running && update.Newer(gateway.Version, o.Version)
	}
	s.Gateway.Archive = archiveState()
	s.CodexDaemon = provider.CodexDaemonStale()
	s.Plugins = pluginSubs()
	s.Fetching = provider.FetchingNew()
	return s
}

// failMove is fail with why the move failed, for the page to say it in
// its reader's language.
func failMove(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(rw).Encode(map[string]any{"error": err.Error(), "why": provider.WhyOf(err)})
}

// moveProvider and moveBackProvider are provider.Move and MoveBack, for
// tests to stand in for.
var (
	moveProvider     = provider.Move
	moveBackProvider = provider.MoveBack
	adoptProvider    = provider.Adopt
)

// moveContext keeps a move going though the page that asked for it goes
// (closed, reloaded): stopped halfway, a move leaves accounts in neither
// place until it is run again.
func moveContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
}

func providerRoutes(mux *http.ServeMux, w Windows) {
	importAppsRoutes(mux)
	pluginRoutes(mux, w)
	traceRoutes(mux)
	contextRoutesAPI(mux)
	groupRoutes(mux)
	// how each key's or account's requests stand under its limit on
	// requests at once (#892), read every two seconds while a provider's
	// rows are shown; empty when the gateway runs elsewhere
	mux.HandleFunc("GET /api/lanes", func(rw http.ResponseWriter, r *http.Request) {
		lanes := map[string]gateway.Lane{}
		if gw := served.Load(); gw != nil {
			lanes = gw.Lanes()
		}
		writeJSON(rw, lanes)
	})
	// what the vendors' own status pages say of the APIs the providers
	// call (#971), so a vendor's outage isn't taken for a sign-in or quota
	// problem. Asked by the Providers and Usage pages; a page is read at
	// most once in upstream.Fresh, and the first look waits a little for it.
	mux.HandleFunc("GET /api/upstream", func(rw http.ResponseWriter, r *http.Request) {
		of := map[string]string{}
		var ids []string
		for _, p := range provider.All() {
			if v := upstream.VendorOf(p.Chat, p.Responses, p.Anthropic); v != "" {
				of[p.ID] = v
				if !slices.Contains(ids, v) {
					ids = append(ids, v)
				}
			}
		}
		writeJSON(rw, map[string]any{"vendors": upstream.Wait(4*time.Second, ids...), "providers": of})
	})
	mux.HandleFunc("GET /api/providers", func(rw http.ResponseWriter, r *http.Request) {
		// ?wait: an account just signed in opens in the editor with its
		// vendor's list, worth the wait there (#204)
		if r.URL.Query().Has("wait") {
			provider.FetchNew(8 * time.Second)
		}
		writeJSON(rw, providersState())
	})
	// the order the Providers tab lists them in, which is the order they
	// are tried in too (#499)
	mux.HandleFunc("POST /api/providers/arrange", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Order []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetOrder(in.Order); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, providersState())
	})
	// a picture for a provider, picked in the editor: kept by content before
	// the provider is saved, which then points at it. The page sends it as
	// base64 in JSON — the app's web view hands a scheme handler no body for
	// a File or Blob, so a picture posted as it is arrived empty — and the
	// bytes as they are are still taken.
	mux.HandleFunc("POST /api/icons", func(rw http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 2*provider.MaxIcon))
		if err != nil {
			fail(rw, err)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var in struct{ Data string }
			if err := json.Unmarshal(b, &in); err != nil {
				fail(rw, err)
				return
			}
			if b, err = base64.StdEncoding.DecodeString(in.Data); err != nil {
				fail(rw, err)
				return
			}
		}
		icon, err := provider.StoreIcon(b)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	// the site's own icon, found from the provider's base URL (#12)
	mux.HandleFunc("POST /api/icons/favicon", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Name string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		icon, err := provider.FaviconFor(r.Context(), in.URL, in.Name)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	mux.HandleFunc("GET /api/icons/{name}", func(rw http.ResponseWriter, r *http.Request) {
		f := provider.IconFile(r.PathValue("name"))
		if f == "" {
			http.NotFound(rw, r)
			return
		}
		// an SVG is only ever drawn as an image, never run
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		rw.Header().Set("Cache-Control", "max-age=31536000, immutable")
		http.ServeFile(rw, r, f)
	})
	mux.HandleFunc("POST /api/provider/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			provider.Provider
			// Proxy is the proxy its requests go through (#237), "" to
			// follow the global one; a save that leaves it out keeps it
			Proxy *string `json:"proxy"`
			// MaxConcurrency is how many requests each of its keys or
			// accounts has out at once: a number (0 none), null for what
			// its plugin says or none; a save that leaves it out keeps it
			MaxConcurrency json.RawMessage `json:"maxConcurrency"`
			// QueueLimit and QueueWait are how many may wait for each key
			// or account and for how many seconds (#892): numbers, 0 or
			// null for no bound; a save that leaves them out keeps them
			QueueLimit json.RawMessage `json:"queueLimit"`
			QueueWait  json.RawMessage `json:"queueWait"`
			// Limit, for accountconcurrency: the account's or key's own
			// limit, 0 for none, null for the provider's (#892)
			Limit *int `json:"limit"`
			// PriceRate is what it charges against the official price
			// (#819): a number, null or 0 for none; left out, it is kept
			PriceRate    json.RawMessage `json:"priceRate"`
			AccountOrder []string        `json:"accountOrder"`
			// New is set by the editor's Add: the provider is one more, never
			// one replacing the provider that has its id or name
			New bool `json:"new"`
			// ClearBalanceToken drops the saved balance token, which a
			// blank one in the form otherwise keeps
			ClearBalanceToken bool `json:"clearBalanceToken"`
			// From is the id the provider had: another is a rename
			From string `json:"from"`
			// CopyOf, with New, is the provider the new one is a copy of
			// (#268): its key and what else the form doesn't carry are
			// taken from it when left out
			CopyOf string `json:"copyOf"`
			// Model and ModelName, for name: the name the user gives one
			// of its models, "" for its own again
			Model     string `json:"model"`
			ModelName string `json:"modelName"`
			// Efforts, for efforts: the reasoning levels it offers, none
			// for all it has
			Efforts []string `json:"efforts"`
			// Images, for images: whether the model takes images. Nil
			// gives the vendor's answer back.
			Images *bool `json:"images"`
			// ModelPrefs, for save: the names, levels and images the
			// editor's Names & levels changed, by model id, made with the
			// rest of the Save and not a click at a time
			ModelPrefs map[string]provider.ModelPref `json:"modelPrefs"`
			// Outputs, for save: the reply limits the editor's Max output
			// says, by model id, "*" for all (provider.SetModelOutputs); a
			// save that leaves it out keeps them
			Outputs map[string]int `json:"outputs"`
			// Compacts, for save: the thresholds the editor's Compact at
			// says, by model id, "*" for all (provider.SetModelCompacts,
			// #876); a save that leaves it out keeps them
			Compacts map[string]int `json:"compacts"`
			// Routing and Affinity, for route, affinity and save: how
			// requests spread over its keys or accounts, and how long a
			// conversation stays with the one that answered it. The
			// editor's Save sends them only when picked there, a save
			// that leaves them out keeping them: the Routing page's Stays
			// was lost at each Save of the provider's editor, which never
			// sent it.
			Routing  *string `json:"routing"`
			Affinity *string `json:"affinity"`
			// Sink, for sink and save: whether one rate limited with
			// quota left goes to the back (provider.Provider.Sink); a
			// save that leaves it out keeps it
			Sink *bool `json:"sink"`
			// Test, for test: models to send a request each, in place of
			// one per endpoint
			Test []string `json:"test"`
			// DetectModels, for detect: models to ask on each API, each
			// answered on its own, in place of Model
			DetectModels []string `json:"detectModels"`
			// Account and Allow, for accountmodels: the account (its name)
			// or key (its id) and the models it alone serves, none for all
			// the provider's (#474)
			Account string   `json:"account"`
			Allow   []string `json:"allow"`
			// Cap, for accountcap: the share (1–99) of its windows the
			// account is used to at most, 0 for no cap
			Cap int `json:"cap"`
			// Typed, for test and models: the request carries the editor's
			// form, which is tried as it stands before a Save (see typed)
			Typed bool `json:"typed"`
			// Base, for detect: the base URL typed, asked as each API
			// takes it where the form has no URL of that API's own
			Base string `json:"base"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(rw, err)
			return
		}
		in := req.Provider
		if req.Routing != nil {
			in.Routing = *req.Routing
		}
		if req.Affinity != nil {
			in.Affinity = *req.Affinity
		}
		if req.Sink != nil {
			in.Sink = *req.Sink
		}
		var moved []agent.Move
		switch r.PathValue("action") {
		case "show":
			// a signed-in account the user removed, back with its picks
			if err := provider.ShowAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "move":
			// a built-in subscription's accounts onto its community plugin
			ctx, cancel := moveContext(r)
			defer cancel()
			if err := moveProvider(ctx, in.ID); err != nil {
				failMove(rw, err)
				return
			}
		case "adopt":
			// a built-in subscription not signed in to, onto its plugin
			// first: the plugin installed, and its sign-in the plugin's
			ctx, cancel := moveContext(r)
			defer cancel()
			if err := adoptProvider(ctx, in.ID); err != nil {
				failMove(rw, err)
				return
			}
		case "moveback":
			ctx, cancel := moveContext(r)
			defer cancel()
			if err := moveBackProvider(ctx, in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "quiet":
			// a removed account's "Add it back" line, dismissed (#116)
			if err := provider.QuietAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "tuck", "untuck":
			// a removed account hidden from the Add sheet's "Removed from
			// magpie" too, or listed there again (#116)
			if err := provider.TuckAccount(in.ID, r.PathValue("action") == "tuck"); err != nil {
				fail(rw, err)
				return
			}
		case "forget":
			// a removed account signed out for good: adding it back
			// later signs in afresh rather than bringing it back (#694)
			if err := provider.ForgetAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "save":
			// a preset needs nothing but the key; a saved provider keeps
			// its key when the form left it blank
			if pr, err := provider.FromPreset(in.Preset); err == nil && in.Chat == "" && in.Responses == "" && in.Anthropic == "" {
				pr.Key, pr.Models, pr.Fallback, pr.Headers, pr.BalanceToken, pr.Contexts = in.Key, in.Models, in.Fallback, in.Headers, in.BalanceToken, in.Contexts
				pr.ZhipuTeam = in.ZhipuTeam
				pr.Searches = in.Searches
				pr.PinUpstream = in.PinUpstream
				pr.Unredacted = in.Unredacted
				if in.Name != "" {
					pr.Name = in.Name
				}
				if in.ID != "" {
					pr.ID = in.ID
				}
				// a decision API's address the user gave, such as the
				// Cloudflare one naming the account
				if d := strings.TrimSpace(in.Decide); d != "" && pr.Decide != "" {
					pr.Decide = d
				}
				in = pr
			}
			cc, keepCC, err := concurrencyOf(req.MaxConcurrency)
			if err != nil {
				fail(rw, err)
				return
			}
			in.MaxConcurrency = cc
			ql, keepQL, err := queueOf(req.QueueLimit, "queue length")
			if err != nil {
				fail(rw, err)
				return
			}
			qw, keepQW, err := queueOf(req.QueueWait, "queue wait")
			if err != nil {
				fail(rw, err)
				return
			}
			if err := provider.CheckQueue(ql, qw); err != nil {
				fail(rw, err)
				return
			}
			in.QueueLimit, in.QueueWait = ql, qw
			rate, keepRate, err := priceRateOf(req.PriceRate)
			if err != nil {
				fail(rw, err)
				return
			}
			in.PriceRate = rate
			// many keys pasted into the key field (361 on Discord: a provider
			// added with hundreds): the first is the key, the others its
			// accounts, as Paste several adds them
			var moreKeys []string
			if ks := provider.SplitKeys(in.Key); len(ks) > 1 {
				saved := req.From
				if saved == "" {
					saved = in.ID
				}
				if o, err := provider.Find(saved); req.New || err != nil || o.Key != in.Key {
					in.Key, moreKeys = ks[0], ks[1:]
				}
			}
			var old *provider.Provider
			if req.New {
				// a second one of a preset, or a name already in use, is
				// added beside the first under the next free id
				if req.Proxy != nil {
					in.Proxy = *req.Proxy
				}
				add := provider.Add
				if req.CopyOf != "" {
					add = func(p provider.Provider) (string, error) { return provider.AddCopy(p, req.CopyOf) }
				}
				id, err := add(in)
				if err != nil {
					fail(rw, err)
					return
				}
				in.ID = id
			} else {
				// a rename saves the rest under the id it had, then moves it
				to := strings.ToLower(strings.TrimSpace(in.ID))
				rename := req.From != "" && req.From != to
				if rename {
					if to == "" || to != provider.Slug(to) {
						fail(rw, fmt.Errorf("a provider's id must be lowercase letters, digits and dashes, not %q", in.ID))
						return
					}
					in.ID = req.From
				}
				old, _ = provider.Find(in.ID)
				if req.Proxy != nil {
					in.Proxy = *req.Proxy
				} else if old != nil {
					in.Proxy = old.Proxy
				}
				if keepCC && old != nil {
					in.MaxConcurrency = old.MaxConcurrency
				}
				if keepQL && old != nil {
					in.QueueLimit = old.QueueLimit
				}
				if keepQW && old != nil {
					in.QueueWait = old.QueueWait
				}
				if keepRate && old != nil {
					in.PriceRate = old.PriceRate
				}
				// each account's own proxy likewise: {} clears them
				if in.AccountProxies == nil && old != nil {
					in.AccountProxies = old.AccountProxies
				}
				// each account's own models are set on their own, with
				// accountmodels
				if old != nil {
					in.AccountModels = old.AccountModels
				}
				// and their usage caps, with accountcap, and their limits on
				// requests at once, with accountconcurrency
				if old != nil {
					in.AccountCaps = old.AccountCaps
					in.AccountConcurrency = old.AccountConcurrency
				}
				// a Zhipu key's team likewise: {} clears it
				if in.ZhipuTeam == nil && old != nil {
					in.ZhipuTeam = old.ZhipuTeam
				}
				if in.Key == "" && old != nil {
					in.Key = old.Key
				}
				if in.BalanceToken == "" && old != nil && !req.ClearBalanceToken {
					in.BalanceToken = old.BalanceToken
				}
				if old != nil {
					// the other keys are kept apart, in the Accounts list
					in.Keys = old.Keys
					// the editor's picks, or as they were
					if req.Routing == nil {
						in.Routing = old.Routing
					}
					if req.Affinity == nil {
						in.Affinity = old.Affinity
					}
					if req.Sink == nil {
						in.Sink = old.Sink
					}
					in.KeepLogin = old.KeepLogin // set on its own, with keeplogin
					in.KeepLoginAs = old.KeepLoginAs
					in.Off = old.Off // and this with off and on
					if in.Contexts == nil {
						in.Contexts = old.Contexts // a save that doesn't say
					}
					if in.Key == old.Key {
						in.KeyName, in.KeyProtocol, in.KeyWeight = old.KeyName, old.KeyProtocol, old.KeyWeight
					}
					// what the editor doesn't show, set from the CLI or the
					// TUI, is kept: its tag, website and key page, and on a
					// preset's provider its Balance and Models URLs too,
					// which only a custom one's editor has fields for
					in.Family = cmp.Or(in.Family, old.Family)
					in.Website = cmp.Or(in.Website, old.Website)
					in.KeysURL = cmp.Or(in.KeysURL, old.KeysURL)
					if provider.Preset(in.Preset) != nil {
						in.BalanceURL = cmp.Or(in.BalanceURL, old.BalanceURL)
						in.BalancePath = cmp.Or(in.BalancePath, old.BalancePath)
						in.ModelsURL = cmp.Or(in.ModelsURL, old.ModelsURL)
					}
				}
				// the Base URL's API, as picked in the editor; a save that
				// doesn't say keeps it
				if in.BaseAPI == "" && old != nil {
					in.BaseAPI = old.BaseAPI
				}
				if in.Icon == "" && old != nil && in.Preset == "" {
					in.Icon = old.Icon
				}
				if err := provider.Save(in); err != nil {
					fail(rw, err)
					return
				}
				if rename {
					if _, err := agent.RenameProvider(in.ID, to); err != nil {
						fail(rw, err)
						return
					}
					in.ID = to
				}
			}
			if len(moreKeys) > 0 {
				// those it has already are passed over, not an error
				if added, had, err := provider.AddKeys(in.ID, moreKeys, in.KeyProtocol); err != nil && !(added == 0 && had > 0) {
					fail(rw, err)
					return
				}
			}
			if len(req.ModelPrefs) > 0 {
				if err := provider.SetModelPrefs(in.ID, req.ModelPrefs); err != nil {
					fail(rw, err)
					return
				}
			}
			provider.ForgetBalances()
			// a new key means a new vendor list is worth a try; keep it short
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && (old == nil || old.Key != p.Key) {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				p.Fetch(ctx)
				cancel()
			}
			// after the list is fetched: a limit has to name a model it has
			if req.Outputs != nil {
				if err := provider.SetModelOutputs(in.ID, req.Outputs); err != nil {
					fail(rw, err)
					return
				}
			}
			if req.Compacts != nil {
				if err := provider.SetModelCompacts(in.ID, req.Compacts); err != nil {
					fail(rw, err)
					return
				}
			}
		case "key":
			// the saved key, for the editor's Show button; it never
			// leaves this machine (the panel is served on loopback)
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"key": p.Key})
			return
		case "name":
			// the agents' own model lists follow, through catalog.Changed
			if err := provider.SetModelName(in.ID+"/"+req.Model, req.ModelName); err != nil {
				fail(rw, err)
				return
			}
		case "efforts":
			if err := provider.SetModelEfforts(in.ID+"/"+req.Model, req.Efforts); err != nil {
				fail(rw, err)
				return
			}
		case "images":
			if err := provider.SetModelImage(in.ID+"/"+req.Model, req.Images); err != nil {
				fail(rw, err)
				return
			}
		case "arrange":
			// Promoting the first row is the same account switch as Make first:
			// keep the agents' catalogs in sync with the new primary sign-in.
			var err error
			moved, err = agent.Reseat(func() error { return provider.SetAccountOrder(in.ID, req.AccountOrder) })
			if err != nil {
				fail(rw, err)
				return
			}
			agent.SyncCatalog()
		case "accountmodels":
			if err := provider.SetAccountModels(in.ID, req.Account, req.Allow); err != nil {
				fail(rw, err)
				return
			}
		case "accountcap":
			if err := provider.SetAccountCap(in.ID, req.Account, req.Cap); err != nil {
				fail(rw, err)
				return
			}
		case "accountconcurrency":
			if err := provider.SetAccountConcurrency(in.ID, req.Account, req.Limit); err != nil {
				fail(rw, err)
				return
			}
		case "route":
			if err := provider.SetRouting(in.ID, in.Routing); err != nil {
				fail(rw, err)
				return
			}
		case "off", "on":
			// switched off, it stays with its keys, but agents are given
			// none of its models; the files they keep them in follow,
			// through catalog.Changed, and the agents on one of its models
			// are moved to another
			var err error
			if moved, err = agent.Reseat(func() error {
				return provider.SetOff(in.ID, r.PathValue("action") == "off")
			}); err != nil {
				fail(rw, err)
				return
			}
			provider.ForgetBalances()
		case "keeplogin":
			// Codex or Claude Code stays signed in to the first account, or
			// to one of the user's choosing (keepLoginAs), which signs it in
			// to that one now, and back to the first when let go (#524)
			var err error
			moved, err = agent.Reseat(func() error {
				if in.KeepLogin && in.KeepLoginAs != "" {
					return provider.SetKeepLoginAs(in.ID, in.KeepLoginAs)
				}
				return provider.SetKeepLogin(in.ID, in.KeepLogin)
			})
			if err != nil {
				fail(rw, err)
				return
			}
			agent.SyncCatalog()
		case "affinity":
			if err := provider.SetAffinity(in.ID, in.Affinity); err != nil {
				fail(rw, err)
				return
			}
		case "sink":
			if err := provider.SetSink(in.ID, in.Sink); err != nil {
				fail(rw, err)
				return
			}
		case "delete":
			var err error
			if moved, err = agent.Reseat(func() error { return provider.Delete(in.ID) }); err != nil {
				fail(rw, err)
				return
			}
		case "test":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			saved := *p
			if req.Typed {
				q := typed(*p, in, req.Proxy)
				p = &q
			}
			if len(req.Test) > 0 {
				// an image model's test draws a picture, which takes longer
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
				defer cancel()
				writeJSON(rw, map[string]any{"results": p.TestModels(ctx, req.Test)})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			writeJSON(rw, struct {
				Results  []provider.Result `json:"results"`
				Provider providerJSON      `json:"provider"`
			}{p.Test(ctx), providerInfo(saved, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		case "detect":
			// which APIs answer at the URL typed (Model, or one from the
			// list for each), the form as it stands: a new provider's,
			// or a saved one's with its key when none is typed
			var p provider.Provider
			if in.ID != "" {
				saved, err := provider.Find(in.ID)
				if err != nil {
					fail(rw, err)
					return
				}
				p = *saved
			}
			p = typed(p, in, req.Proxy)
			if strings.TrimSpace(in.Decide) != "" {
				// a decision API is asked its smallest question, at
				// POST …/systemone (#647)
				ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
				defer cancel()
				writeJSON(rw, map[string]any{"results": []provider.Detection{p.DetectDecide(ctx, req.Model)}})
				return
			}
			if len(req.DetectModels) > 0 {
				// model by model, a few at a time (01huadalang: 应该能
				// 看出来选择的模型支持情况)
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
				defer cancel()
				each, sum, err := p.DetectModels(ctx, req.Base, req.DetectModels)
				if err != nil {
					fail(rw, err)
					return
				}
				writeJSON(rw, map[string]any{"results": sum, "models": each})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			got, err := p.Detect(ctx, req.Base, req.Model)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"results": got})
			return
		case "balance":
			// the editor's check of a balance as it stands in the form,
			// before a Save: the saved provider with the form's URL, field,
			// headers and token (a blank one keeping the saved)
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			q := *p
			q.BalanceURL, q.BalancePath = strings.TrimSpace(in.BalanceURL), strings.TrimSpace(in.BalancePath)
			if in.Headers != nil {
				q.Headers = in.Headers
			}
			if in.BalanceToken != "" {
				q.BalanceToken = in.BalanceToken
			} else if req.ClearBalanceToken {
				q.BalanceToken = ""
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			amount, ok, err := provider.Balance(ctx, q)
			out := map[string]any{"ok": ok, "amount": amount}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(rw, out)
			return
		case "unfetch":
			// the vendor's list, forgotten until the next Refresh
			if err := catalog.SaveLive(in.ID, "", nil); err != nil {
				fail(rw, err)
				return
			}
		case "list":
			// the vendor's list for the form as it stands, nothing saved:
			// the add form's Fetch models, picked from before the provider
			// is (#578: 添加供应商的时候，希望添加可以获取全模型的按钮)
			var p provider.Provider
			if pr, err := provider.FromPreset(in.Preset); err == nil {
				p = pr
			}
			if p.Name == "" {
				p.Name = cmp.Or(strings.TrimSpace(in.Name), "the vendor")
			}
			p = typed(p, in, req.Proxy)
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			ms, err := p.List(ctx)
			if err != nil {
				fail(rw, err)
				return
			}
			type listed struct {
				ID   string `json:"id"`
				Name string `json:"name,omitempty"`
			}
			out := make([]listed, 0, len(ms))
			for _, m := range ms {
				x := listed{ID: m.ID}
				if m.Name != m.ID {
					x.Name = m.Name
				}
				out = append(out, x)
			}
			writeJSON(rw, map[string]any{"models": out})
			return
		case "models":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			ask := *p
			if req.Typed {
				ask = typed(*p, in, req.Proxy)
			}
			ms, dropped, err := ask.Refetch(ctx)
			if err != nil {
				fail(rw, err)
				return
			}
			// the picks the vendor's list no longer has are gone from
			// the saved provider: the editor takes them out of its draft
			// too, or its Save wrote them back
			if q, err := provider.Find(p.ID); err == nil {
				p = q
			}
			writeJSON(rw, struct {
				Count    int          `json:"count"`
				Dropped  []string     `json:"dropped,omitempty"`
				Provider providerJSON `json:"provider"`
			}{len(ms), dropped, providerInfo(*p, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		default:
			http.NotFound(rw, r)
			return
		}
		st := providersState()
		st.Moved = moved
		writeJSON(rw, st)
	})
	// How much of its allowance each of an agent's accounts has used.
	mux.HandleFunc("GET /api/login/usage", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		writeJSON(rw, provider.WithCapped(provider.LoginUsage(ctx, r.URL.Query().Get("agent"))))
	})
	// Switching the account an agent is signed in to, among those magpie
	// remembers, and forgetting one.
	mux.HandleFunc("POST /api/login/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var change func() error
		switch r.PathValue("action") {
		case "switch":
			change = func() error { return provider.SwitchLogin(in.Agent, in.User) }
		case "forget":
			change = func() error { return provider.ForgetLogin(in.Agent, in.User) }
		case "on", "off":
			change = func() error { return provider.SetLoginOn(in.Agent, in.User, r.PathValue("action") == "on") }
		default:
			http.NotFound(rw, r)
			return
		}
		// the last account signed out or off takes the sign-in's models
		// away: the agents on them are moved to others
		moved, err := agent.Reseat(change)
		if err != nil {
			fail(rw, err)
			return
		}
		// an agent on its own models goes through magpie while more of
		// its accounts are on, and straight to its vendor again once not
		agent.SyncCatalog()
		st := providersState()
		st.Moved = moved
		writeJSON(rw, st)
	})
	// Codex's background app-server, left on the account before a switch:
	// restarting it (which ends the Codex sessions on it), or letting it be.
	mux.HandleFunc("POST /api/codex/daemon/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "restart":
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			if err := provider.RestartCodexDaemon(ctx); err != nil {
				fail(rw, err)
				return
			}
		case "dismiss":
			provider.DismissCodexDaemon()
		default:
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, providersState())
	})
	// A provider's several keys: add one, put one in use, name or remove it.
	mux.HandleFunc("POST /api/keys/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, Key, Name, Ref string
			Refs               []string
			Protocol           provider.Protocol
			Weight             int
		}
		var added, had, removed int
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		var moved []agent.Move
		switch r.PathValue("action") {
		case "add":
			err = provider.AddKey(in.ID, in.Name, in.Key, in.Protocol)
		case "import":
			// many keys pasted at once, one a line or comma separated
			added, had, err = provider.AddKeys(in.ID, provider.SplitKeys(in.Key), in.Protocol)
		case "protocol":
			err = provider.SetKeyProtocol(in.ID, in.Ref, in.Protocol)
		case "weight":
			err = provider.SetKeyWeight(in.ID, in.Ref, in.Weight)
		case "use":
			err = provider.UseKey(in.ID, in.Ref)
		case "remove":
			// the last key gone, or off, takes the provider's models away
			moved, err = agent.Reseat(func() error { return provider.RemoveKey(in.ID, in.Ref) })
		case "remove-many":
			// the keys picked in a long list, or every dead one, at once
			moved, err = agent.Reseat(func() (err error) { removed, err = provider.RemoveKeys(in.ID, in.Refs); return err })
		case "rename":
			err = provider.RenameKey(in.ID, in.Ref, in.Name)
		case "on", "off":
			moved, err = agent.Reseat(func() error { return provider.SetKeyOn(in.ID, in.Ref, r.PathValue("action") == "on") })
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		// a key that now takes requests has its models asked for: which
		// models a key sees is known only from its own list, and an off
		// key's isn't asked (#76), so until then a relay that hands out a
		// key per group would send the key nothing, or everything
		if a := r.PathValue("action"); a == "add" || a == "import" || a == "on" {
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && len(p.KeysOn()) > 1 {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				p.Fetch(ctx)
				cancel()
			}
		}
		st := providersState()
		st.Moved, st.Added, st.Had, st.Removed = moved, added, had, removed
		writeJSON(rw, st)
	})
	// Adding a subscription: magpie opens the vendor's sign-in in the
	// browser and the window follows it until the account is in.
	mux.HandleFunc("POST /api/signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, Site string } // Site: ZCode's "zai" or "bigmodel"
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		st, err := provider.StartSignInAt(in.Agent, in.Site)
		if err != nil {
			fail(rw, err)
			return
		}
		if st.URL != "" {
			// one that installs a CLI first has no URL yet: the window
			// opens it once there is one
			w.OpenURL(st.URL)
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("GET /api/signin/{id}", func(rw http.ResponseWriter, r *http.Request) {
		st, ok := provider.SignInStatus(r.PathValue("id"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/signin/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		provider.CancelSignIn(r.PathValue("id"))
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/signin/{id}/callback", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SubmitSignInCallback(r.PathValue("id"), in.URL); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// Accounts brought in from another tool's export (Antigravity's, from
	// Antigravity Cockpit, Antigravity Manager, CLIProxyAPI; ChatGPT's and
	// Claude's from CLIProxyAPI or the agents' own files), each file's
	// text as it is; each checked with the vendor before it is kept.
	mux.HandleFunc("POST /api/signin/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			Files []string
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		imp := provider.ImportGoogleAccounts
		if in.Agent == "codex" || in.Agent == "claude" {
			// ChatGPT and Claude sign-ins: CLIProxyAPI's auth files, Codex
			// CLI's auth.json, Claude Code's .credentials.json
			imp = provider.ImportLogins
		} else if in.Agent == "factory" {
			// Factory API keys (fk-…), as droid takes FACTORY_API_KEY (#506)
			imp = func(ctx context.Context, _ string, files []string) ([]provider.ImportedAccount, error) {
				return provider.ImportFactoryKeys(ctx, files)
			}
		}
		res, err := imp(ctx, in.Agent, in.Files)
		if err != nil {
			fail(rw, err)
			return
		}
		agent.SyncCatalog()
		writeJSON(rw, struct {
			Results   []provider.ImportedAccount `json:"results"`
			Providers providersJSON              `json:"providers"`
		}{res, providersState()})
	})
	// StepFun's Step Plan windows: the session the user copied from their
	// browser with magpie's bookmarklet
	mux.HandleFunc("POST /api/stepfun/{site}/session", func(rw http.ResponseWriter, r *http.Request) {
		site := r.PathValue("site")
		if provider.StepFunSignInURL(site) == "" {
			http.NotFound(rw, r)
			return
		}
		var in struct{ Text string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := provider.SaveStepFunPaste(ctx, site, in.Text); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, providersState())
	})
	mux.HandleFunc("POST /api/stepfun/{site}/signout", func(rw http.ResponseWriter, r *http.Request) {
		if err := provider.SignOutStepFun(r.PathValue("site")); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, providersState())
	})
	// the page copies through here first: in the app's window the
	// clipboard API is refused or missing, depending on the system
	mux.HandleFunc("POST /api/copy", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Text == "" || !w.Copy(in.Text) {
			http.Error(rw, "", http.StatusNotImplemented)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasPrefix(in.URL, "https://") || strings.HasPrefix(in.URL, "http://") || strings.HasPrefix(in.URL, agent.CindyScheme) {
			w.OpenURL(in.URL)
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

// typed is the saved provider p as the editor's form has it (in), for
// Refresh and Test models before a Save: a key pasted to replace the saved
// one is the one asked with, and the URLs, headers and proxy typed are
// where and how. Nothing is saved. What the form left blank (the key above
// all) is the saved one's; the provider's other keys are kept.
func typed(p, in provider.Provider, proxy *string) provider.Provider {
	if k := strings.TrimSpace(in.Key); k != "" && k != p.Key {
		p.Key, p.KeyName, p.KeyProtocol, p.KeyWeight = k, "", "", 0
	}
	for _, f := range []struct {
		to *string
		v  string
	}{{&p.Chat, in.Chat}, {&p.Responses, in.Responses}, {&p.Anthropic, in.Anthropic}, {&p.Decide, in.Decide}, {&p.ModelsURL, in.ModelsURL}} {
		if v := strings.TrimSpace(f.v); v != "" {
			*f.to = v
		}
	}
	if in.Headers != nil {
		p.Headers = in.Headers
	}
	if proxy != nil {
		p.Proxy = *proxy
	}
	return p
}

// queueOf is a save's queueLimit or queueWait: keep when the save left it
// out, 0 for null, else the number.
func queueOf(raw json.RawMessage, what string) (n int, keep bool, err error) {
	if len(raw) == 0 {
		return 0, true, nil
	}
	var v *int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("the %s must be a whole number, not %s", what, raw)
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, false, nil
}

// priceRateOf is a save's priceRate: keep when the save left it out, 0
// for null, else the rate.
func priceRateOf(raw json.RawMessage) (r float64, keep bool, err error) {
	if len(raw) == 0 {
		return 0, true, nil
	}
	var n *float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false, fmt.Errorf("a price rate is a number, like 0.8, not %s", raw)
	}
	if n == nil {
		return 0, false, nil
	}
	if bad := provider.PriceRateOK(*n); bad != "" {
		return 0, false, fmt.Errorf("%s, not %v", bad, *n)
	}
	return *n, false, nil
}

// concurrencyOf is a save's maxConcurrency: keep when the save left it
// out, nil for null (the plugin's, or none), else the number, 0 for none.
func concurrencyOf(raw json.RawMessage) (n *int, keep bool, err error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, false, fmt.Errorf("max concurrent requests must be a whole number, not %s", raw)
	}
	if n != nil && (*n < 0 || *n > 1000) {
		return nil, false, fmt.Errorf("max concurrent requests must be from 0 to 1000, not %d", *n)
	}
	return n, false, nil
}
