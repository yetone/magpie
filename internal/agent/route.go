package agent

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Every agent's model picker has two halves: the models the agent reaches
// on its own (its sign-in, its own keys), and magpie's catalog — every model
// of every provider the user added, reached through the local gateway.
// A catalog model is spelled "provider/model"; an agent that sends that to
// the gateway gets the vendor's reply in whichever API the agent speaks.

// magpieID is the provider id agents know the gateway by.
const magpieID = "magpie"

// RoutingGroups is the picker's group of routing groups.
const RoutingGroups = "Routing groups"

// viaMagpie lists the catalog as agent is shown it, for a picker: the
// routing groups first, in one group of their own, then one group per
// provider. Built again only when provider.StampNow moved, or
// magpieModelsFor after it was; each caller is given its own copy.
func viaMagpie(agent, prefix string) []Option {
	return slices.Clone(viaMagpieShared(agent, prefix))
}

// viaMagpieShared is viaMagpie without a copy of its own, for a caller
// that only reads it or appends it onto a list of its own: a copy for each
// of an agent's fields was 42 MB of what twenty window polls allocated.
// An append onto it makes a copy, but its Options are never written into.
func viaMagpieShared(agent, prefix string) []Option {
	return builtOptions.shared(agent+"\x00"+prefix, func() []Option { return buildVia(agent, prefix) })
}

// withVia is append(own, viaMagpieShared(agent, prefix)...), the list a
// model field offers, kept while own reads the same and the catalog's list
// is the one kept: the append copied the whole catalog for each field of
// each agent, every poll, about 60 MB of what forty polls of the Agents
// page allocated with real accounts. Read only, as viaMagpieShared's.
func withVia(own []Option, agent, prefix string) []Option {
	via := viaMagpieShared(agent, prefix)
	if len(own) == 0 {
		return via
	}
	return joinedVia(own, via, [2]string{agent, prefix})
}

// joinedVia is append(own, via...) for a field whose via is a shared list,
// kept under key while own reads the same and via is the list kept (see
// withVia). Read only; with no own models it is still a list apart from
// via, as Claude Code's model field has always given.
func joinedVia(own, via []Option, key [2]string) []Option {
	joinedOptions.Lock()
	defer joinedOptions.Unlock()
	if j, ok := joinedOptions.m[key]; ok && sameList(j.via, via) && slices.EqualFunc(j.own, own, sameOption) {
		return j.out
	}
	out := make([]Option, 0, len(own)+len(via))
	out = append(append(out, own...), via...)
	if joinedOptions.m == nil {
		joinedOptions.m = map[[2]string]joinedList{}
	}
	joinedOptions.m[key] = joinedList{own: out[:len(own)], via: via, out: out}
	return out
}

var joinedOptions struct {
	sync.Mutex
	m map[[2]string]joinedList
}

type joinedList struct{ own, via, out []Option }

// sameList reports whether a and b are the one list kept: the same array,
// as long.
func sameList(a, b []Option) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// sameOption reports whether a and b read the same, field by field.
func sameOption(a, b Option) bool {
	return a.Value == b.Value && a.Label == b.Label && a.Note == b.Note && a.Icon == b.Icon &&
		slices.Equal(a.Icons, b.Icons) && a.Group == b.Group && a.GroupIcon == b.GroupIcon &&
		a.Ref == b.Ref && a.Free == b.Free && a.Zero == b.Zero && a.Rate == b.Rate && a.RateWas == b.RateWas &&
		a.Context == b.Context && a.Takes == b.Takes && a.Direct == b.Direct && a.Via == b.Via &&
		a.Same == b.Same && a.Served == b.Served && a.Alias == b.Alias && a.FastFor == b.FastFor &&
		a.Fast == b.Fast && a.OwnFast == b.OwnFast && a.UltraOK == b.UltraOK && a.Ultra == b.Ultra &&
		a.own == b.own && a.sub == b.sub
}

// builtOptions is what viaMagpie built last, per agent and prefix. Each
// agent's model field offers it, and the window's drift poll asked every
// field's options of every agent each 15s: Claude Code's alone
// (claudeViaMagpie) allocated 4.7 GB of the 33 GB a live magpie did in 53
// minutes.
var builtOptions stampedLists[Option]

