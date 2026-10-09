package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// codexClientVersion is the Codex CLI version the models list is asked for
// when Codex CLI has not asked itself yet: the list leaves out models newer
// than the client asking.
const codexClientVersion = "0.159.0"

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersion())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := sign(ctx, req, nil); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT models: %s", resp.Status)
	}
	saveCodexPrompts(b)
	ms := parseCodexModels(b)
	if len(ms) == 0 {
		return nil, errors.New("ChatGPT listed no Codex models")
	}
	return ms, nil
}

var codexVersionCache struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexVersion is the client version the models list is asked for: the
// newest of the Codex CLI installed, the one Codex CLI last asked with and
// codexClientVersion. The list leaves out models newer than the client
// asking, and Codex CLI's models_cache.json keeps the version it was written
// with until Codex next asks — after an update that brought new models
// (0.155 GPT-6 Luna and Sol), asking with it would leave them out.
func codexVersion() string {
	codexVersionCache.Lock()
	defer codexVersionCache.Unlock()
	if time.Since(codexVersionCache.at) >= 10*time.Minute {
		v := codexClientVersion
		newer := func(c string) {
			if c = claudeSemverRE.FindString(c); c != "" && compareClaudeVersion(c, v) > 0 {
				v = c
			}
		}
		var c struct {
			ClientVersion string `json:"client_version"`
		}
		if b, err := os.ReadFile(catalog.CodexModelsCache()); err == nil && json.Unmarshal(b, &c) == nil {
			newer(c.ClientVersion)
		}
		if exe := codexExecutable(); exe != "" {
			// "codex-cli 0.155.1"; read from its npm package, and not run
			// again once it failed (#864: macOS's malware alert each time)
			newer(codexCLIVersion(exe))
		}
		codexVersionCache.v, codexVersionCache.at = v, time.Now()
	}
	return newerVersion(codexVersionCache.v, codexSeen.get())
}

// codexCLIVersion is what the codex CLI at exe says its version is; a var
// so tests can fake it.
var codexCLIVersion = proc.Version

// newerVersion is the later of two versions, a when b isn't one.
func newerVersion(a, b string) string {
	if b = claudeSemverRE.FindString(b); b != "" && (a == "" || compareClaudeVersion(b, a) > 0) {
		return b
	}
	return a
}

// codexSeen is the newest version a Codex client that came through the
// gateway said it was. Codex CLI is often out of magpie's reach — installed
// where a desktop app's PATH doesn't go, its models_cache.json written by
// the version before an update — and the backend serves a model only to a
// client new enough for it: signed by the pool as an older Codex, a model
// Codex CLI reaches on its own answers 400 "The 'gpt-6.1-sol' model is not
// supported when using Codex with a ChatGPT account".
var codexSeen seenVersion

type seenVersion struct {
	sync.Mutex
	v string
}

func (s *seenVersion) get() string {
	s.Lock()
	defer s.Unlock()
	return s.v
}

func (s *seenVersion) saw(v string) {
	s.Lock()
	s.v = newerVersion(s.v, v)
	s.Unlock()
}

// SawCodexClient notes the version a Codex client's request says it is —
// its `version` header, else the one in its User-Agent (codex_cli_rs/0.159.0,
// Codex Desktop/0.159.0) — when the request is Codex's (it names an
// originator, or its User-Agent is codex_…).
func SawCodexClient(h http.Header) {
	ua := h.Get("User-Agent")
	if h.Get("originator") == "" && !strings.HasPrefix(strings.ToLower(ua), "codex") {
		return
	}
	v := claudeSemverRE.FindString(h.Get("version"))
	if v == "" {
		if _, rest, ok := strings.Cut(ua, "/"); ok {
			if m := claudeSemverRE.FindStringIndex(rest); m != nil && m[0] == 0 {
				v = rest[:m[1]]
			}
		}
	}
	if v == "" {
		return
	}
	codexSeen.saw(v)
}

