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

// CodexLiveList is the ChatGPT backend's model list for the account Codex
// is signed in to now, as the backend sends it: its entries, their
// base_instructions among them, untouched. magpie writes it for the Codex
// app beside the sign-in (agent.CodexDesktopList), whose picker shows only
// the models a flag of OpenAI's names unless Codex reads its list from a
// file. An error when Codex is not signed in to ChatGPT, the backend
// doesn't answer 200, or its answer lists no model: never an empty list.
// CodexListRevoked tells one that is the backend calling the sign-in
// revoked.
func CodexLiveList(ctx context.Context) ([]byte, error) {
	home, _ := os.UserHomeDir()
	p, ok := codexAccount(home)
	if !ok || p.Account == nil || p.Account.sign == nil {
		return nil, errors.New("Codex is not signed in to ChatGPT")
	}
	ctx = ViaSignedIn(ctx, "codex")
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersion())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := p.Account.sign(ctx, req, nil); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// what it said, for CodexListRevoked
		return nil, fmt.Errorf("ChatGPT models: %w", &accountStatusError{status: resp.StatusCode, body: b[:min(len(b), 64<<10)]})
	}
	var list struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("ChatGPT models: %w", err)
	}
	if len(list.Models) == 0 {
		return nil, errors.New("ChatGPT listed no Codex models")
	}
	return b, nil
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

// ForgetCodexClientsForTest forgets the Codex version the gateway saw a
// client say it was and the one it found installed, for a test that asks
// what the pool says it is: both are kept for the process's life (the
// installed one for ten minutes, read in whichever test's HOME asked
// first), so an earlier test's Codex 0.165.0 spoke for the next test's
// 0.161.0 (#4807).
func ForgetCodexClientsForTest() {
	codexSeen.Lock()
	codexSeen.v = ""
	codexSeen.Unlock()
	codexVersionCache.Lock()
	codexVersionCache.v, codexVersionCache.at = "", time.Time{}
	codexVersionCache.Unlock()
}