// buildVia is viaMagpie built afresh.
func buildVia(agent, prefix string) []Option {
	var out, groups []Option
	shown, _ := provider.CatalogFor(agent)
	// own: whether a provider is the account the agent is signed in to,
	// asked once a provider (OwnPaused reads the saved logins); a plugin's
	// for the agent's own vendor (Grok moved onto its plugin) is when it
	// is the account the agent itself is signed in to
	own := map[string]bool{}
	for _, e := range shown {
		a := e.Provider.Account
		if a == nil || a.StandIn() {
			continue
		}
		if _, ok := own[e.Provider.ID]; ok {
			continue
		}
		if a.Agent == agent {
			own[e.Provider.ID] = !e.Provider.OwnPaused()
		} else if e.Provider.PluginProvider() == agent && a.User != "" {
			own[e.Provider.ID] = a.User == provider.AgentUser(agent)
		}
	}
	// firstUser: the account a request for a subscription's model goes to
	// first, which the note names. A Codex or Claude Code subscription is
	// the account the agent is signed in to (Account.User), but its
	// requests go to the first in magpie's order: another one when that
	// one is paused, or when the agent is kept signed in to an account
	// that isn't first (#1957, parkavenue9639: every codex/gpt-* read
	// "A · via magpie" while each went to B). Asked once a provider.
	firsts := map[string]string{}
	firstUser := func(p provider.Provider) string {
		a := p.Account
		if a.Agent != p.ID || a.StandIn() {
			return a.User
		}
		u, ok := firsts[p.ID]
		if !ok {
			u = cmp.Or(provider.FirstLogin(a.Agent), a.User)
			firsts[p.ID] = u
		}
		return u
	}
	fast, ultra := provider.FastPicks(agent), provider.UltraPicks(agent)
	set := settings.Shared()   // the prices the user set, read once for the list
	var tiered map[string]bool // provider.UltraModels, read at the first fast model
	for _, e := range shown {
		if e.Group != "" {
			groups = append(groups, Option{Value: prefix + e.ID, Label: e.Name, Note: "routing group · via magpie",
				Icon: e.Provider.Icon, Icons: e.Icons, Group: RoutingGroups, Ref: e.ID})
			continue
		}
		note := e.Provider.Name + " · via magpie"
		if a := e.Provider.Account; a != nil {
			note = firstUser(e.Provider) + " · via magpie"
		}
		o := Option{Value: prefix + e.ID, Label: e.Name, Note: note,
			Icon: e.Provider.Icon, Group: e.Provider.Name, Ref: e.ID, Free: e.Free, Rate: e.Rate, RateWas: e.RateWas, Context: e.Context, own: own[e.Provider.ID],
			sub: e.Provider.Account != nil && (e.Provider.Account.Agent != "claude" || agent == "claude")}
		if !e.Free {
			o.Zero = e.Provider.PricedFree(set, e.Model)
		}
		// a model with a fast mode is switched fast in the picker (#954)
		if provider.CanFast(e.Provider, e.Model) {
			o.FastFor, o.Fast = agent, slices.Contains(fast, e.ID)
			o.OwnFast = provider.OwnFast(e.Provider, e.Model)
			if tiered == nil {
				tiered = provider.UltraModels()
			}
			if provider.UltraOn(e.Provider, e.Model, tiered) {
				o.UltraOK, o.Ultra = true, o.Fast && slices.Contains(ultra, e.ID)
			}
		}
		out = append(out, o)
	}
	return append(groups, out...)
}

// listLabels are the names the agent's own list calls the catalog's models
// by, by id: provider.Labels, which follows the Provider in model names
// setting, where an Option's Label is the name alone for magpie's picker.
func listLabels(agent string) map[string]string {
	shown, _ := provider.CatalogFor(agent)
	labels := provider.Labels(shown)
	out := make(map[string]string, len(shown))
	for i, e := range shown {
		out[e.ID] = labels[i]
	}
	return out
}

// isMagpie reports whether a model value is a catalog reference.
func isMagpie(v string) bool {
	if pid, _, ok := strings.Cut(v, "/"); ok && dryProviders[pid] {
		return true
	}
	if !strings.Contains(v, "/") {
		return false
	}
	return magpieRefs.get(v, func() bool {
		_, _, ok := provider.Resolve(v)
		return ok
	})
}

// magpieRefs keeps isMagpie's answers while provider.StampNow stays the
// same, for a minute at most. Outside a request that holds the catalog,
// each Resolve built every provider's entries again: the window's drift
// poll, every 15s, asked it for each agent's values, ~6 MB a poll (the
// procmem heap profile: 235 MB of 1.37 GB in 10 minutes of GUI use).
var magpieRefs stampedRefs

// stampedRefs is a model value → answer, dropped when the stamp moves,
// after a minute (what the stamp can't see: an account another program
// signed in to), or once it holds more values than any config has.
type stampedRefs struct {
	mu    sync.Mutex
	stamp provider.Stamp
	at    time.Time
	of    map[string]bool
}

const (
	stampedRefsFor = time.Minute
	stampedRefsMax = 4096
)