// codexExecutable finds the codex CLI; a var so tests can fake it. Beyond
// PATH it looks where npm, nvm, bun, volta, pnpm, mise and the standalone
// installer put it, which a desktop app's PATH lacks.
var codexExecutable = func() string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	names := []string{"codex"}
	if runtime.GOOS == "windows" {
		names = []string{"codex.cmd", "codex.exe", "codex"}
	}
	for _, d := range proc.UserBinDirs() {
		for _, n := range names {
			p := filepath.Join(d, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// parseCodexModels reads the backend's list, the listed ones in its order.
func parseCodexModels(b []byte) []catalog.Model {
	var list struct {
		Models []struct {
			Slug        string   `json:"slug"`
			DisplayName string   `json:"display_name"`
			Visibility  string   `json:"visibility"`
			Priority    int      `json:"priority"`
			Input       []string `json:"input_modalities"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Context int `json:"context_window"`
			Max     int `json:"max_context_window"`
			Tiers   []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	sort.SliceStable(list.Models, func(i, j int) bool { return list.Models[i].Priority < list.Models[j].Priority })
	var out []catalog.Model
	for _, m := range list.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		mm := catalog.Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai", Context: m.Context}
		if m.Max > m.Context {
			mm.MaxContext = m.Max
		}
		if m.Input != nil {
			yes := slices.Contains(m.Input, "image")
			mm.ImageInput, mm.Images = &yes, yes
		}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		for _, t := range m.Tiers {
			mm.Tiers = append(mm.Tiers, t.ID)
		}
		out = append(out, mm)
	}
	return out
}

// accountModels names where one account's own model list is kept.
func accountModels(agent, user string) string {
	return agent + "@" + keyID(strings.ToLower(user))
}

// Lists reports whether the account's plan has the model, as far as magpie
// knows: one whose list was never fetched is taken to have them all.
func (a *Account) Lists(model string) bool {
	if a == nil {
		return true
	}
	if a.plugin != nil {
		return a.pluginLists(model)
	}
	live, _, ok := catalog.Live(accountModels(a.Agent, a.User))
	if !ok {
		return true
	}
	if a.Agent == "antigravity" {
		// Clients pick the family, while each account lists raw effort
		// variants. A secondary account may be the only one listing it.
		if variants, family := AntigravityVariants(model); family {
			return slices.ContainsFunc(live, func(m catalog.Model) bool {
				for _, id := range variants {
					if m.ID == id {
						return true
					}
				}
				return false
			})
		}
	}
	return slices.ContainsFunc(live, func(m catalog.Model) bool { return m.ID == model })
}

// Levels are the reasoning levels the account's own list gives the model —
// a Free ChatGPT plan's may be fewer than a Plus one's — and ok is false
// when that list wasn't fetched, doesn't have the model or gives it none.
func (a *Account) Levels(model string) (levels []string, ok bool) {
	if a == nil || a.plugin != nil {
		return nil, false
	}
	live, _, found := catalog.Live(accountModels(a.Agent, a.User))
	if !found {
		return nil, false
	}
	if a.Agent == "antigravity" {
		live = collapseAntigravityModels(live)
	}
	for _, m := range live {
		if m.ID == model && len(m.Efforts) > 0 {
			return m.Efforts, true
		}
	}
	return nil, false
}

// Tiers are the service tiers the account's own model list offers on
// model, when its list says: a list that gives no model a tier doesn't
// say, as one fetched before ChatGPT listed them.
func (a *Account) Tiers(model string) (tiers []string, ok bool) {
	if a == nil || a.plugin != nil {
		return nil, false
	}
	live, _, found := catalog.Live(accountModels(a.Agent, a.User))
	if !found || !slices.ContainsFunc(live, func(m catalog.Model) bool { return len(m.Tiers) > 0 }) {
		return nil, false
	}
	for _, m := range live {
		if m.ID == model {
			return m.Tiers, true
		}
	}
	return nil, false
}

// codexPoolModels is ms — the list of the account Codex is signed in to —
// with what the other accounts on add to it. A model only another account's
// plan has joins it (Raven on Discord: Codex signed in to a Free account
// beside a Pro 5x one, and the codex provider listed only the Free plan's
// three), right after the model before it in that account's list, as
// Antigravity's pool does (mergeAntigravityModels). And each model takes
// the reasoning levels any of them gives it: the provider's levels are what
// its accounts together take, so a Free account signed in, whose plan lacks
// high, doesn't lower the request a Plus one beside it answers (#520). The
// gateway sends each account only the models and levels its own list has
// (Account.Lists, Account.Levels) first.
func codexPoolModels(ms []catalog.Model) []catalog.Model {
	var lists [][]catalog.Model
	for _, l := range Logins("codex") {
		if l.Active || !l.On {
			continue
		}
		if live, _, ok := catalog.Live(accountModels("codex", l.User)); ok {
			lists = append(lists, live)
		}
	}
	if len(lists) == 0 {
		return ms
	}
	out := slices.Clone(ms)
	for _, live := range lists {
		next := 0
		for _, o := range live {
			i := slices.IndexFunc(out, func(m catalog.Model) bool { return m.ID == o.ID })
			if i < 0 {
				o.Efforts = slices.Clone(o.Efforts)
				out = slices.Insert(out, next, o)
				next++
				continue
			}
			next = max(next, i+1)
			if len(out[i].Efforts) == 0 {
				continue
			}
			efforts := slices.Clone(out[i].Efforts)
			for _, e := range o.Efforts {
				if !slices.Contains(efforts, e) {
					efforts = append(efforts, e)
				}
			}
			if len(efforts) > len(out[i].Efforts) {
				slices.SortStableFunc(efforts, func(a, b string) int { return levelRank(a) - levelRank(b) })
				out[i].Efforts = efforts
			}
		}
	}
	return out
}

// levelRank is a reasoning level's place among Levels; one it lacks goes last.
func levelRank(e string) int {
	if i := slices.Index(Levels, e); i >= 0 {
		return i
	}
	return len(Levels)
}

// codexFetchSaved asks for the models of each saved ChatGPT account that
// stands behind the one Codex is signed in to, with that account's own
// sign-in, so the gateway doesn't send one a model its plan lacks. One that
// can't be asked now keeps what it listed last.
func codexFetchSaved(ctx context.Context) {
	for _, l := range Logins("codex") {
		if l.Active || !l.On {
			continue
		}
		user := l.User
		sign := codexSign(func(ctx context.Context) (string, string, error) { return savedLoginToken(ctx, "codex", user) })
		// through the account's own proxy, if it has one
		if ms, err := codexModels(ViaLogin(ctx, "codex", user), sign); err == nil {
			catalog.SaveLive(accountModels("codex", user), CodexBase, ms)
		}
	}
}

// CodexListed is the catalog as a Codex signed in to ChatGPT is handed it,
// after the backend's own models: all but a ChatGPT account's in magpie,
// which the backend lists already. A group answers for its first member
// but is not that provider's.
func CodexListed() []catalog.Model {
	shown, _ := CatalogFor("codex")
	find := GroupFinder()
	return codexListed(shown, func(id string) []Member {
		_, ms, _ := find(id)
		return ms
	}, false)
}

// CodexCatalog is shown as a Codex that names magpie its model_provider is
// handed it from GET /v1/codex/models (#1281): every model, a ChatGPT
// account's own among them, since that Codex reaches them through magpie's
// /v1 by magpie's id, not through the ChatGPT backend's list.
func CodexCatalog(shown []Entry) []catalog.Model {
	find := GroupFinder()
	return codexListed(shown, func(id string) []Member {
		_, ms, _ := find(id)
		return ms
	}, true)
}

// CodexNativeHidden is the ChatGPT account's own model slugs the user took
// out of Codex's list (HiddenModels), or didn't pick for it when it is
// shown only the models picked (PickedModels): the backend lists them, and
// the gateway drops them from its /models answer as it does the ones not
// picked.
func CodexNativeHidden() map[string]bool {
	_, only := PickedModels("codex")
	if !only && len(HiddenModels("codex")) == 0 {
		return nil
	}
	off := ModelOff("codex")
	out := map[string]bool{}
	for _, e := range Catalog() {
		if CodexOwn(e) && off(e.ID) {
			out[e.Model] = true
		}
	}
	return out
}

// CodexOrder is where each model of Codex's list goes when the user put
// them in an order of their own on the Agents page (#855), by the slug
// Codex knows it by — a ChatGPT account's own by its bare one, as the
// backend lists it — and whether they did. A model it doesn't name keeps
// its place after them.
func CodexOrder() (map[string]int, bool) {
	order := ModelOrder("codex")
	if len(order) == 0 {
		return nil, false
	}
	at := make(map[string]int, len(order))
	for i, id := range order {
		at[id] = i
	}
	out := map[string]int{}
	for _, e := range Catalog() {
		i, ok := at[e.ID]
		if !ok {
			continue
		}
		slug := e.ID
		if CodexOwn(e) {
			slug = e.Model
		}
		if cur, seen := out[slug]; !seen || i < cur {
			out[slug] = i
		}
	}
	return out, true
}

// CodexListTag names the list Codex is handed, for its ETag: magpie's models,
// the account's own taken out of it, the order they are in, the windows set on them, the auto-review model, and whether its OpenAI models say
// multi-agent V1 (settings.CodexAgentsV1), so any of them changing has
// Codex ask for the list again.
func CodexListTag() string { return codexListTag("") }

// CodexOwnListTag names the list a Codex that reaches magpie for account
// failover alone is handed, its own models without magpie's (#1385). Neither
// it nor CodexListTag's is a part of the other, so Codex asks again when it
// moves from one list to the other; a V1 list's mark stays in front.
func CodexOwnListTag() string { return codexListTag("own") }

func codexListTag(kind string) string {
	ms := CodexListed()
	off := slices.Sorted(maps.Keys(CodexNativeHidden()))
	for _, slug := range off {
		ms = append(ms, catalog.Model{ID: "-" + slug})
	}
	// and the order the user put them in, the account's own among them
	for _, id := range ModelOrder("codex") {
		ms = append(ms, catalog.Model{ID: "^" + id})
	}
	ms = append(ms, codexWindowsTag()...)
	// and where a model with no threshold of its own is compacted, when
	// that isn't the working window
	if n := settings.Load().Compact(); n != settings.WorkingWindow {
		ms = append(ms, catalog.Model{ID: "~compact", Context: n})
	}
	// and the model Codex's auto-review runs on (#938)
	if v := settings.Load().CodexAutoReview; v != "" {
		ms = append(ms, catalog.Model{ID: "~autoreview:" + v})
	}
	return codexcat.PolicyTag(kind + codexcat.Tag(ms))
}

// CodexNativePicked is the set of the ChatGPT account's own model slugs the
// user kept, and whether they narrowed that list at all. The backend lists
// every model the account can reach; when the user has picked among them on
// the codex provider, the gateway keeps its /models answer to those (see
// codexModels). Not narrowed — the account's list is left whole.
//
// A provider switched off picks nothing, as it serves no agent anything
// (Provider.Off): its picks are kept for when it is switched on again and
// are not a narrowing now.
func CodexNativePicked() (map[string]bool, bool) {
	p, ok := find(All(), "codex")
	if !ok || p.Off || len(p.Models) == 0 {
		return nil, false
	}
	keep := make(map[string]bool, len(p.Models))
	for _, id := range p.Models {
		keep[id] = true
	}
	return keep, true
}

// CodexNativeWindow is the context window Codex is handed for one of the
// ChatGPT account's own models, by the backend's slug, and the most it may
// be raised to (0: as the entry says): the window the user set on the codex
// provider (Provider.ContextOf), as /v1/models says it (#674). ok is false
// for a model the user set none for, whose entry keeps the backend's window
// — unless restore, for an entry not fresh from the backend: Codex's
// models_cache.json keeps what magpie last handed it, a window since taken
// away among it, so such an entry takes the account's own list's again.
func CodexNativeWindow(restore bool) func(slug string) (n, most int, ok bool) {
	p, _ := find(All(), "codex")
	live, _, _ := catalog.Live("codex")
	return func(slug string) (int, int, bool) {
		if n := p.ContextOf(slug); n > 0 {
			return n, 0, true
		}
		if !restore {
			return 0, 0, false
		}
		for _, m := range live {
			if m.ID == slug && m.Context > 0 {
				// a list without the max (saved before magpie kept
				// it) leaves the cache's own; Window still raises it to
				// the window when it's below
				return m.Context, m.MaxContext, true
			}
		}
		return 0, 0, false
	}
}

// codexWindowsTag is the windows the user set on the codex provider, for
// the ETag of Codex's list: setting one, or taking it away, has Codex ask
// for the list again.
func codexWindowsTag() []catalog.Model {
	p, _ := find(All(), "codex")
	var out []catalog.Model
	for _, k := range slices.Sorted(maps.Keys(p.Contexts)) {
		out = append(out, catalog.Model{ID: "=" + k, Context: p.Contexts[k]})
	}
	return out
}

// codexListed marks a group Fast when a ChatGPT account's GPT model is in
// it, so Codex offers /fast there too; the tier goes out only to that
// account (buildResponses). own keeps a ChatGPT account's own models in,
// which a signed-in Codex has from the backend already.
func codexListed(shown []Entry, members func(id string) []Member, own bool) []catalog.Model {
	var ms []catalog.Model
	// named among all shown: the account's own, which the backend lists,
	// are in Codex's picker beside these
	labels := Labels(shown)
	seen := described()
	s := settings.Load()
	find := func(id string) (Group, []Member, bool) { return Group{}, members(id), true }
	for i, e := range shown {
		if CodexOwn(e) && !own {
			continue
		}
		m := catalog.Model{ID: e.ID, Name: labels[i], Efforts: e.Efforts, Images: e.Images || seen, Context: e.Context, AgentsV2: e.AgentsV2}
		m.Compact = compactSet(s, e.ID, find)
		// a provider the user added by its address is sent the tier Codex
		// asks for as it is (hsiangron on X)
		m.OwnTier = e.Group == "" && e.Provider.Preset == "" && e.Provider.Account == nil && e.Provider.ID != ""
		if e.Group != "" {
			for _, mb := range members(e.ID) {
				if a := mb.Provider.Account; a != nil && a.Agent == "codex" && strings.HasPrefix(mb.Model, "gpt-") {
					m.Fast = true
					break
				}
			}
		}
		ms = append(ms, m)
	}
	return ms
}