// codexExecutable finds the codex CLI; a var so tests can fake it. Beyond
// PATH it looks where npm, nvm, bun, volta, pnpm, mise and the standalone
// installer put it, which a desktop app's PATH lacks.
var codexExecutable = func() string {
	if p, err := proc.LookPath("codex"); err == nil {
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
	if a.Agent == "claude" {
		// what the account's Claude Code was told it has: a gated model
		// (Claude Mythos 5.1) goes to the accounts entitled to it
		if has, ok := claudeLists(a.User, model); ok {
			return has
		}
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

// codexSavedListAge is how old a saved account's list is before it is
// asked for again, and codexSavedAtOnce how many of those one fetch of the
// active account's list asks. The active account's list is asked every
// accountListAge (KeepAccountsListed) as Codex asks its own; asking every
// switched-on saved account's beside it each hour was a burst of N
// /codex/models calls from one address for accounts no Codex was using
// (the audit after owl's deactivated accounts, 10-30). A plan's models
// change seldom: once a day, one account a look, so they drift apart.
const (
	codexSavedListAge = 24 * time.Hour
	codexSavedAtOnce  = 1
)

// codexSavedAsked is when each saved account's list was last asked for,
// fetched or not, so one that keeps failing (a deactivated account) waits
// its day like the others instead of being first every look.
var codexSavedAsked = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// codexSavedNow is codexFetchSaved's clock; a var so tests can move it.
var codexSavedNow = time.Now

// codexFetchSaved asks for the models of the saved ChatGPT accounts that
// stand behind the one Codex is signed in to, with each account's own
// sign-in, so the gateway doesn't send one a model its plan lacks. An
// account with no list yet is asked at once; of the rest, the one asked
// longest ago, once codexSavedListAge has passed, and no more than
// codexSavedAtOnce of them. One that can't be asked now keeps what it
// listed last.
func codexFetchSaved(ctx context.Context) {
	now := codexSavedNow()
	type due struct {
		user string
		at   time.Time
	}
	var fresh, stale []due
	ls := Logins("codex")
	codexSavedAsked.Lock()
	for _, l := range ls {
		if l.Active || !l.On {
			continue
		}
		_, at, ok := catalog.Live(accountModels("codex", l.User))
		if !ok {
			fresh = append(fresh, due{l.User, at})
			continue
		}
		if a := codexSavedAsked.m[strings.ToLower(l.User)]; a.After(at) {
			at = a
		}
		if now.Sub(at) >= codexSavedListAge {
			stale = append(stale, due{l.User, at})
		}
	}
	sort.SliceStable(stale, func(i, j int) bool { return stale[i].at.Before(stale[j].at) })
	ask := append(fresh, stale[:min(len(stale), codexSavedAtOnce)]...)
	for _, d := range ask {
		codexSavedAsked.m[strings.ToLower(d.user)] = now
	}
	codexSavedAsked.Unlock()
	for _, d := range ask {
		user := d.user
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
	for _, e := range catalogOf("codex") {
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
	for _, e := range catalogOf("codex") {
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

// buildCodexListTag is codexListTag built afresh from the catalog.
func buildCodexListTag(kind string) string {
	ms := CodexListed()
	off := slices.Sorted(maps.Keys(CodexNativeHidden()))
	for _, slug := range off {
		ms = append(ms, catalog.Model{ID: "-" + slug})
	}
	// and the account's own kept to the codex provider's picks, none of
	// them under Usage only (#2530), so a change of either has Codex ask
	// for the list again rather than keep the one it has
	if keep, narrowed := CodexNativePicked(); narrowed {
		ms = append(ms, catalog.Model{ID: "~picked"})
		for _, slug := range slices.Sorted(maps.Keys(keep)) {
			ms = append(ms, catalog.Model{ID: "~picked:" + slug})
		}
	}
	// and the models the user disabled (#1867)
	for _, pat := range DisabledModels() {
		ms = append(ms, catalog.Model{ID: "!" + pat})
	}
	// and the order the user put them in, the account's own among them
	for _, id := range ModelOrder("codex") {
		ms = append(ms, catalog.Model{ID: "^" + id})
	}
	ms = append(ms, codexWindowsTag()...)
	// and where a model with no threshold of its own is compacted, when
	// that isn't the working window
	if n := settings.Load().ForAgent("codex").Compact(); n != settings.WorkingWindow {
		ms = append(ms, catalog.Model{ID: "~compact", Context: n})
	}
	// and the model Codex's auto-review runs on (#938)
	if v := codexcat.AutoReviewModel(); v != "" {
		ms = append(ms, catalog.Model{ID: "~autoreview:" + v})
	}
	// and the effort new sessions start at, which magpie's models take as
	// their default (codexcat.ModelDefault): another one set, Codex asks
	// for the list again
	if v := codexcat.NewSessionEffort(); v != "" {
		ms = append(ms, catalog.Model{ID: "~effort:" + v})
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
// are not a narrowing now. Usage only narrows the visible native list to
// none, as it does the general catalog; codexModels keeps hidden jobs. Best
// in family narrows it to the newest of each family the account lists.
func CodexNativePicked() (map[string]bool, bool) {
	p, ok := find(All(), "codex")
	if !ok || p.Off {
		return nil, false
	}
	if p.ServesNone() {
		return nil, true
	}
	if len(p.Models) == 0 && p.BestInFamily {
		// Best in family: the newest of each family the account lists
		keep := map[string]bool{}
		for _, m := range catalog.BestInFamily(p.Available()) {
			keep[m.ID] = true
		}
		// with no list known, the account's own is left whole
		return keep, len(keep) > 0
	}
	if len(p.Models) == 0 {
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

// CodexWindow is the context window Codex works in for one of its own
// models, by the backend's slug, for a request to the ChatGPT backend or
// OpenAI's API that Codex sent through magpie: the window the user set on
// the codex provider, else the one the account's own list gives (user, ""
// for any), else magpie's Codex subscription's, else Codex CLI's
// models_cache.json's; 0 when none says. models.dev's input limit for the
// same id is OpenAI's API's (gpt-5.5's 922K), not the window Codex holds
// a conversation to (272K): netube on Discord saw Routing say 922K for a
// Codex Plus account.
func CodexWindow(user, slug string) int {
	p, _ := find(All(), "codex")
	if n := p.ContextOf(slug); n > 0 {
		return n
	}
	var lists [][]catalog.Model
	if user != "" {
		if live, _, ok := catalog.Live(accountModels("codex", user)); ok {
			lists = append(lists, live)
		}
	}
	if live, _, ok := catalog.Live("codex"); ok {
		lists = append(lists, live)
	}
	lists = append(lists, catalog.Codex())
	for _, ms := range lists {
		for _, m := range ms {
			if n := catalog.Window(m.Context); m.ID == slug && n > 0 {
				return n
			}
		}
	}
	return 0
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
	s := settings.Load().ForAgent("codex")
	find := func(id string) (Group, []Member, bool) { return Group{}, members(id), true }
	listed := func(e Entry) bool { return own || !CodexOwn(e) }
	apart := codexApart(shown, labels, listed)
	for i, e := range shown {
		if !listed(e) {
			continue
		}
		m := catalog.Model{ID: e.ID, Name: labels[i], Efforts: CodexEfforts(e), Images: e.Images || seen, Context: e.Context, AgentsV2: e.AgentsV2, Codex: e.Codex, Apart: apart[i]}
		m.Compact = CompactFor(s, e, find)
		CodexTiers(&m, e, members)
		ms = append(ms, m)
	}
	return ms
}

// codexUnknownLevels are the levels Codex is offered on a model magpie
// has no word on (CodexEfforts): the three every API that takes a
// reasoning effort reads, OpenAI's Chat and Responses and the relays
// copying them.
var codexUnknownLevels = []string{"low", "medium", "high"}

// CodexEfforts are the levels Codex's picker offers for e: its own, or,
// for a model nothing magpie reads says anything about — a custom
// provider's model-code, a relay's alias — low, medium and high. Codex
// sends such a model an effort whatever its entry lists (medium, or the
// one config.toml starts sessions at), and the gateway passes an effort
// on as asked where the model's levels aren't known (fitEffort), so with
// no levels in its entry the effort was sent and couldn't be changed
// (ccdcn on X: no effort choice for a custom provider's models). A model
// known to have no levels keeps none: one its vendor's catalog or
// models.dev lists without them, one the user said takes no reasoning
// ask, and a routing group, whose levels are its members'.
func CodexEfforts(e Entry) []string {
	if len(e.Efforts) > 0 || !e.levelsUnknown() {
		return e.Efforts
	}
	return slices.Clone(codexUnknownLevels)
}

// levelsUnknown reports whether nothing magpie reads says which reasoning
// levels e's model has, or that it has none: not its vendor's own list
// (levels, or an entry in Codex's shape), not the vendor's catalog, not
// models.dev under any provider, not the user.
func (e Entry) levelsUnknown() bool {
	if e.Group != "" || len(e.Efforts) > 0 || e.Reasonless || len(e.Codex) > 0 || e.Model == "" {
		return false
	}
	if _, ok := catalog.ListedBy(e.Provider.Catalogs(), e.Model); ok {
		return false
	}
	// models.dev lists it (glm-4.5-air, with no reasoning) or knows it
	// under another spelling: what it says stands
	return !catalog.Lists(e.Model) && !catalog.Knows(e.Model)
}

// codexApart is, for each of shown that is listed, what tells it from
// another listed entry its label (labels) would call the same in Codex's
// picker (catalog.Model.Apart): its provider's id, or its own id where
// that doesn't tell them apart (a routing group, or two of one provider).
// "" for every other entry, whose picker name stays as it was.
func codexApart(shown []Entry, labels []string, listed func(Entry) bool) []string {
	out := make([]string, len(shown))
	same := map[string][]int{}
	for i, e := range shown {
		if listed(e) {
			k := strings.ToLower(labels[i])
			same[k] = append(same[k], i)
		}
	}
	for _, is := range same {
		if len(is) < 2 {
			continue
		}
		ids := map[string]int{}
		for _, i := range is {
			ids[strings.ToLower(shown[i].Provider.ID)]++
		}
		for _, i := range is {
			e := shown[i]
			if e.Group == "" && e.Provider.ID != "" && ids[strings.ToLower(e.Provider.ID)] == 1 {
				out[i] = e.Provider.ID
			} else {
				out[i] = e.ID
			}
		}
	}
	return out
}

// CodexTiers sets on m what Codex is offered on e's service tiers
// (codexcat.Entries turns them into the entry's service_tiers): one source
// for every list Codex is handed, the gateway's and the models.json file
// magpie writes for a Codex CLI that names magpie its provider (#1530).
func CodexTiers(m *catalog.Model, e Entry, members func(id string) []Member) {
	// a provider the user added by its address is sent the tier Codex
	// asks for as it is (hsiangron on X)
	m.OwnTier = e.Group == "" && e.Provider.Preset == "" && e.Provider.Account == nil && e.Provider.ID != ""
	// another magpie offers the tiers its own Codex is offered on the
	// model, and is sent the one Codex asks for as it is (#1234); a
	// provider plugin, those it says its vendor serves the model at, and
	// is sent the one Codex asks for when it is among them (#2514)
	if e.Group == "" && (e.Provider.IsRemoteMagpie() || e.Provider.IsPlugin() && len(e.Tiers) > 0) {
		m.Tiers = e.Tiers
	}
	if e.Group != "" {
		fast := false
		for _, mb := range members(e.ID) {
			if a := mb.Provider.Account; a != nil && a.Agent == "codex" && strings.HasPrefix(mb.Model, "gpt-") {
				m.Fast = true
				break
			}
			fast = fast || CanFast(mb.Provider, mb.Model)
		}
		// a group with another member that has a fast mode (an OpenAI or
		// Anthropic key's, a relay's GPT, Cursor's, a plugin's) is offered
		// Fast too: Codex sends /fast's service_tier only on a model whose
		// entry lists it, and the gateway asks each member for it in its
		// vendor's words, or not at all where it has none (cute_lynx_36404
		// on Discord: /fast did nothing through a group)
		if !m.Fast && fast {
			m.Tiers = []string{codexFastTier}
		}
		return
	}
	// so is a model of its own that has one, which no tier above names
	// (an Anthropic key's Opus, an OpenAI key's GPT, a Cursor account's)
	if len(m.Tiers) == 0 && CanFast(e.Provider, e.Model) {
		m.Tiers = []string{codexFastTier}
	}
}

// codexFastTier is Fast's service tier as Codex sends it.
const codexFastTier = "priority"

// CodexListRevoked says err, from CodexLiveList, is the backend calling
// Codex's sign-in revoked (SignInRevoked), not a refusal a refresh mends,
// a 5xx or a network error.
func CodexListRevoked(err error) bool { return codexRevoked(err) }