func (s *stampedRefs) get(v string, build func() bool) bool {
	st := provider.StampNow()
	s.mu.Lock()
	if s.stamp != st || time.Since(s.at) >= stampedRefsFor || len(s.of) >= stampedRefsMax {
		s.stamp, s.at, s.of = st, time.Now(), nil
	}
	if ok, found := s.of[v]; found {
		s.mu.Unlock()
		return ok
	}
	s.mu.Unlock()
	// built from what is there after st was taken: never older than st
	ok := build()
	s.mu.Lock()
	if s.stamp == st {
		if s.of == nil {
			s.of = map[string]bool{}
		}
		s.of[v] = ok
	}
	s.mu.Unlock()
	return ok
}

func firstOf(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

// magpieModels is the catalog as agent is shown it, as catalog.Models, for
// agents that keep their own model files: built again only when
// provider.StampNow moved, or magpieModelsFor after it was, each caller
// given its own copy of the list.
func magpieModels(agent string) []catalog.Model {
	return builtModels.get(agent, func() []catalog.Model { return buildModels(agent) })
}

// sharedModels is magpieModels without the caller's own copy, for one
// that only reads it: a range over it copies each model, so it can't
// write into the list kept. A copy per call was 37 MB of what forty polls
// of the window allocated with real accounts, most of it Pi's and Claude
// Code's thinking levels, read per field.
func sharedModels(agent string) []catalog.Model {
	return builtModels.shared(agent, func() []catalog.Model { return buildModels(agent) })
}

// builtModels is what magpieModels built last, per agent. The window asks
// /api/drift every 15s, and each agent's Drift asked for its list again
// per field, each built whole with its prices and Vision's ranking of the
// models that see: 14.5 GB of the 33 GB a live magpie allocated in 53
// minutes, the garbage its footprint peaks on.
var builtModels stampedLists[catalog.Model]

// stampedLists keeps a list built per key while provider.StampNow stays
// the same, magpieModelsFor at most, and gives each caller a copy.
type stampedLists[T any] struct {
	sync.Mutex
	m map[string]stampedList[T]
}

type stampedList[T any] struct {
	list  []T
	stamp provider.Stamp
	at    time.Time
}

func (s *stampedLists[T]) get(key string, build func() []T) []T {
	return slices.Clone(s.shared(key, build))
}

// shared is get without the caller's own copy: the list kept, read only.
func (s *stampedLists[T]) shared(key string, build func() []T) []T {
	s.Lock()
	b, ok := s.m[key]
	s.Unlock()
	// the stamp is read before the list is built: a change while it is
	// moves it past the one kept, and the next call builds it again
	stamp := provider.StampNow()
	if !ok || b.stamp != stamp || time.Since(b.at) >= magpieModelsFor {
		b = stampedList[T]{build(), stamp, time.Now()}
		s.Lock()
		if s.m == nil {
			s.m = map[string]stampedList[T]{}
		}
		s.m[key] = b
		s.Unlock()
	}
	return slices.Clip(b.list)
}

// magpieModelsFor is how long a list is kept while provider.StampNow says
// nothing changed: what the stamp can't see (another app rewriting an
// account's file, a model retired by its date, a provider resting, which
// Vision's pick of a model that sees follows) reaches the lists within it.
const magpieModelsFor = time.Minute

// buildModels is magpieModels built afresh. A model's own lists are
// clipped, so a caller appending to one in its copy makes a new one and
// never writes into the list kept.
func buildModels(agent string) []catalog.Model {
	// the providers built once for all the models' prices, not for each:
	// dsh's round at every start built them for each of hundreds
	defer provider.Hold()()
	var out []catalog.Model
	shown, _ := provider.CatalogFor(agent)
	labels := provider.Labels(shown)
	// a model magpie describes images to takes them (provider.Described)
	seen := describedNow()
	// the agent's own Long conversations, if it has one (ForAgent)
	st, find := settings.Load().ForAgent(agent), provider.FoundOnce(provider.GroupFinder())
	priceOf := provider.AgentPricer(st, find)
	members := func(id string) []provider.Member {
		_, ms, _ := find(id)
		return ms
	}
	for i, e := range shown {
		efforts := e.Efforts
		if agent == "codex" {
			// the levels Codex's picker offers, a model magpie has no
			// word on included (provider.CodexEfforts)
			efforts = provider.CodexEfforts(e)
		}
		m := catalog.Model{ID: e.ID, Name: labels[i], Provider: firstOf(e.Provider.Catalogs()), Efforts: efforts, Images: e.Images || seen, ImageInput: e.ImageInput, Context: e.Context, Output: e.Output, OutputSet: e.OutputSet, AgentsV2: e.AgentsV2, Reasoning: e.Thinks(),
			AlwaysThinks: e.AlwaysThinks, DynamicTools: e.DynamicTools, DefaultEffort: e.DefaultEffort}
		// where Codex and Claude Code compact it (#876): a threshold the
		// user set on the model or on its provider, else a window they
		// typed whole (#1809), else the one for every model, which codexcat
		// answers from the settings it is given
		m.Compact = provider.CompactFor(st, e, find)
		// what a call costs the user (Pi's cost, #781)
		if pr, ok := priceOf(e); ok {
			m.Price = &pr
		}
		if seen && !e.Images {
			yes := true
			m.ImageInput = &yes
		}
		// APIs is the preferred client protocol, preserving the vendor's
		// own content where possible. A group's members may differ, so a
		// group has Responses when one member is best there and every
		// other takes it as it is too (provider.GroupAPI; #1594,
		// ObsidianArch02): Pi, omp, droid and dsh then ask it there, and a
		// GPT group's tool-call arguments stream as the member sends them
		// instead of being held for a Chat answer
		if e.Group == "" {
			if n := e.Provider.Native(e.Model); n != "" {
				m.APIs = []string{string(n)}
			} else {
				// Gemini clients also reach Code Assist and Google plugins
				// through the gateway's Gemini API, with translation as needed.
				apis := e.Provider.APIs(e.Model)
				for _, pr := range e.Provider.Speaks() {
					if (pr == provider.Gemini || pr == provider.CodeAssist) && (apis == nil || slices.Contains(apis, pr)) {
						m.APIs = []string{string(provider.Gemini)}
						break
					}
				}
			}
		} else if api := provider.GroupAPI(members(e.ID)); api != "" {
			m.APIs = []string{string(api)}
		}
		// the service tiers Codex is offered on it, as the gateway's own
		// lists offer them: Fast in the Codex CLI magpie writes
		// magpie-models.json for (#1530)
		provider.CodexTiers(&m, e, members)
		if agent == "codex" {
			// the entry its vendor lists the model under in Codex's own
			// shape, handed on as it is (codexcat)
			m.Codex = e.Codex
		}
		// the gateway paths it goes on untranslated, as /v1/models says
		// them: what an agent plugin's connect is told (cleary5779)
		m.Endpoints = provider.NativeEndpoints(e)
		if e.Group != "" {
			// a group's, the APIs every member relays as it is
			// (provider.GroupEndpoints), which /v1/models leaves out: a
			// remote magpie sends a group's request on in the client's
			// API, for the other side to pick per member
			m.Endpoints = provider.GroupEndpoints(members(e.ID))
		}
		// Codex's /fast on a ChatGPT account's own GPT model, which
		// OpenCode has no command for and takes as variants (#2379); a
		// group is sent fast as the group says, as a fast pick is
		if a := e.Provider.Account; e.Group == "" && a != nil && a.Agent == "codex" {
			m.CodexFast = provider.CanFast(e.Provider, e.Model)
		}
		m.Efforts, m.Keys, m.APIs, m.Endpoints = slices.Clip(m.Efforts), slices.Clip(m.Keys), slices.Clip(m.APIs), slices.Clip(m.Endpoints)
		m.InputModalities, m.OutputModalities, m.Tiers, m.Codex = slices.Clip(m.InputModalities), slices.Clip(m.OutputModalities), slices.Clip(m.Tiers), slices.Clip(m.Codex)
		out = append(out, m)
	}
	return out
}

// describedNow is provider.Described, asked once for every agent's list
// while provider.StampNow stays the same, magpieModelsFor at most, as the
// lists are kept: each list built asked the gateway's Vision pick again,
// which ranks every provider's models that see, 553 MB of what a live
// magpie allocated in 37 minutes for the same answer a dozen times over.
func describedNow() bool {
	return builtSeen.shared("", func() []bool {
		return []bool{provider.Described != nil && provider.Described()}
	})[0]
}

var builtSeen stampedLists[bool]

// maxTokens is the output limit an agent is handed for m, kept within the
// context window it is handed with it: models.dev lists some models' output
// above their window (deepseek-chat's 384000 against 128000). An unknown
// window leaves the output as it is.
func maxTokens(m catalog.Model) int { return provider.OutputWithin(m.Context, m.Output) }

// outputOr is the output limit an agent is handed for m: the Max output the
// user set for it (catalog.Model.OutputSet), kept within its window, else
// capped, the agent's own trim of the vendor's figure. A cap of magpie's
// never overrides the user's number: DeepSeek V4.1 Flash set to 384k reached
// VS Code as 250000 and WorkBuddy as 128000 (#2064).
func outputOr(m catalog.Model, capped int) int {
	if n := maxTokens(m); m.OutputSet && n > 0 {
		return n
	}
	return capped
}

// group tags every option with a group name.
func group(name string, opts []Option) []Option {
	for i := range opts {
		opts[i].Group = name
	}
	return opts
}

func gatewayV1() string { return gateway.URL() + "/v1" }
